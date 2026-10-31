package activity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestUnknownProjectPrecedesInvalidNumericOptions(t *testing.T) {
	h := testHandler(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/activity.json?project_id=missing&limit=0", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "unknown_project") {
		t.Fatalf("validation precedence changed: %d %s", rec.Code, rec.Body.String())
	}
}
func TestCanceledGitLeaderDoesNotPoisonSnapshotCache(t *testing.T) {
	var calls atomic.Int64
	started := make(chan struct{})
	h := testHandler(t, func(o *Options) {
		o.Git = func(ctx context.Context, p wire.Project, diff bool) (map[string]any, error) {
			if calls.Add(1) == 1 {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return map[string]any{"available": true}, nil
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	project := wire.Project{ID: "alpha", Root: t.TempDir()}
	go func() { _, err := h.cachedGit(ctx, project); result <- err }()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled Git snapshot did not exit")
	}
	value, err := h.cachedGit(context.Background(), project)
	if err != nil || value["available"] != true || calls.Load() != 2 {
		t.Fatalf("canceled snapshot poisoned cache: %#v %v calls=%d", value, err, calls.Load())
	}
}
func TestHandlerCloseCancelsActiveGitObservation(t *testing.T) {
	started := make(chan struct{})
	h := testHandler(t, func(o *Options) {
		o.Git = func(ctx context.Context, p wire.Project, diff bool) (map[string]any, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
	})
	result := make(chan error, 1)
	go func() {
		_, err := h.cachedGit(context.Background(), wire.Project{ID: "alpha", Root: t.TempDir()})
		result <- err
	}()
	<-started
	h.Close()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("closed dashboard left Git observation running")
	}
}
