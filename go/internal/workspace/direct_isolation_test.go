package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// This is a native integration proof, not an HTTP benchmark. A real managed
// lease is created and persisted, then the same state is reopened in supported
// direct mode. Only the historical lease's probe is replaced by a controlled
// per-probe timeout; unrelated open/read handlers and the healing Git inspection
// are the production implementations.
func TestHistoricalLeaseProbeDoesNotBlockUnrelatedDirectProject(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	oldRepo := filepath.Join(root, "historical-project")
	directRepo := filepath.Join(root, "unrelated-direct-project")
	const directText = "unrelated direct fixture\n"
	isolationGitFixture(t, ctx, oldRepo, "historical managed fixture\n")
	isolationGitFixture(t, ctx, directRepo, directText)

	newConfig := func(mode string) *config.Config {
		cfg := config.New(filepath.Join(root, "state"))
		cfg.Set("worktreeMode", mode)
		cfg.Set("codexDir", filepath.Join(root, "empty-codex"))
		return cfg
	}
	seedConfig := newConfig("mcp")
	if err := os.MkdirAll(seedConfig.Home, 0700); err != nil {
		t.Fatal(err)
	}
	catalog := Catalog{Version: 1, DefaultProject: "healthy", Projects: []wire.Project{
		{ID: "historical", Label: "Historical fixture", Root: oldRepo, BaseRef: "HEAD"},
		{ID: "healthy", Label: "Unrelated direct fixture", Root: directRepo, BaseRef: "HEAD"},
	}}
	catalogJSON, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(seedConfig.ProjectsFile, catalogJSON, 0600); err != nil {
		t.Fatal(err)
	}
	seed, err := New(seedConfig)
	if err != nil {
		t.Fatal(err)
	}
	seedClosed := false
	t.Cleanup(func() {
		if !seedClosed {
			_ = seed.Close()
		}
	})
	owner := wire.Scope{PrincipalID: "historical-owner", SessionID: "seed-session", RequestID: "seed", TransportID: "native"}
	handle, err := seed.CreateWorkspace(ctx, owner, map[string]any{"project_id": "historical", "idempotency_key": "persisted-isolation-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	baseline, baselineBody := isolationStoredLease(t, seed, handle.Workspace.ID)
	if handle.Workspace.Mode != "worktree" || baseline.State != "ready" || baseline.OwnerID != owner.PrincipalID {
		t.Fatalf("fixture did not create an owned, ready managed lease: %+v %+v", handle.Workspace, baseline)
	}
	if err = seed.Close(); err != nil {
		t.Fatal(err)
	}
	seedClosed = true

	directConfig := newConfig("direct")
	m, err := New(directConfig)
	if err != nil {
		t.Fatal(err)
	}
	directClosed := false
	t.Cleanup(func() {
		if !directClosed {
			_ = m.Close()
		}
	})
	f, err := files.New(directConfig, m)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(m, f)
	if m.Mode() != "direct" {
		t.Fatal("the restarted manager is not serving direct workspaces")
	}
	isolationAssertStoredLease(t, m, baseline, baselineBody)
	directID := IDForRoot(directRepo)
	reader := wire.Scope{PrincipalID: "unrelated-reader", SessionID: "direct-session", RequestID: "read-open", TransportID: "native"}
	directWorkspace, err := m.Resolve(ctx, reader, directID)
	if err != nil || directWorkspace.Mode != "direct" || directWorkspace.ProjectID != "healthy" || directWorkspace.Root != directRepo || directWorkspace.ID == baseline.WorkspaceID {
		t.Fatalf("fixture is not an unrelated direct workspace: %+v %v", directWorkspace, err)
	}

	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var calls sync.WaitGroup
	var probeErr error
	go func() {
		defer close(finished)
		probeErr = m.Reconcile(ctx, func(probeCtx context.Context, path string) (Repository, error) {
			if path != baseline.WorkspaceRoot {
				return Repository{}, fmt.Errorf("unexpected probe target %q", path)
			}
			close(entered)
			select {
			case <-release:
				// The reconciliation context remains live: this represents one
				// Git observation timing out, not cancellation of the whole run.
				return Repository{}, context.DeadlineExceeded
			case <-probeCtx.Done():
				return Repository{}, probeCtx.Err()
			}
		})
	}()
	defer func() {
		unblock()
		cancel()
		joined := make(chan struct{})
		go func() { calls.Wait(); <-finished; close(joined) }()
		select {
		case <-joined:
		case <-time.After(2 * time.Second):
			t.Error("bounded isolation proof cleanup did not join its calls/probe")
		}
	}()
	select {
	case <-entered:
	case <-finished:
		t.Fatalf("historical lease probe did not enter: %v", probeErr)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	type callResult struct {
		tool    string
		result  wire.Result
		err     error
		elapsed time.Duration
	}
	var openMS, readMS []float64
	expectedSHA := sha256.Sum256([]byte(directText))
	round := func(manager *Manager, workspaceService *Service, fileService *files.Service, scope wire.Scope) {
		t.Helper()
		callCtx, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		results := make(chan callResult, 2)
		calls.Add(2)
		go func() {
			defer calls.Done()
			started := time.Now()
			result, callErr := workspaceService.Call(callCtx, scope, "open_workspace", map[string]any{
				"project_id": "healthy", "include_tree": false, "include_skills": false, "include_global_skills": false,
			})
			results <- callResult{"open_workspace", result, callErr, time.Since(started)}
		}()
		go func() {
			defer calls.Done()
			started := time.Now()
			result, callErr := fileService.Call(callCtx, scope, "read", map[string]any{
				"workspace_id": directID, "path": "README.md", "start_line": 1, "end_line": 1,
			})
			results <- callResult{"read", result, callErr, time.Since(started)}
		}()
		for range 2 {
			var got callResult
			select {
			case got = <-results:
			case <-callCtx.Done():
				t.Fatal("unrelated direct open/read waited for the historical probe: ", callCtx.Err())
			}
			data := got.result.StructuredContent
			if got.err != nil || got.result.IsError || got.elapsed >= time.Second || data["workspace_id"] != directID || data["root"] != directRepo {
				t.Fatalf("%s lost direct identity/result or exceeded 1s: elapsed=%s result=%+v err=%v", got.tool, got.elapsed, got.result, got.err)
			}
			if got.tool == "read" {
				if data["sha256"] != hex.EncodeToString(expectedSHA[:]) || !strings.Contains(wire.String(data, "text", ""), strings.TrimSuffix(directText, "\n")) {
					t.Fatalf("direct read returned wrong file content: %+v", data)
				}
				readMS = append(readMS, float64(got.elapsed)/float64(time.Millisecond))
			} else {
				openMS = append(openMS, float64(got.elapsed)/float64(time.Millisecond))
			}
		}
		resolved, resolveErr := manager.Resolve(ctx, scope, directID)
		if resolveErr != nil || resolved.Mode != "direct" || resolved.ProjectID != "healthy" || resolved.Root != directRepo {
			t.Fatalf("open/read changed the direct resolver identity: %+v %v", resolved, resolveErr)
		}
	}
	blockedAt := time.Now()
	for i := 0; i < 8; i++ {
		reader.SessionID = fmt.Sprintf("unrelated-session-%d", i%2)
		round(m, service, f, reader)
		select {
		case <-finished:
			t.Fatal("probe finished before all direct traffic completed")
		default:
		}
		isolationAssertStoredLease(t, m, baseline, baselineBody)
	}
	blockedFor := time.Since(blockedAt)
	unblock()
	select {
	case <-finished:
		if probeErr != nil {
			t.Fatal(probeErr)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	unavailable, unavailableBody := isolationStoredLease(t, m, baseline.WorkspaceID)
	isolationAssertObservation(t, baseline, unavailable, baseline.Revision+1, "unavailable", context.DeadlineExceeded.Error())
	round(m, service, f, reader)
	isolationAssertStoredLease(t, m, unavailable, unavailableBody)

	// Use real Git after removing the injected fault; health must heal without
	// rewriting ownership or manually editing persisted lease JSON.
	if err = m.Reconcile(ctx, nil); err != nil {
		t.Fatal(err)
	}
	healed, healedBody := isolationStoredLease(t, m, baseline.WorkspaceID)
	isolationAssertObservation(t, baseline, healed, baseline.Revision+2, "healthy", "")
	round(m, service, f, reader)
	isolationAssertStoredLease(t, m, healed, healedBody)
	calls.Wait()
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	directClosed = true
	restarted, err := New(newConfig("direct"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	restartedFiles, err := files.New(restarted.cfg, restarted)
	if err != nil {
		t.Fatal(err)
	}
	isolationAssertStoredLease(t, restarted, healed, healedBody)
	round(restarted, NewService(restarted, restartedFiles), restartedFiles, reader)
	isolationAssertStoredLease(t, restarted, healed, healedBody)
	t.Logf("native direct isolation: blocked_rounds=8 concurrent_calls_while_blocked=16 blocked_ms=%.3f read_ms=%v open_ms=%v historical_lease_unchanged_while_blocked=true owner_unchanged=true orphaned_observations=0 revision_before=%d revision_unavailable=%d revision_healed=%d real_git_heal=true persisted_after_reopen=true",
		float64(blockedFor)/float64(time.Millisecond), readMS, openMS, baseline.Revision, unavailable.Revision, healed.Revision)
}

func isolationGitFixture(t *testing.T, ctx context.Context, root, content string) {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	environment := make([]string, 0, len(os.Environ())+8)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "GIT_") {
			environment = append(environment, item)
		}
	}
	environment = append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Isolation Fixture", "GIT_AUTHOR_EMAIL=isolation@localhost", "GIT_COMMITTER_NAME=Isolation Fixture", "GIT_COMMITTER_EMAIL=isolation@localhost")
	run := func(arguments ...string) {
		t.Helper()
		arguments = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}, arguments...)
		command := exec.CommandContext(ctx, "git", arguments...)
		command.Dir, command.Env = root, environment
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("private fixture Git failed: %v: %s", err, output)
		}
	}
	run("init", "--quiet", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "--quiet", "-m", "test: isolated native fixture")
}

func isolationStoredLease(t *testing.T, manager *Manager, id string) (Lease, []byte) {
	t.Helper()
	var revision int
	var body []byte
	if err := manager.db.QueryRow("SELECT revision,body FROM leases WHERE id=?", id).Scan(&revision, &body); err != nil {
		t.Fatal(err)
	}
	var lease Lease
	if err := json.Unmarshal(body, &lease); err != nil {
		t.Fatal(err)
	}
	if lease.Revision != revision {
		t.Fatal("persisted lease revision column/body disagree")
	}
	snapshot, found := manager.leaseSnapshot(id)
	if !found || !reflect.DeepEqual(snapshot, lease) {
		t.Fatalf("memory and persisted lease disagree: %+v %+v", snapshot, lease)
	}
	return lease, body
}

func isolationAssertStoredLease(t *testing.T, manager *Manager, expected Lease, expectedBody []byte) {
	t.Helper()
	actual, body := isolationStoredLease(t, manager, expected.WorkspaceID)
	if !reflect.DeepEqual(actual, expected) || !bytes.Equal(body, expectedBody) {
		t.Fatalf("unrelated direct operations changed historical lease/revision: got=%+v expected=%+v", actual, expected)
	}
}

func isolationAssertObservation(t *testing.T, baseline, actual Lease, revision int, health, failure string) {
	t.Helper()
	if actual.Revision != revision || actual.State != "ready" || actual.Health != health || actual.Failure != failure || actual.ObservedAt == "" || actual.UpdatedAt != actual.ObservedAt {
		t.Fatalf("unexpected health-only transition: %+v", actual)
	}
	// Health observations may advance their own revision/time. Every other
	// persisted field, including owner, identity, last-use and release state,
	// must still equal the original lease loaded after the mode transition.
	expected := baseline
	expected.Revision, expected.Health, expected.Failure = revision, health, failure
	expected.ObservedAt, expected.UpdatedAt = actual.ObservedAt, actual.UpdatedAt
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("health observation changed historical ownership/lifecycle: got=%+v expected=%+v", actual, expected)
	}
}
