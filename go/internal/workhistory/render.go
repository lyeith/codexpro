package workhistory

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func jsonData(value any) []byte {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return bytes.TrimSuffix(output.Bytes(), []byte("\n"))
}
func jsonBytes(value any) int { return len(jsonData(value)) }
func (s *Service) result(value map[string]any, offset, limit int) wire.Result {
	original, maxBytes := jsonBytes(value), s.options.PacketBytes
	essential := map[string]any{}
	packet := obj(value["packet"])
	warning := obj(packet["activity"])["warning"]
	if warning == nil {
		warning = value["warning"]
	}
	if warning != nil {
		essential["activity_warning"] = warning
	}
	copyKeys(essential, value, "run_id", "workspace_id", "context_dir", "project_id", "iteration_id", "state", "revision", "plan_revision", "spec_revision", "mode", "generation", "claimed", "inflight", "timing", "limits", "attempt_count", "next_offset", "next_sequence", "earliest_sequence", "latest_sequence", "has_more", "gap_detected", "total_items", "total_runs", "document_id", "document_revision", "checkpoint_id", "recovery_reason", "completion_matches_current_source")
	remaining := clone(value)
	delete(remaining, "packet")
	for key := range essential {
		delete(remaining, key)
	}
	packetBudget := max(768, (maxBytes-1800)/2-jsonBytes(essential))
	var rendered wire.Result
	for {
		if len(packet) > 0 {
			essential["packet"] = briefing(packet, packetBudget, offset, limit)
		}
		compactBudget := max(256, (maxBytes-1800)/2-jsonBytes(essential))
		if len(packet) > 0 {
			compactBudget = 256
		}
		compact, truncated := wire.BoundedStructured(remaining, compactBudget)
		body := clone(obj(compact))
		if body == nil {
			body = map[string]any{}
		}
		for key, value := range essential {
			body[key] = value
		}
		truncated = truncated || len(packet) > 0
		payload := clone(body)
		payload["return_size"] = map[string]any{"total_bytes": original, "returned_bytes": jsonBytes(body), "truncated": truncated}
		if truncated {
			if len(packet) > 0 {
				payload["handling"] = "Briefing only. Read full section pages with offset/limit; read_document with the referenced IDs/revisions and offset/max_bytes returns the complete spec/handoff."
			} else {
				payload["handling"] = "Repeat this offset/sequence with a smaller limit before advancing. Read full documents with read_document and offset/max_bytes."
			}
		}
		safe := obj(wire.RedactValue(payload))
		text := wire.Redact(string(jsonData(payload)))
		// The release wrapper adds the tool title after this bounded result.
		rendered = wire.Result{Meta: map[string]any{}, Content: []wire.Content{{"type": "text", "text": text}}, StructuredContent: safe}
		if len(packet) == 0 || jsonBytes(rendered) <= maxBytes {
			break
		}
		packetBudget = packetBudget * 3 / 4
		if packetBudget < 128 {
			break
		}
	}
	return rendered
}
func briefing(packet map[string]any, maxBytes, offset, limit int) map[string]any {
	documents := []map[string]any{}
	for _, raw := range list(packet["documents"]) {
		doc := obj(raw)
		if doc["kind"] == "spec" || doc["kind"] == "handoff" {
			item := map[string]any{}
			copyKeys(item, doc, "id", "revision", "kind")
			documents = append(documents, item)
		}
	}
	var checkpoint any
	if value := obj(packet["checkpoint"]); len(value) > 0 {
		item := map[string]any{}
		copyKeys(item, value, "summary", "next_action")
		checkpoint = item
	}
	brief := map[string]any{"objective": packet["objective"], "scope": packet["scope"], "checkpoint": checkpoint, "documents": documents, "todos": []any{}, "acceptance": []any{}, "pages": map[string]any{}, "truncated_fields": []string{}}
	pages := obj(brief["pages"])
	sections := []string{"todos", "acceptance"}
	for _, section := range sections {
		values := list(packet[section])
		var next any
		if offset < len(values) {
			next = offset
		}
		pages[section] = map[string]any{"offset": offset, "total_items": len(values), "next_offset": next}
	}
	for jsonBytes(brief) > maxBytes {
		type field struct {
			parent    map[string]any
			key, path string
			size      int
		}
		candidates := []field{{brief, "objective", "objective", 0}, {brief, "scope", "scope", 0}}
		if checkpointMap, ok := brief["checkpoint"].(map[string]any); ok {
			candidates = append(candidates, field{checkpointMap, "summary", "checkpoint.summary", 0}, field{checkpointMap, "next_action", "checkpoint.next_action", 0})
		}
		best := -1
		for i := range candidates {
			candidate := &candidates[i]
			if _, ok := candidate.parent[candidate.key].(string); !ok {
				continue
			}
			candidate.size = jsonBytes(candidate.parent[candidate.key])
			if candidate.size > 36 && (best < 0 || candidate.size > candidates[best].size) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		candidate := candidates[best]
		value, _ := wire.TruncateUTF8(str(candidate.parent[candidate.key]), len([]byte(str(candidate.parent[candidate.key])))/2, "…")
		candidate.parent[candidate.key] = value
		fields := brief["truncated_fields"].([]string)
		exists := false
		for _, name := range fields {
			if name == candidate.path {
				exists = true
			}
		}
		if !exists {
			brief["truncated_fields"] = append(fields, candidate.path)
		}
	}
	for index := offset; index < offset+limit; index++ {
		added := false
		for _, section := range sections {
			values := list(packet[section])
			page := obj(pages[section])
			if index >= len(values) || page["next_offset"] == nil || num(page["next_offset"]) != int64(index) {
				continue
			}
			retained := list(brief[section])
			brief[section] = append(retained, values[index])
			if index+1 < len(values) {
				page["next_offset"] = index + 1
			} else {
				page["next_offset"] = nil
			}
			if jsonBytes(brief) > maxBytes {
				brief[section] = retained
				page["next_offset"] = index
			} else {
				added = true
			}
		}
		if !added {
			break
		}
	}
	return brief
}
func (s *Service) activity(ctx context.Context, run map[string]any, after *int64) (map[string]any, error) {
	if s.options.Audit != nil {
		return s.options.Audit.RecentActivity(ctx, str(run["project_id"]), str(obj(run["workspace"])["id"]), after)
	}
	return map[string]any{"enabled": false, "last_recorded_project_change": nil, "latest_recorded_activity": nil, "actions": []any{}, "latest_sequence": 0, "next_sequence": 0, "earliest_sequence": 0, "has_more": false, "gap_detected": false, "coverage": audit.RecentActivityCoverage}, nil
}
