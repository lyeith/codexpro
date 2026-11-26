package batch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type retainingBatchCaller struct {
	retain func(context.Context) (context.Context, func(), error)
	child  func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error)
}

func (c retainingBatchCaller) Call(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
	return wire.Result{}, errors.New("wrapped caller must not run inside batch")
}

func (c retainingBatchCaller) CallBatchChild(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	return c.child(ctx, scope, name, args)
}

func (c retainingBatchCaller) RetainBatchChild(ctx context.Context) (context.Context, func(), error) {
	return c.retain(ctx)
}

type retainedBatchContextKey struct{}

func waitBatchRetention(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for " + label)
	}
}

func TestBatchRetainsSynchronouslyAndReleasesAfterCancelledParent(t *testing.T) {
	s, _, w := fixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deadline := time.Now().Add(time.Minute)
	retainEntered, allowRetain := make(chan struct{}), make(chan struct{})
	childEntered, allowChild := make(chan struct{}), make(chan struct{})
	released, parentDone := make(chan struct{}), make(chan struct{})
	var retainOnce, childOnce sync.Once
	unblockRetain := func() { retainOnce.Do(func() { close(allowRetain) }) }
	unblockChild := func() { childOnce.Do(func() { close(allowChild) }) }
	t.Cleanup(func() {
		unblockRetain()
		unblockChild()
		waitBatchRetention(t, parentDone, "parent cleanup")
		waitBatchRetention(t, released, "child cleanup")
	})
	var retained, releases, calls atomic.Int32
	type observation struct {
		value    any
		deadline time.Time
		err      error
	}
	observed := make(chan observation, 1)
	s.caller = retainingBatchCaller{
		retain: func(childCtx context.Context) (context.Context, func(), error) {
			close(retainEntered)
			<-allowRetain
			retained.Add(1)
			return context.WithValue(childCtx, retainedBatchContextKey{}, "reserved"), func() {
				releases.Add(1)
				close(released)
			}, nil
		},
		child: func(childCtx context.Context, _ wire.Scope, name string, _ map[string]any) (wire.Result, error) {
			calls.Add(1)
			at, _ := childCtx.Deadline()
			observed <- observation{childCtx.Value(retainedBatchContextKey{}), at, childCtx.Err()}
			close(childEntered)
			<-allowChild
			return success(name, nil, "late original result"), nil
		},
	}
	result := make(chan childResult, 1)
	go func() {
		defer close(parentDone)
		result <- s.runBounded(ctx, deadline, time.Minute, wire.Scope{}, w, operation{
			ID: "read", Tool: "read", Validated: map[string]any{"path": "a"},
		}, 128, 1024)
	}()
	waitBatchRetention(t, retainEntered, "synchronous retain")
	cancel()
	select {
	case <-parentDone:
		t.Fatal("parent escaped its synchronous retain before the child had an admission")
	case <-time.After(30 * time.Millisecond):
	}
	unblockRetain()
	waitBatchRetention(t, parentDone, "cancelled parent")
	if got := <-result; got.OK || got.Error != "Batch call was cancelled." {
		t.Fatalf("existing cancellation result changed: %+v", got)
	}
	waitBatchRetention(t, childEntered, "retained child")
	got := <-observed
	if got.value != "reserved" || !got.deadline.Equal(deadline) || !errors.Is(got.err, context.Canceled) {
		t.Fatalf("retaining changed child context/deadline: %+v", got)
	}
	if retained.Load() != 1 || calls.Load() != 1 || releases.Load() != 0 {
		t.Fatalf("parent return released a live child: retained=%d calls=%d releases=%d", retained.Load(), calls.Load(), releases.Load())
	}
	unblockChild()
	waitBatchRetention(t, released, "actual child completion")
	if releases.Load() != 1 {
		t.Fatalf("child released %d times", releases.Load())
	}
}

func TestBatchRetentionRefusalAndPanicRelease(t *testing.T) {
	for _, scenario := range []string{"refused", "panic", "success"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, w := fixture(t, nil)
			var retains, calls, releases atomic.Int32
			released := make(chan struct{})
			s.caller = retainingBatchCaller{
				retain: func(ctx context.Context) (context.Context, func(), error) {
					retains.Add(1)
					if scenario == "refused" {
						return ctx, nil, wire.Errorf("execution_held", "Execution is held.")
					}
					return ctx, func() {
						releases.Add(1)
						close(released)
					}, nil
				},
				child: func(_ context.Context, _ wire.Scope, name string, _ map[string]any) (wire.Result, error) {
					calls.Add(1)
					if scenario == "panic" {
						panic("fixture child panic")
					}
					return success(name, nil, "original"), nil
				},
			}
			got := s.runBounded(context.Background(), time.Now().Add(time.Second), time.Second, wire.Scope{}, w, operation{
				ID: "read", Tool: "read", Validated: map[string]any{"path": "a"},
			}, 128, 1024)
			if retains.Load() != 1 {
				t.Fatalf("child acquired %d reservations", retains.Load())
			}
			if scenario == "refused" {
				if got.OK || got.Error != "Execution is held." || calls.Load() != 0 || releases.Load() != 0 {
					t.Fatalf("refused retain crossed execution boundary: result=%+v calls=%d releases=%d", got, calls.Load(), releases.Load())
				}
				return
			}
			waitBatchRetention(t, released, "child release")
			if calls.Load() != 1 || releases.Load() != 1 {
				t.Fatalf("child lifetime mismatch: calls=%d releases=%d", calls.Load(), releases.Load())
			}
			if scenario == "panic" {
				if got.OK || !strings.Contains(got.Error, "fixture child panic") {
					t.Fatalf("child panic outcome changed: %+v", got)
				}
			} else if !got.OK {
				t.Fatalf("successful retained child failed: %+v", got)
			}
		})
	}
}
