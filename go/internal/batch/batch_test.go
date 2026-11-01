package batch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type testResolver struct{ workspace wire.Workspace }

func (r testResolver) Resolve(_ context.Context, _ wire.Scope, id string) (wire.Workspace, error) {
	if id == "" || id == r.workspace.ID {
		return r.workspace, nil
	}
	return wire.Workspace{}, wire.Errorf("workspace_not_found", "Unknown workspace_id: %s", id)
}
func (r testResolver) Projects() []wire.Project {
	return []wire.Project{{ID: r.workspace.ProjectID, Root: r.workspace.Root, Label: "fixture"}}
}
func (r testResolver) Project(id string) (wire.Project, bool) {
	if id == r.workspace.ProjectID {
		return r.Projects()[0], true
	}
	return wire.Project{}, false
}

type callerFunc func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error)

func (f callerFunc) Call(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	return f(ctx, scope, name, args)
}

func fixture(t *testing.T, caller callerFunc) (*Service, *config.Config, wire.Workspace) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.New(filepath.Join(base, "state"))
	cfg.Set("writeMode", "workspace")
	cfg.Set("maxOutputBytes", 120000)
	w := wire.Workspace{ID: "test-workspace", ProjectID: "test", Root: root, Mode: "workspace"}
	validator := func(name string, args map[string]any) (map[string]any, error) {
		out := clone(args)
		if out["invalid"] == true {
			return nil, wire.Errorf("args_invalid", "invalid child")
		}
		delete(out, "discard")
		return out, nil
	}
	s, err := New(cfg, testResolver{w}, caller, validator)
	if err != nil {
		t.Fatal(err)
	}
	return s, cfg, w
}

func op(id, name string, args map[string]any) map[string]any {
	out := map[string]any{"tool": name}
	if id != "" {
		out["id"] = id
	}
	if args != nil {
		out["args"] = args
	}
	return out
}
func invoke(t *testing.T, s *Service, args map[string]any) wire.Result {
	t.Helper()
	args["workspace_id"] = "test-workspace"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := s.Call(ctx, wire.Scope{PrincipalID: "principal", RequestID: "request", ParentActionID: "parent"}, "batch", args)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func errorCode(r wire.Result) string      { return wire.String(r.StructuredContent, "error_code", "") }
func rows(r wire.Result) []map[string]any { return r.StructuredContent["results"].([]map[string]any) }
func success(name string, data map[string]any, text string) wire.Result {
	return wire.Success(name, name, data, text)
}

func TestPreflightHasNoChildOrStoreEffects(t *testing.T) {
	var calls atomic.Int32
	s, _, w := fixture(t, func(_ context.Context, _ wire.Scope, name string, args map[string]any) (wire.Result, error) {
		calls.Add(1)
		return success(name, nil, "ok"), nil
	})
	r := invoke(t, s, map[string]any{"persist": true, "operations": []any{op("first", "write", map[string]any{"path": "first.txt", "content": "must not happen"}), op("last", "read", map[string]any{"path": "x", "invalid": true})}})
	if !r.IsError || errorCode(r) != "args_invalid" || calls.Load() != 0 {
		t.Fatalf("preflight crossed effects boundary: %#v calls=%d", r, calls.Load())
	}
	if _, err := os.Stat(filepath.Join(w.Root, StoreDir)); !os.IsNotExist(err) {
		t.Fatal("invalid batch created storage directory")
	}
	if _, err := os.Stat(filepath.Join(w.Root, "first.txt")); !os.IsNotExist(err) {
		t.Fatal("invalid batch applied earlier write")
	}
}

func TestMutationAndCredentialPreflight(t *testing.T) {
	s, _, w := fixture(t, func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		t.Error("invalid workflow executed")
		return wire.Result{}, nil
	})
	if err := os.Mkdir(filepath.Join(w.Root, "real"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(w.Root, "alias")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, code string
		args       map[string]any
	}{
		{"alias duplicate", "batch_mutation_conflict", map[string]any{"operations": []any{op("a", "write", map[string]any{"path": "real/new.txt"}), op("b", "edit", map[string]any{"path": "alias/new.txt"})}}},
		{"patch exclusive", "batch_mutation_conflict", map[string]any{"operations": []any{op("a", "apply_patch", map[string]any{"patch": "anything"}), op("b", "write", map[string]any{"path": "other"})}}},
		{"verify last", "batch_verification_order", map[string]any{"operations": []any{op("a", "bash", map[string]any{"command": "true"}), op("b", "write", map[string]any{"path": "other"})}}},
		{"parallel mutation", "batch_mode_serial_required", map[string]any{"mode": "parallel", "operations": []any{op("a", "write", map[string]any{"path": "other"})}}},
		{"continue mutation", "batch_args_invalid", map[string]any{"continue_on_error": true, "operations": []any{op("a", "bash", map[string]any{"command": "true"})}}},
		{"child workspace", "batch_args_invalid", map[string]any{"operations": []any{op("a", "read", map[string]any{"workspace_id": "other"})}}},
		{"child credentials", "batch_args_invalid", map[string]any{"operations": []any{op("a", "bash", map[string]any{"command": "true", "session_id": "secret"})}}},
		{"background verification", "args_invalid", map[string]any{"operations": []any{op("a", "bash", map[string]any{"command": "true", "background": true})}}},
		{"retired checkpoint", "batch_args_invalid", map[string]any{"checkpoint": map[string]any{}, "operations": []any{op("a", "read", nil)}}},
		{"duplicate id", "batch_duplicate_id", map[string]any{"operations": []any{op("a", "read", nil), op("a", "tree", nil)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := invoke(t, s, tc.args)
			if !result.IsError || errorCode(result) != tc.code {
				t.Fatalf("expected %s: %#v", tc.code, result)
			}
		})
	}
}

func TestSerialFailureContinueAndChangedPaths(t *testing.T) {
	var mu sync.Mutex
	called := []string{}
	s, _, _ := fixture(t, func(_ context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
		if scope.ParentActionID != "parent" || !strings.Contains(scope.RequestID, "/batch/") {
			t.Errorf("child scope lost attribution: %+v", scope)
		}
		path := wire.String(args, "path", "")
		mu.Lock()
		called = append(called, path)
		mu.Unlock()
		if path == "bad" {
			return wire.Result{}, errors.New("child failed")
		}
		return success(name, map[string]any{"path": path, "changed_paths": []any{path, map[string]any{"path": "extra"}}}, "done"), nil
	})
	r := invoke(t, s, map[string]any{"operations": []any{op("first", "write", map[string]any{"path": "ok"}), op("fail", "write", map[string]any{"path": "bad"}), op("last", "write", map[string]any{"path": "unreached"})}})
	if !r.IsError || r.StructuredContent["succeeded_count"] != 1 || r.StructuredContent["failed_count"] != 1 || r.StructuredContent["skipped_count"] != 1 || len(called) != 2 {
		t.Fatalf("serial stop contract: %#v called=%v", r, called)
	}
	paths := r.StructuredContent["changed_paths"].([]string)
	if strings.Join(paths, ",") != "ok,extra" {
		t.Fatalf("unsuccessful child paths leaked: %v", paths)
	}
	if rows(r)[2]["skipped"] != true || rows(r)[0]["text_in_content"] != true {
		t.Fatal("child public DTO missing markers")
	}
	called = nil
	r = invoke(t, s, map[string]any{"continue_on_error": true, "operations": []any{op("first", "read", map[string]any{"path": "bad"}), op("last", "read", map[string]any{"path": "after"})}})
	if !r.IsError || r.StructuredContent["skipped_count"] != 0 || len(called) != 2 {
		t.Fatal("read-only continue did not execute following child")
	}
}

func TestParallelExecutionIsConcurrentAndResultsRemainOrdered(t *testing.T) {
	ready := make(chan struct{}, 3)
	release := make(chan struct{})
	var count atomic.Int32
	s, _, _ := fixture(t, func(ctx context.Context, _ wire.Scope, name string, args map[string]any) (wire.Result, error) {
		ready <- struct{}{}
		select {
		case <-ctx.Done():
			return wire.Result{}, ctx.Err()
		case <-release:
		}
		count.Add(1)
		return success(name, map[string]any{"path": args["path"]}, "done"), nil
	})
	go func() {
		for i := 0; i < 3; i++ {
			<-ready
		}
		close(release)
	}()
	r := invoke(t, s, map[string]any{"mode": "parallel", "operations": []any{op("a", "read", map[string]any{"path": "a"}), op("b", "tree", map[string]any{"path": "b"}), op("c", "search", map[string]any{"path": "c"})}})
	if r.IsError || count.Load() != 3 {
		t.Fatalf("parallel fan-out failed: %#v", r)
	}
	for i, id := range []string{"a", "b", "c"} {
		if rows(r)[i]["id"] != id {
			t.Fatal("completion order changed result order")
		}
	}
}

func TestBashVerificationRequiresQuiescenceAndKillPolicy(t *testing.T) {
	var quiescent atomic.Bool
	s, cfg, _ := fixture(t, func(_ context.Context, _ wire.Scope, name string, args map[string]any) (wire.Result, error) {
		if args["on_timeout"] != "kill" {
			t.Error("verification can promote")
		}
		return success(name, map[string]any{"exit_code": 0, "job_status": "succeeded", "job_id": "job_owned", "timed_out": false}, "verified"), nil
	})
	cfg.Set("batchSettlementWaitMs", 500)
	s.SetJobLookup(func(string, string) (bool, bool) { return true, quiescent.Load() })
	go func() { time.Sleep(60 * time.Millisecond); quiescent.Store(true) }()
	started := time.Now()
	r := invoke(t, s, map[string]any{"persist": false, "operations": []any{op("verify", "bash", map[string]any{"command": "true", "on_timeout": "background"})}})
	if r.IsError || time.Since(started) < 50*time.Millisecond {
		t.Fatal("successful verification preceded exact ownership settlement")
	}
	quiescent.Store(false)
	cfg.Set("batchSettlementWaitMs", 30)
	r = invoke(t, s, map[string]any{"persist": false, "operations": []any{op("verify", "bash", map[string]any{"command": "true"})}})
	if !r.IsError || rows(r)[0]["error"] != "Bash verification has not finished; inspect its job before checkpointing." {
		t.Fatalf("unsettled verification succeeded: %#v", r)
	}
	if r.DashboardMetadata == nil {
		t.Fatal("executed Bash scripts missing from private audit metadata")
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "shell_scripts") {
		t.Fatal("private shell scripts leaked in public DTO")
	}
}

func TestBashFailurePrecedenceAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		data       map[string]any
	}{
		{"timeout", "Bash command timed out.", map[string]any{"timed_out": true, "signal": "SIGTERM", "exit_code": 143}},
		{"signal", "Bash command terminated by signal SIGTERM.", map[string]any{"signal": "SIGTERM", "exit_code": 143}},
		{"exit", "Bash command exited with code 7.", map[string]any{"exit_code": 7}},
		{"unfinished", "Bash verification has not finished; inspect its job before checkpointing.", map[string]any{"exit_code": 0, "job_status": "running"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := fixture(t, func(_ context.Context, _ wire.Scope, name string, _ map[string]any) (wire.Result, error) {
				return success(name, tc.data, ""), nil
			})
			r := invoke(t, s, map[string]any{"persist": false, "operations": []any{op("verify", "bash", map[string]any{"command": "true"})}})
			if !r.IsError || rows(r)[0]["error"] != tc.want {
				t.Fatalf("wrong failure: %#v", r)
			}
		})
	}
	s, _, _ := fixture(t, func(_ context.Context, _ wire.Scope, name string, _ map[string]any) (wire.Result, error) {
		return success(name, map[string]any{"exit_code": 0, "job_id": "known"}, ""), nil
	})
	s.SetJobLookup(func(string, string) (bool, bool) { return true, false })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r, err := s.Call(ctx, wire.Scope{}, "batch", map[string]any{"workspace_id": "test-workspace", "persist": false, "operations": []any{op("verify", "bash", map[string]any{"command": "true"})}})
	if err != nil || !r.IsError {
		t.Fatal("canceled ownership wait reported success")
	}
}

func TestPersistenceSanitizesCredentialsAndResumeValidatesAllChildren(t *testing.T) {
	var calls atomic.Int32
	s, cfg, w := fixture(t, func(_ context.Context, _ wire.Scope, name string, args map[string]any) (wire.Result, error) {
		calls.Add(1)
		if name == "bash" && args["session_id"] != "fixture-session" {
			t.Error("outer session was not inherited")
		}
		return success(name, map[string]any{"exit_code": 0}, "ok"), nil
	})
	cfg.Set("bashSessionId", "fixture-session")
	cfg.Set("requireBashSession", true)
	r := invoke(t, s, map[string]any{"session_id": "fixture-session", "operations": []any{op("inspect", "read", map[string]any{"path": "a", "discard": "not saved"}), op("verify", "bash", map[string]any{"command": "printf 'a&b'", "on_timeout": "background"})}})
	if r.IsError || r.StructuredContent["persisted"] != true || r.StructuredContent["persistence_default"] != true {
		t.Fatalf("verification default persistence missing: %#v", r)
	}
	path := r.StructuredContent["batch_path"].(string)
	body, err := os.ReadFile(filepath.Join(w.Root, path))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"session_id", "workspace_id", "fixture-session", "discard", "\\u0026"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("saved definition contains %s", forbidden)
		}
	}
	var definition Definition
	if err = json.Unmarshal(body, &definition); err != nil {
		t.Fatal(err)
	}
	if definition.Operations[1].Args["on_timeout"] != "kill" {
		t.Fatal("explicit promotion policy persisted")
	}
	info, err := os.Stat(filepath.Join(w.Root, path))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("batch definition is not private")
	}
	calls.Store(0)
	r = invoke(t, s, map[string]any{"path": path, "from": "verify", "session_id": "fixture-session"})
	if r.IsError || calls.Load() != 1 || r.StructuredContent["start_index"] != 1 || r.StructuredContent["total_operation_count"] != 2 {
		t.Fatalf("resume lost original indices: %#v", r)
	}
	definition.Operations[0].Args["invalid"] = true
	body, _ = json.Marshal(definition)
	if err = os.WriteFile(filepath.Join(w.Root, path), body, 0600); err != nil {
		t.Fatal(err)
	}
	calls.Store(0)
	before, err := os.Stat(filepath.Join(w.Root, path))
	if err != nil {
		t.Fatal(err)
	}
	r = invoke(t, s, map[string]any{"path": path, "from": "verify", "session_id": "fixture-session"})
	after, err := os.Stat(filepath.Join(w.Root, path))
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError || calls.Load() != 0 || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("resume bypassed skipped-child validation or touched invalid stored file")
	}
}

func TestStoredDefinitionsStrictAndResumeArgumentRules(t *testing.T) {
	s, _, w := fixture(t, func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		t.Error("invalid definition executed")
		return wire.Result{}, nil
	})
	for _, tc := range []struct{ name, body, code string }{
		{"strict.json", `{"version":1,"mode":"serial","continue_on_error":false,"operations":[{"tool":"read"}],"extra":true}`, "batch_file_invalid"},
		{"empty.json", `{"version":1,"mode":"serial","continue_on_error":false,"operations":[]}`, "batch_file_invalid"},
		{"broken.json", `{"version":`, ""},
	} {
		if err := os.WriteFile(filepath.Join(w.Root, tc.name), []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		r := invoke(t, s, map[string]any{"path": tc.name})
		if !r.IsError || errorCode(r) != tc.code {
			t.Fatalf("strict definition error mismatch: %#v", r)
		}
	}
	valid := `{"version":1,"mode":"serial","continue_on_error":false,"operations":[{"id":"a","tool":"read"}]}`
	if err := os.WriteFile(filepath.Join(w.Root, "ok.json"), []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{{"path": "ok.json", "from": "absent"}, {"path": "ok.json", "from_index": 1}} {
		r := invoke(t, s, args)
		if !r.IsError || errorCode(r) != "batch_resume_invalid" {
			t.Fatalf("invalid resume accepted: %#v", r)
		}
	}
	for _, args := range []map[string]any{{"path": "ok.json", "persist": false}, {"path": "ok.json", "mode": "serial"}, {"path": "ok.json", "continue_on_error": false}, {"path": "ok.json", "from": "a", "from_index": 0}} {
		r := invoke(t, s, args)
		if !r.IsError || errorCode(r) != "batch_args_invalid" {
			t.Fatalf("invalid stored override accepted: %#v", r)
		}
	}
}

func TestGitExcludeAndRetentionPreserveCustomAndResumedFile(t *testing.T) {
	s, _, w := fixture(t, func(_ context.Context, _ wire.Scope, name string, _ map[string]any) (wire.Result, error) {
		return success(name, nil, "ok"), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "git", "-C", w.Root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git fixture: %v %s", err, output)
	}
	dir := filepath.Join(w.Root, StoreDir)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"version":1,"mode":"serial","continue_on_error":false,"operations":[{"tool":"read"}]}`)
	old := time.Now().Add(-time.Hour)
	for i := 0; i < 24; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%04X.json", i))
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		when := old.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	custom := filepath.Join(dir, "keep-custom.json")
	if err := os.WriteFile(custom, body, 0600); err != nil {
		t.Fatal(err)
	}
	r := invoke(t, s, map[string]any{"path": StoreDir + "/0000.json"})
	if r.IsError || r.StructuredContent["pruned_batch_count"] != 4 || r.StructuredContent["git_excluded"] != true {
		t.Fatalf("stored-file maintenance contract: %#v", r)
	}
	items, err := s.batchFiles(w)
	if err != nil || len(items) != 20 {
		t.Fatalf("retention count=%d err=%v", len(items), err)
	}
	for _, path := range []string{custom, filepath.Join(dir, "0000.json")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("custom or resumed definition removed", err)
		}
	}
	exclude, err := os.ReadFile(filepath.Join(w.Root, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(exclude), "/"+StoreDir+"/") != 1 {
		t.Fatal("Git exclude missing or duplicated")
	}
	r = invoke(t, s, map[string]any{"path": StoreDir + "/0000.json"})
	if r.IsError {
		t.Fatal("second maintenance failed")
	}
	exclude, err = os.ReadFile(filepath.Join(w.Root, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(exclude), "/"+StoreDir+"/") != 1 {
		t.Fatal("Git exclude appended duplicate")
	}
}

func TestPruneRechecksEditedFileUnderSharedLock(t *testing.T) {
	s, _, w := fixture(t, func(_ context.Context, _ wire.Scope, name string, _ map[string]any) (wire.Result, error) {
		return success(name, nil, ""), nil
	})
	dir := filepath.Join(w.Root, StoreDir)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	for i := 0; i < 21; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%04X.json", i))
		if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		when := old.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	oldest := filepath.Join(dir, "0000.json")
	release := s.fileService().LockPaths(oldest)
	done := make(chan error, 1)
	go func() { _, err := s.prune(w, ""); done <- err }()
	// A tagged edit holds the same canonical path lock; retention must re-read
	// timestamps after waiting. This test gives the first census a bounded turn.
	time.Sleep(40 * time.Millisecond)
	now := time.Now()
	if err := os.Chtimes(oldest, now, now); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retention deadlocked with edit")
	}
	if _, err := os.Stat(oldest); err != nil {
		t.Fatal("retention deleted freshly edited definition")
	}
}

func TestOutputBudgetsAndStructuredPriority(t *testing.T) {
	s, cfg, _ := fixture(t, func(_ context.Context, _ wire.Scope, name string, _ map[string]any) (wire.Result, error) {
		return success(name, map[string]any{"workspace_id": "test-workspace", "path": "kept", "matches": []any{strings.Repeat("λ", 10000)}, "irrelevant": strings.Repeat("discard", 10000)}, strings.Repeat("🐕", 30000)), nil
	})
	cfg.Set("maxOutputBytes", 1200)
	r := invoke(t, s, map[string]any{"operations": []any{op("a", "read", nil), op("b", "read", nil), op("c", "read", nil)}})
	text := r.Content[0]["text"].(string)
	if len(text) > 1200 || !utf8.ValidString(text) || r.StructuredContent["child_text_truncated_count"] != 3 || r.StructuredContent["child_structured_truncated_count"] != 3 {
		t.Fatal("batch output exceeded bounded UTF-8 contract")
	}
	for _, row := range rows(r) {
		data, err := jsonBytes(row["structured"])
		if err != nil || len(data) > 256 {
			t.Fatalf("oversized child structured output: %d %v", len(data), err)
		}
		if _, exists := row["text"]; exists {
			t.Fatal("child text was duplicated into structured result")
		}
	}
	for _, input := range []string{strings.Repeat("λ", 30), "\xffbad"} {
		for budget := 0; budget < 15; budget++ {
			bounded, _ := truncate(input, budget, "…")
			if len(bounded) > budget || !utf8.ValidString(bounded) {
				t.Fatal("invalid bounded UTF-8")
			}
		}
	}
}

func TestReadOnlyChildrenHaveADeadlineAndMutationsDoNot(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	s, cfg, _ := fixture(t, func(ctx context.Context, _ wire.Scope, name string, args map[string]any) (wire.Result, error) {
		if args["path"] == "slow" {
			<-block // ignores its context, like a stuck filesystem call
		}
		if args["path"] == "slow-write" {
			time.Sleep(400 * time.Millisecond)
		}
		return success(name, map[string]any{"path": args["path"]}, "done"), nil
	})
	cfg.Set("batchReadDeadlineMs", 200)
	started := time.Now()
	r := invoke(t, s, map[string]any{"mode": "parallel", "continue_on_error": true, "operations": []any{op("a", "read", map[string]any{"path": "fast"}), op("b", "search", map[string]any{"path": "slow"}), op("c", "tree", map[string]any{"path": "fast2"})}})
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("read-only batch was not bounded: %v", elapsed)
	}
	got := rows(r)
	if got[0]["ok"] != true || got[2]["ok"] != true {
		t.Fatalf("fast siblings lost: %#v", got)
	}
	if got[1]["ok"] != false || !strings.Contains(fmt.Sprint(got[1]["error"]), "Timed out") {
		t.Fatalf("slow child not reported as timed out: %#v", got[1])
	}
	// A mutating child is not cut short by the read-only limit.
	r = invoke(t, s, map[string]any{"operations": []any{op("w", "write", map[string]any{"path": "slow-write", "content": "x"}), op("r", "read", map[string]any{"path": "fast"})}})
	got = rows(r)
	if got[0]["ok"] != true || got[1]["ok"] != true {
		t.Fatalf("mutating child or following read was cut short: %#v", got)
	}
}
