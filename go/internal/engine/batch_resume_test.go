package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/batch"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func resumePublicRows(t *testing.T, result wire.Result) []any {
	t.Helper()
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	rows, ok := value["results"].([]any)
	if !ok {
		t.Fatalf("batch result has no child receipts: %#v", value)
	}
	return rows
}

func TestBatchResumeUsesRealBashOutcomeWithoutRepeatingWrite(t *testing.T) {
	e, scope, id, root := batchWrapperFixture(t)
	e.Config.Set("jobTimeoutMs", 5000)
	e.Config.Set("bashTimeoutMs", 4000)
	e.Config.Set("maxBashTimeoutMs", 5000)
	service, ok := e.Batch.(*batch.Service)
	if !ok {
		t.Fatal("batch fixture lacks real batch service")
	}
	service.SetJobLookup(func(jobID, workspaceID string) (bool, bool) {
		job, found := e.Jobs.Get(jobID, workspaceID)
		return found, job.Quiescent
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	operations := []any{
		map[string]any{"id": "write", "tool": "write", "args": map[string]any{"path": "written.txt", "content": "written once\n"}},
		map[string]any{"id": "verify", "tool": "bash", "args": map[string]any{"command": "test -f gate && printf 'verified\\n'", "cwd": ".", "on_timeout": "background"}},
		map[string]any{"id": "inspect", "tool": "read", "args": map[string]any{"path": "written.txt"}},
	}
	first, err := e.Call(ctx, scope, "batch", map[string]any{"workspace_id": id, "operations": operations})
	if err != nil || !first.IsError {
		t.Fatalf("missing gate should fail verification: %v %#v", err, first)
	}
	rows := resumePublicRows(t, first)
	if len(rows) != 3 || rows[1].(map[string]any)["error"] != "Bash command exited with code 1." || rows[2].(map[string]any)["skipped"] != true {
		t.Fatalf("real Bash failure lost exit precedence: %#v", rows)
	}
	path := wire.String(first.StructuredContent, "batch_path", "")
	if path == "" {
		t.Fatal("verification workflow was not persisted")
	}
	// A second write must be observable even when its content would be identical.
	written := filepath.Join(root, "written.txt")
	stamp := time.Unix(1234567890, 0)
	if err := os.Chtimes(written, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gate"), []byte("ready\n"), 0600); err != nil {
		t.Fatal(err)
	}
	scope.RequestID = "batch-resume-second-call"
	resumed, err := e.Call(ctx, scope, "batch", map[string]any{"workspace_id": id, "path": path, "from": "verify"})
	if err != nil || resumed.IsError {
		t.Fatalf("successful real Bash resume failed: %v %#v", err, resumed)
	}
	rows = resumePublicRows(t, resumed)
	if len(rows) != 2 || rows[0].(map[string]any)["id"] != "verify" || rows[0].(map[string]any)["ok"] != true || rows[1].(map[string]any)["id"] != "inspect" {
		t.Fatalf("resume changed executed children: %#v", rows)
	}
	verify := rows[0].(map[string]any)["structured"].(map[string]any)
	if verify["exit_code"] != float64(0) || verify["stdout"] != "verified\n" {
		t.Fatalf("successful verifier output changed: %#v", verify)
	}
	info, err := os.Stat(written)
	if err != nil || !info.ModTime().Equal(stamp) {
		t.Fatalf("resume repeated the earlier write: %v %#v", err, info)
	}
	for _, job := range e.Jobs.Snapshot() {
		if job.Status == "running" || !job.Quiescent {
			t.Fatalf("verification left unresolved process ownership: %#v", job)
		}
	}
}
