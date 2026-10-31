package batch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type childResult struct {
	ID, Tool                           string
	Index                              int
	OK, Skipped                        bool
	Text                               string
	TextTruncated, StructuredTruncated bool
	Structured                         any
	ChangedPaths                       []string
	Error                              string
}

// A completed command outcome is distinct from proof that its exact owned
// scope has drained. Only this read-only proof allows a Bash verification to
// count as successful. Failure and cancellation never acquire that proof.
func (s *Service) settled(ctx context.Context, id, workspace string) bool {
	s.mu.RLock()
	lookup := s.jobLookup
	s.mu.RUnlock()
	if lookup == nil || id == "" {
		return true
	}
	found, quiescent := lookup(id, workspace)
	if !found {
		return true
	}
	if quiescent {
		return true
	}
	deadline := time.NewTimer(time.Duration(max(1, s.cfg.Int("batchSettlementWaitMs", 30000))) * time.Millisecond)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-tick.C:
			found, quiescent = lookup(id, workspace)
			if !found {
				return false
			}
			if quiescent {
				return true
			}
		}
	}
}

func changedPaths(data map[string]any) []string {
	seen := map[string]bool{}
	out := []string{}
	appendValue := func(value any) {
		path, ok := value.(string)
		if !ok {
			if object, ok := value.(map[string]any); ok {
				path, _ = object["path"].(string)
			}
		}
		if path != "" && !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	appendValue(data["path"])
	for _, key := range []string{"paths", "changed_paths", "changed_files"} {
		switch values := data[key].(type) {
		case []any:
			for _, value := range values {
				appendValue(value)
			}
		case []string:
			for _, value := range values {
				appendValue(value)
			}
		case []map[string]any:
			for _, value := range values {
				appendValue(value)
			}
		}
	}
	return out
}

func (s *Service) run(ctx context.Context, scope wire.Scope, w wire.Workspace, op operation, textBudget, structuredBudget int) (result childResult) {
	result = childResult{ID: op.ID, Index: op.Index, Tool: op.Tool}
	// Domain handlers must return errors rather than panic. A failed optional
	// child cannot abort the remaining read-only siblings or tear down HTTP.
	defer func() {
		if failure := recover(); failure != nil {
			result.OK = false
			result.Error = fmt.Sprintf("Error: child tool panicked: %v", failure)
		}
	}()
	if s.caller == nil {
		result.Error = "Tool became unavailable."
		return result
	}
	childScope := scope
	childScope.RequestID = scope.RequestID + "/batch/" + op.ID
	childScope.StartedAt = time.Now()
	raw, err := s.caller.Call(ctx, childScope, op.Tool, clone(op.Validated))
	if err != nil {
		raw = wire.ErrorResult(err)
	}
	data := raw.StructuredContent
	if data == nil {
		data = map[string]any{}
	}
	exit, exitPresent := number(data["exit_code"])
	signal, _ := data["signal"].(string)
	timedOut := wire.Bool(data, "timed_out", false)
	incomplete := false
	if op.Tool == "bash" {
		incomplete = !exitPresent || data["job_status"] == "running"
		if !incomplete && !timedOut && signal == "" && exit == 0 && !raw.IsError {
			incomplete = !s.settled(ctx, wire.String(data, "job_id", ""), w.ID)
		}
	}
	bashFailed := op.Tool == "bash" && (incomplete || timedOut || signal != "" || !exitPresent || exit != 0)
	result.OK = !raw.IsError && !bashFailed
	// Match the legacy precedence even when a non-Bash child returns similarly
	// named data fields: its tool error remains the source of success/failure.
	switch {
	case timedOut:
		result.Error = "Bash command timed out."
	case signal != "":
		result.Error = fmt.Sprintf("Bash command terminated by signal %s.", signal)
	case exitPresent && exit != 0:
		result.Error = fmt.Sprintf("Bash command exited with code %v.", exit)
	case raw.IsError:
		if value, ok := data["error"]; ok {
			result.Error = fmt.Sprint(value)
		} else {
			result.Error = "Child tool returned an error."
		}
	case incomplete:
		result.Error = "Bash verification has not finished; inspect its job before checkpointing."
	}
	text := []string{}
	for _, block := range raw.Content {
		if block["type"] == "text" {
			if value, ok := block["text"].(string); ok {
				text = append(text, value)
			}
		}
	}
	if joined := strings.Join(text, "\n"); joined != "" {
		result.Text, result.TextTruncated = truncate(joined, textBudget, "\n...[batch child output truncated]")
	}
	if raw.StructuredContent != nil {
		result.Structured, result.StructuredTruncated = boundedStructured(raw.StructuredContent, structuredBudget)
	}
	if controlled(op.Tool) {
		result.ChangedPaths = changedPaths(data)
	}
	return result
}

func (s *Service) execute(ctx context.Context, scope wire.Scope, args map[string]any, p plan) wire.Result {
	operations := p.Operations[p.Start:]
	budget := max(0, s.cfg.Int("maxOutputBytes", 120000))
	reserve := min(budget/2, 512+len(operations)*96)
	childText := max(64, max(0, budget-reserve)/len(operations))
	childData := max(256, budget/max(2, len(operations)*2))
	results := make([]childResult, len(operations))
	if p.Mode == "parallel" {
		var wg sync.WaitGroup
		for i, op := range operations {
			wg.Add(1)
			go func(i int, op operation) {
				defer wg.Done()
				results[i] = s.run(ctx, scope, p.Workspace, op, childText, childData)
			}(i, op)
		}
		wg.Wait()
	} else {
		for i, op := range operations {
			results[i] = s.run(ctx, scope, p.Workspace, op, childText, childData)
			if !results[i].OK && !p.ContinueOnError {
				for j := i + 1; j < len(operations); j++ {
					skipped := operations[j]
					results[j] = childResult{ID: skipped.ID, Index: skipped.Index, Tool: skipped.Tool, Skipped: true, Error: "Skipped after an earlier operation failed."}
				}
				break
			}
		}
	}
	succeeded, failed, skipped, textTruncated, dataTruncated := 0, 0, 0, 0, 0
	var firstFailure *childResult
	paths := []string{}
	seenPaths := map[string]bool{}
	public := make([]map[string]any, len(results))
	sections := make([]string, len(results))
	executedBash := map[string]bool{}
	for i := range results {
		r := &results[i]
		if r.OK {
			succeeded++
			for _, path := range r.ChangedPaths {
				if !seenPaths[path] {
					seenPaths[path] = true
					paths = append(paths, path)
				}
			}
		} else if r.Skipped {
			skipped++
		} else {
			failed++
			if firstFailure == nil {
				firstFailure = r
			}
		}
		if r.TextTruncated {
			textTruncated++
		}
		if r.StructuredTruncated {
			dataTruncated++
		}
		row := map[string]any{"id": r.ID, "index": r.Index, "tool": r.Tool, "ok": r.OK, "text_in_content": r.Text != ""}
		if r.Skipped {
			row["skipped"] = true
		}
		if r.Error != "" {
			row["error"] = r.Error
		}
		if r.Structured != nil {
			row["structured"] = r.Structured
		}
		if r.TextTruncated {
			row["text_truncated"] = true
		}
		if r.StructuredTruncated {
			row["structured_truncated"] = true
		}
		if len(r.ChangedPaths) > 0 {
			row["changed_paths"] = r.ChangedPaths
		}
		public[i] = row
		marker, body := "✓", r.Text
		if r.OK {
			if body == "" {
				body = "Completed."
			}
		} else {
			marker = "✗"
			if r.Skipped {
				marker = "–"
			}
			body = r.Error
			if body == "" {
				body = "Failed."
			}
			if r.Text != "" {
				body += "\n\n" + r.Text
			}
		}
		sections[i] = fmt.Sprintf("## %s [%d] %s — %s\n\n%s", marker, r.Index, r.ID, r.Tool, body)
		if r.Tool == "bash" && !r.Skipped {
			executedBash[r.ID] = true
		}
	}
	lines := []string{"# Batch Operations", ""}
	if p.Path != "" {
		line := "Batch file: " + p.Path
		if p.Tag != "" {
			line += " (" + p.Tag + ")"
		}
		lines = append(lines, line)
	}
	persistence := " · one-shot"
	if p.Persisted {
		persistence = " · persisted"
	}
	lines = append(lines, "Source: "+p.Source+persistence, "Mode: "+p.Mode, fmt.Sprintf("Start: %d (%s)", p.Start, operations[0].ID), fmt.Sprintf("Operations: %d of %d", len(operations), len(p.Operations)), fmt.Sprintf("Succeeded: %d", succeeded), fmt.Sprintf("Failed: %d", failed), fmt.Sprintf("Skipped: %d", skipped))
	if p.Efficiency != "" {
		lines = append(lines, "", "Efficiency: "+p.Efficiency)
	}
	if p.Path != "" {
		line := "Stored definition: " + p.Path
		if firstFailure != nil {
			line = fmt.Sprintf("Resume: batch(path=\"%s\", from=\"%s\")", p.Path, firstFailure.ID)
		}
		lines = append(lines, "", line)
	}
	lines = append(lines, "")
	lines = append(lines, sections...)
	text, aggregateTruncated := truncate(strings.Join(lines, "\n"), budget, "\n...[batch aggregate output truncated]")
	visible := append([]string{}, p.Pruned[:min(len(p.Pruned), StoreLimit)]...)
	mutating := p.Source == "inline" && p.Persisted
	for _, op := range operations {
		if controlled(op.Tool) {
			mutating = true
		}
	}
	data := map[string]any{
		"batch_source": p.Source, "persisted": p.Persisted, "persistence_default": p.PersistenceDefault, "persistence_requested": p.PersistenceRequested, "auto_stored": p.AutoStored, "git_excluded": p.GitExcluded,
		"retention_limit": StoreLimit, "pruned_batch_count": len(p.Pruned), "pruned_batch_paths": visible, "pruned_batch_paths_truncated": len(visible) < len(p.Pruned), "mode": p.Mode, "mutating": mutating,
		"total_operation_count": len(p.Operations), "start_index": p.Start, "start_operation_id": operations[0].ID, "operation_count": len(operations), "executed_operation_count": len(results) - skipped,
		"succeeded_count": succeeded, "failed_count": failed, "skipped_count": skipped, "succeeded": failed == 0, "output_truncated": aggregateTruncated, "child_text_truncated_count": textTruncated, "child_structured_truncated_count": dataTruncated, "changed_paths": paths, "results": public,
	}
	if workspace, exists := args["workspace_id"]; exists {
		data["workspace_id"] = workspace
	}
	if p.Path != "" {
		data["batch_path"] = p.Path
	}
	if p.Tag != "" {
		data["batch_tag"] = p.Tag
	}
	if p.Efficiency != "" {
		data["efficiency_hint"] = p.Efficiency
	}
	if firstFailure != nil {
		data["failed_operation_id"] = firstFailure.ID
		data["failed_index"] = firstFailure.Index
		if p.Path != "" {
			data["resumable_from"] = firstFailure.ID
		}
	}
	result := wire.Success("batch", "Batch Operations", data, text)
	result.IsError = failed > 0
	scripts := []map[string]any{}
	for _, op := range operations {
		if executedBash[op.ID] {
			scripts = append(scripts, map[string]any{"operation_id": op.ID, "script": wire.String(op.Validated, "command", "")})
		}
	}
	if len(scripts) > 0 {
		result.DashboardMetadata = map[string]any{"shell_scripts": scripts}
	}
	return result
}
