package jobs

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func duration(r Record) int64 {
	end := r.FinishedAtMS
	if end == 0 {
		end = time.Now().UnixMilli()
	}
	return end - r.StartedAtMS
}
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
func exitLabel(code *int) string {
	if code == nil {
		return "null"
	}
	return fmt.Sprint(*code)
}

func elapsedLabel(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	if ms < 10000 {
		return fmt.Sprintf("%.1f s", float64(ms)/1000)
	}
	if ms < 60000 {
		return fmt.Sprintf("%.0f s", float64(ms)/1000)
	}
	return fmt.Sprintf("%.1f min", float64(ms)/60000)
}

func jobLine(r Record) string {
	outcome := "running " + elapsedLabel(duration(r))
	if r.Status != "running" {
		outcome = r.Status
		if r.ExitCode != nil {
			outcome += " exit " + exitLabel(r.ExitCode)
		}
		outcome += " after " + elapsedLabel(duration(r))
	}
	return "- " + r.ID + " · " + r.CommandLabel + " · " + outcome
}

func (s *Service) jobView(r Record, output *Output, head bool) map[string]any {
	m := s.OutputMetadata(r)
	m["job_id"] = r.ID
	m["status"] = r.Status
	m["origin"] = r.Origin
	m["command"] = r.CommandLabel
	m["cwd"] = r.Cwd
	m["started_at"] = r.StartedAt
	m["finished_at"] = nullable(r.FinishedAt)
	m["elapsed_ms"] = duration(r)
	m["exit_code"] = exitValue(r.ExitCode)
	m["signal"] = nullable(r.Signal)
	m["stop_reason"] = nullable(r.StopReason)
	m["deadline_in_ms"] = nil
	if r.Status == "running" {
		m["deadline_in_ms"] = max(int64(0), r.DeadlineMS-time.Now().UnixMilli())
	}
	m["returned_bytes"] = 0
	if output != nil {
		m["returned_bytes"] = len(output.Stdout) + len(output.Stderr)
		m["output_mode"] = "tail"
		if head {
			m["output_mode"] = "head"
			m["stdout"] = output.Stdout
			m["stderr"] = output.Stderr
		} else {
			m["stdout_tail"] = output.Stdout
			m["stderr_tail"] = output.Stderr
		}
		m["stdout_bytes"] = output.StdoutBytes
		m["stderr_bytes"] = output.StderrBytes
		m["output_truncated"] = output.Truncated
	}
	return m
}

func number(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}
func outputReceipt(r Record, meta map[string]any, returned int) string {
	available := number(meta["available_stdout_bytes"]) + number(meta["available_stderr_bytes"])
	captured := number(meta["stdout_bytes"]) + number(meta["stderr_bytes"])
	growing := ""
	if meta["output_growing"] == true {
		growing = " (growing)"
	}
	summary := fmt.Sprintf("Output: %d captured bytes; %d retained rendered bytes; %d returned bytes%s.", captured, available, returned, growing)
	if meta["output_available"] == false {
		return summary + " Output expired under retention."
	}
	if available <= int64(returned) {
		return summary
	}
	return summary + fmt.Sprintf(" Read pages with jobs(job_ids=[%q], output=\"incremental\"). Omit cursor on the first page, then pass next_cursor unchanged. Or inspect output_files with Bash (input_job_ids=[%q] pins these logs): sed -n '1,80p' \"$CODEXPRO_JOB_OUTPUT_DIR/%s/stderr.log\". Use grep/rg if installed to locate matches; bound context/matches.", r.ID, r.ID, r.ID)
}

func outputBlocks(out Output, label string) string {
	parts := []string{}
	if out.Stdout != "" {
		parts = append(parts, "\n## stdout"+label+"\n\n```text\n"+out.Stdout+"\n```")
	}
	if out.Stderr != "" {
		parts = append(parts, "\n## stderr"+label+"\n\n```text\n"+out.Stderr+"\n```")
	}
	return strings.Join(parts, "\n")
}

func outputTail(value string) (string, bool) {
	trimmed := strings.TrimRightFunc(value, unicode.IsSpace)
	if trimmed == "" {
		return "", false
	}
	lines := strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n")
	truncated := len(lines) > 40
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	tail := strings.Join(lines, "\n")
	if len(tail) > 4096 {
		tail = tail[len(tail)-4096:]
		for len(tail) > 0 && tail[0]&0xc0 == 0x80 {
			tail = tail[1:]
		}
		truncated = true
	}
	if truncated {
		tail = "…\n" + tail
	}
	return tail, truncated
}

func nonEmptyLines(parts []string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}
func countLines(s string) int {
	n := 0
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if line != "" {
			n++
		}
	}
	return n
}

func (s *Service) bashText(r Record, out Output) string {
	stdout, outTruncated := outputTail(out.Stdout)
	stderr, errTruncated := outputTail(out.Stderr)
	duration := duration(r)
	if r.Status == "running" {
		status := fmt.Sprintf("Started as background job %s (%d ms so far).", r.ID, duration)
		if r.Origin == "promoted" {
			status = fmt.Sprintf("Still running after %d ms; it was moved to the background as job %s.", duration, r.ID)
		}
		parts := []string{"# Bash (background job)", "", "`" + r.Command + "`", "", "CWD: " + r.Cwd, status, fmt.Sprintf("Collect it with one jobs(job_ids=[%q], wait_ms=300000) call (waiting is cheaper than polling) or stop it with stop_jobs.", r.ID)}
		if stdout != "" {
			parts = append(parts, "\n## stdout so far\n\n```text\n"+stdout+"\n```")
		}
		if stderr != "" {
			parts = append(parts, "\n## stderr so far\n\n```text\n"+stderr+"\n```")
		}
		return nonEmptyLines(parts)
	}
	signal := ""
	if r.Signal != "" {
		signal = " (" + r.Signal + ")"
	}
	if s.cfg.String("bashTranscript", "tail") == "full" {
		return fmt.Sprintf("# Bash\n\n```bash\n$ %s\n```\n\nCWD: %s\nExit: %s%s\nDuration: %d ms\n\n## stdout\n\n```text\n%s\n```\n\n## stderr\n\n```text\n%s\n```", r.Command, r.Cwd, exitLabel(r.ExitCode), signal, duration, out.Stdout, out.Stderr)
	}
	stdoutLines, stderrLines := countLines(out.Stdout), countLines(out.Stderr)
	parts := []string{"# Bash", "", "`" + r.Command + "`", "", "CWD: " + r.Cwd, "Exit: " + exitLabel(r.ExitCode) + signal, fmt.Sprintf("Duration: %d ms", duration), fmt.Sprintf("Output: stdout %d line%s, stderr %d line%s.", stdoutLines, plural(stdoutLines), stderrLines, plural(stderrLines))}
	if stdout != "" {
		label := ""
		if outTruncated {
			label = " (tail)"
		}
		parts = append(parts, "\n## stdout"+label+"\n\n```text\n"+stdout+"\n```")
	}
	if stderr != "" {
		label := ""
		if errTruncated {
			label = " (tail)"
		}
		parts = append(parts, "\n## stderr"+label+"\n\n```text\n"+stderr+"\n```")
	}
	if outTruncated || errTruncated {
		parts = append(parts, "Only the tail is shown above; bounded stdout/stderr are in structured content. Use job output pages or managed files for more.")
	} else {
		parts = append(parts, "Bounded stdout/stderr are also in structured content.")
	}
	return nonEmptyLines(parts)
}

func redact(value any, paths []process.PathRedaction) any {
	switch v := value.(type) {
	case string:
		return process.RedactPaths(process.RedactSensitiveText(v), paths)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[k] = redact(item, paths)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redact(item, paths)
		}
		return out
	case []map[string]any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redact(item, paths)
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i, item := range v {
			out[i] = redact(item, paths).(string)
		}
		return out
	default:
		return value
	}
}

func (s *Service) success(w wire.Workspace, tool, title string, data map[string]any, text string) wire.Result {
	paths := s.paths(w)
	return wire.Success(tool, title, redact(data, paths).(map[string]any), redact(text, paths).(string))
}
