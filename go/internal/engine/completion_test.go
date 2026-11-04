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
	"github.com/lyeith/codexpro/go/internal/wire"
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
				Agent: "SP-3",
				ID:    "j_12345678", WorkspaceID: "ws_completion", ProjectID: "completion",
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
			if action["agent"] != job.Agent {
				t.Fatalf("completion lost caller label: %v", action)
			}
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
			if !ok || metadata["job_agent"] != job.Agent {
				t.Fatalf("completion lost original job label: %v", metadata)
			}
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

func TestNamedCompletionMapsPersistedOwnerToAudit(t *testing.T) {
	j, err := audit.Open(audit.Options{Directory: t.TempDir(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	e := Engine{Audit: j}
	job := jobs.Record{ID: "job_00000008", Origin: "background", Status: "succeeded", StartedAt: "2026-10-05T00:00:00Z", StartedAtMS: 1, FinishedAtMS: 2, OwnerPrincipalID: "principal:alpha", Agent: "SP-3", Identity: wire.Identity{Source: "named", PrincipalID: "alpha", CredentialID: "dev-a", AgentID: "crew-a", AgentKind: "crew"}}
	if err = e.recordCompletion(job); err != nil {
		t.Fatal(err)
	}
	list, err := j.List(context.Background(), audit.Filter{ToolName: "bash_job", Limit: 1})
	if err != nil || len(list.Actions) != 1 {
		t.Fatal("missing completion", err)
	}
	a := list.Actions[0]
	id, ok := a["identity"].(map[string]any)
	if !ok || id["principal_id"] != "alpha" || id["credential_id"] != "dev-a" || id["agent_id"] != "crew-a" || a["agent"] != "SP-3" {
		t.Fatalf("incomplete caller provenance: %+v", a)
	}
	digest := sha256.Sum256([]byte(job.OwnerPrincipalID))
	if a["actor_ref"] != "actor_"+hex.EncodeToString(digest[:])[:32] {
		t.Fatal("wrong completion actor")
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
