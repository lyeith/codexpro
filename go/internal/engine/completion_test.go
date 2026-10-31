package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/jobs"
)

func TestCompletionAuditUsesOriginalBackgroundContext(t *testing.T) {
	for _, state := range []struct {
		status, expected string
		exit             int
	}{
		{"succeeded", "succeeded", 0}, {"failed", "failed", 7},
		{"stopped", "failed", 0}, {"timed_out", "timed_out", 0},
	} {
		t.Run(state.status, func(t *testing.T) {
			root := t.TempDir()
			journal, err := audit.Open(audit.Options{Directory: filepath.Join(root, "audit"), Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			// No workspace/files/git services are installed: the source callback
			// records a completion directly and does not inspect a workspace.
			engine := &Engine{Audit: journal}
			started := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
			job := jobs.Record{
				ID: "j_12345678", WorkspaceID: "ws_completion", ProjectID: "completion",
				Root: root, Cwd: ".", Command: "printf out", Origin: "background",
				Status: state.status, ExitCode: &state.exit,
				StartedAt: started.Format(time.RFC3339Nano), StartedAtMS: started.UnixMilli(),
				FinishedAt: started.Add(time.Second).Format(time.RFC3339Nano), FinishedAtMS: started.Add(time.Second).UnixMilli(),
				StdoutPath: filepath.Join(root, "stdout"), StderrPath: filepath.Join(root, "stderr"),
				CapturedStdoutBytes: 9999, CapturedStderrBytes: 8888,
				OwnerPrincipalID: "private-owner-must-not-be-public", OwnerSessionID: "private-session",
				RequestID: "private-request", ParentActionID: "private-parent",
			}
			if state.status == "timed_out" {
				job.StopReason = "timeout"
			}
			if err := os.WriteFile(job.StdoutPath, []byte("out\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(job.StderrPath, []byte("err\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := engine.recordCompletion(job); err != nil {
				t.Fatal(err)
			}
			if err := engine.recordCompletion(job); err != nil {
				t.Fatal(err)
			}
			result, err := journal.List(context.Background(), audit.Filter{ToolName: "bash_job", Limit: 10})
			if err != nil || len(result.Actions) != 1 {
				t.Fatalf("completion count=%d err=%v", len(result.Actions), err)
			}
			action := result.Actions[0]
			unknown := sha256.Sum256([]byte("unknown"))
			suffix := hex.EncodeToString(unknown[:])[:32]
			if action["actor_ref"] != "actor_"+suffix || action["request_ref"] != "request_"+suffix || action["status"] != state.expected {
				t.Fatalf("completion attribution/status differs from original callback: %v", action)
			}
			for _, key := range []string{"audit_scope", "transport_session_ref", "request_fingerprint", "parent_action_id", "git_before", "git_after", "path_evidence_before", "path_evidence_after"} {
				if _, ok := action[key]; ok {
					t.Fatalf("unexpected completion field %s", key)
				}
			}
			request, ok := action["request_metadata"].(map[string]any)
			if !ok || request["job_id"] != job.ID || request["origin"] != job.Origin {
				t.Fatalf("missing original request metadata: %#v", action["request_metadata"])
			}
			metadata, ok := action["result_metadata"].(map[string]any)
			if !ok || metadata["exit_code"] != float64(state.exit) || metadata["stdout_bytes"] != float64(4) || metadata["stderr_bytes"] != float64(4) || metadata["job_origin"] != "background" || metadata["timed_out"] != (state.status == "timed_out") {
				t.Fatalf("completion result metadata mismatch: %#v", action["result_metadata"])
			}
			encoded, _ := json.Marshal(action)
			if strings.Contains(string(encoded), "private-owner") || strings.Contains(string(encoded), "private-request") || strings.Contains(string(encoded), "private-parent") {
				t.Fatal("private outbox attribution escaped into the original public callback contract")
			}
		})
	}
}

func TestCompletionAuditUsesRetainedFileSizesAfterExpiry(t *testing.T) {
	root := t.TempDir()
	journal, err := audit.Open(audit.Options{Directory: filepath.Join(root, "audit"), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	engine := &Engine{Audit: journal}
	started := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	code := 0
	job := jobs.Record{ID: "j_87654321", WorkspaceID: "ws_expired", Command: "true", Cwd: ".", Origin: "background", Status: "succeeded", ExitCode: &code,
		StartedAt: started.Format(time.RFC3339Nano), StartedAtMS: started.UnixMilli(), FinishedAtMS: started.Add(time.Second).UnixMilli(),
		StdoutPath: filepath.Join(root, "expired-stdout"), StderrPath: filepath.Join(root, "expired-stderr"), CapturedStdoutBytes: 77, CapturedStderrBytes: 88, OutputExpired: true}
	if err := engine.recordCompletion(job); err != nil {
		t.Fatal(err)
	}
	result, err := journal.List(context.Background(), audit.Filter{ToolName: "bash_job", Limit: 10})
	if err != nil || len(result.Actions) != 1 {
		t.Fatalf("completion list: %v %v", result.Actions, err)
	}
	metadata := result.Actions[0]["result_metadata"].(map[string]any)
	if metadata["stdout_bytes"] != float64(0) || metadata["stderr_bytes"] != float64(0) {
		t.Fatalf("expired output retained stale physical byte counts: %v", metadata)
	}
	if _, ok := result.Actions[0]["project_id"]; ok {
		t.Fatal("unattributed completion invented an optional project ID")
	}
}
