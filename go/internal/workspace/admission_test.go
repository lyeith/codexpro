package workspace

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestWorkspaceRemovalChecksPublishedJobOwnershipInsideLeaseLock(t *testing.T) {
	m, _, scope := fixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h, err := m.CreateWorkspace(ctx, scope, map[string]any{"project_id": "test"})
	if err != nil {
		t.Fatal(err)
	}
	var active atomic.Bool
	var checks atomic.Int32
	m.HasActiveJobs = func(id string) bool {
		if id != h.Workspace.ID {
			t.Errorf("snapshot requested for wrong workspace: %s", id)
		}
		checks.Add(1)
		return active.Load()
	}
	// This is the formerly racy engine precheck: no job has been admitted yet.
	if m.HasActiveJobs(h.Workspace.ID) {
		t.Fatal("fixture begins with an active job")
	}
	checks.Store(0)
	entered, publish := make(chan struct{}), make(chan struct{})
	var publishOnce sync.Once
	unblock := func() { publishOnce.Do(func() { close(publish) }) }
	defer unblock()
	admission := make(chan error, 1)
	go func() {
		_, err := m.Execute(ctx, scope, h.Workspace.ID, true, func() (wire.Result, error) {
			close(entered)
			select {
			case <-publish:
			case <-ctx.Done():
				return wire.Result{}, ctx.Err()
			}
			// Equivalent to jobs admission publishing its durable immutable record.
			active.Store(true)
			return wire.Result{}, nil
		})
		admission <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err = m.RemoveWorkspace(ctx, scope, h.Workspace.ID); err == nil || err.Error() != "Workspace still has active operations." {
		t.Fatalf("active admission was not protected: %v", err)
	}
	if checks.Load() != 0 {
		t.Fatal("busy lease consulted job snapshot outside the ownership lock")
	}
	unblock()
	select {
	case err = <-admission:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err = m.RemoveWorkspace(ctx, scope, h.Workspace.ID); err == nil || err.Error() != "Workspace still has active operations." {
		t.Fatalf("admitted job outlived its lease protection: %v", err)
	}
	if checks.Load() != 1 {
		t.Fatalf("authoritative job check was not executed exactly once: %d", checks.Load())
	}
	if _, err = os.Stat(h.Workspace.Root); err != nil {
		t.Fatalf("active job's checkout was removed: %v", err)
	}
	active.Store(false)
	if err = m.RemoveWorkspace(ctx, scope, h.Workspace.ID); err != nil {
		t.Fatalf("settled job prevented clean removal: %v", err)
	}
}

func TestWorkspaceRemovalExcludesWaitingJobAdmission(t *testing.T) {
	m, _, scope := fixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h, err := m.CreateWorkspace(ctx, scope, map[string]any{"project_id": "test"})
	if err != nil {
		t.Fatal(err)
	}
	checking, allowRemoval := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(allowRemoval) }) }
	defer release()
	// The real provider is nonblocking. This test barrier pauses precisely at
	// the snapshot read, allowing a competing admission to queue at the lock.
	m.HasActiveJobs = func(id string) bool {
		close(checking)
		select {
		case <-allowRemoval:
		case <-ctx.Done():
		}
		return false
	}
	removed := make(chan error, 1)
	go func() { removed <- m.RemoveWorkspace(ctx, scope, h.Workspace.ID) }()
	select {
	case <-checking:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var called atomic.Bool
	admitted := make(chan error, 1)
	go func() {
		_, err := m.Execute(ctx, scope, h.Workspace.ID, true, func() (wire.Result, error) {
			called.Store(true)
			return wire.Result{}, nil
		})
		admitted <- err
	}()
	// A reference covers a holder or waiter. Two references establish that the
	// competing admission reached the ownership boundary before removal resumes.
	deadline := time.Now().Add(time.Second)
	for {
		m.leaseLocks.mu.Lock()
		entry := m.leaseLocks.entries[h.Workspace.ID]
		queued := entry != nil && entry.refs == 2
		m.leaseLocks.mu.Unlock()
		if queued {
			break
		}
		if called.Load() || time.Now().After(deadline) {
			t.Fatal("job admission did not serialize behind removal's snapshot check")
		}
		time.Sleep(time.Millisecond)
	}
	release()
	select {
	case err = <-removed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err = <-admitted:
		if err == nil || err.Error() != "Unknown workspace_id." {
			t.Fatalf("queued admission did not observe removal: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if called.Load() {
		t.Fatal("job admission executed after its workspace was removed")
	}
}
