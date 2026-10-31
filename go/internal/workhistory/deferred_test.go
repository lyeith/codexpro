package workhistory

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func deferredWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bounded archive operation did not resolve")
	}
}

func deferredUnavailable(t *testing.T, err error) {
	t.Helper()
	var typed *wire.Error
	if !errors.As(err, &typed) || typed.Code != "work_history_unavailable" {
		t.Errorf("expected explicit archive unavailability, got %v", err)
	}
}

// A waiting/blocked archive must not transfer its deadline to callers. Exercise
// all HTTP-facing callbacks concurrently, not just an initializer state flag.
func TestDeferredArchiveReadersNeverWaitForInitializer(t *testing.T) {
	d := NewDeferred(filepath.Join(t.TempDir(), "not-opened.sqlite"))
	defer d.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var opens atomic.Int32
	d.open = func(ctx context.Context, _ string, _ ...Options) (*Service, error) {
		opens.Add(1)
		close(entered)
		select {
		case <-release:
			return Open("")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	_, err := d.Call(context.Background(), wire.Scope{PrincipalID: "owner"}, "work_status", map[string]any{"action": "list"})
	deferredUnavailable(t, err)
	if opens.Load() != 0 {
		t.Fatal("constructor or request opened the archive before Start")
	}
	d.Start(context.Background())
	deferredWait(t, entered)
	var group sync.WaitGroup
	finished := make(chan struct{})
	for range 40 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := d.Call(context.Background(), wire.Scope{PrincipalID: "owner"}, "work_status", nil)
			deferredUnavailable(t, err)
			_, err = d.Runs(context.Background(), "project")
			deferredUnavailable(t, err)
			_, err = d.Iteration(context.Background(), "iteration")
			deferredUnavailable(t, err)
			_, err = d.OperationCount(context.Background(), "run")
			deferredUnavailable(t, err)
			target := d.AuditTarget(context.Background(), wire.Scope{PrincipalID: "owner"}, map[string]any{"action": "get", "run_id": "run_private"}, wire.Result{})
			if target == nil || target.Scope != "unattributed" || target.RunID != "" || target.ProjectID != "" {
				t.Errorf("unready archive exposed attribution: %#v", target)
			}
		}()
	}
	go func() { group.Wait(); close(finished) }()
	deferredWait(t, finished)
	if state, _ := d.State(); state != "loading" {
		t.Fatalf("test did not keep initializer blocked: %s", state)
	}
	close(release)
	deferredWait(t, d.Done())
	d.Start(context.Background())
	if state, err := d.State(); state != "ready" || err != nil || opens.Load() != 1 {
		t.Fatalf("publication or repeated Start failed: state=%s error=%v opens=%d", state, err, opens.Load())
	}
}

func TestDeferredArchiveCloseCancelsAndRejectsLatePublication(t *testing.T) {
	d := NewDeferred(filepath.Join(t.TempDir(), "unused.sqlite"))
	entered, cancelled, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	connection, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err = connection.Ping(); err != nil {
		t.Fatal(err)
	}
	d.open = func(ctx context.Context, _ string, _ ...Options) (*Service, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		defer close(returned)
		// Simulate a driver finishing after cancellation. Its database must not
		// be published or leaked even if the opener returns an apparent success.
		return &Service{db: connection}, nil
	}
	d.Start(context.Background())
	deferredWait(t, entered)
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	deferredWait(t, cancelled)
	deferredWait(t, returned)
	deferredWait(t, d.Done())
	d.Start(context.Background())
	if state, _ := d.State(); state != "closed" {
		t.Fatalf("closed reader became %s", state)
	}
	deadline := time.Now().Add(time.Second)
	for connection.Ping() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if connection.Ping() == nil {
		t.Fatal("late database reader was not closed")
	}
	_, err = d.Runs(context.Background(), "")
	deferredUnavailable(t, err)
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDeferredArchiveFailureIsBoundedPrivateAndDoesNotRetryOnRead(t *testing.T) {
	d := NewDeferred(filepath.Join(t.TempDir(), "private-archive.sqlite"))
	defer d.Close()
	var opens atomic.Int32
	d.open = func(ctx context.Context, path string, _ ...Options) (*Service, error) {
		opens.Add(1)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > ArchiveInitializationTimeout {
			t.Error("initializer lacks its bounded deadline")
		}
		return nil, errors.New("private failure at " + path)
	}
	d.Start(context.Background())
	deferredWait(t, d.Done())
	state, internal := d.State()
	if state != "failed" || internal == nil || !strings.Contains(internal.Error(), "private-archive") {
		t.Fatalf("operator diagnostic lost: %s %v", state, internal)
	}
	for range 20 {
		_, err := d.Runs(context.Background(), "")
		deferredUnavailable(t, err)
		if strings.Contains(err.Error(), "private-archive") || strings.Contains(err.Error(), "private failure") {
			t.Fatal("public error leaked archive failure detail")
		}
		d.Start(context.Background())
	}
	if opens.Load() != 1 {
		t.Fatalf("requests retried failed archive %d times", opens.Load())
	}
}

func deferredFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT);
CREATE TABLE runs(id TEXT PRIMARY KEY,body TEXT);
CREATE TABLE iterations(id TEXT PRIMARY KEY,run_id TEXT,body TEXT);
CREATE TABLE operations(id TEXT PRIMARY KEY,run_id TEXT,body TEXT);
CREATE INDEX operations_run ON operations(run_id);
CREATE TABLE documents(id TEXT,run_id TEXT,revision INTEGER,body TEXT);
CREATE TABLE events(sequence INTEGER PRIMARY KEY,run_id TEXT,kind TEXT,at TEXT,body TEXT);
CREATE TABLE work_jobs(id TEXT PRIMARY KEY,run_id TEXT,body TEXT);
INSERT INTO metadata VALUES('schema','3');
INSERT INTO runs VALUES('run_abcdefgh12345678','{"id":"run_abcdefgh12345678","principal_id":"archived-owner","project_id":"fixture","state":"ready","title":"Inert archive","mode":"manual","revision":1,"todos":[],"workspace":{"id":"ws_archived","branch":"archive"}}');
INSERT INTO operations VALUES('op_fixture','run_abcdefgh12345678','{"id":"op_fixture","run_id":"run_abcdefgh12345678","state":"unknown"}');`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDeferredArchiveSteadyContractsAndOriginalBytesRemainExact(t *testing.T) {
	path := deferredFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return time.Date(2026, 10, 31, 21, 22, 0, 0, time.UTC) }
	principalMap := map[string]string{"current-owner": "archived-owner"}
	options := Options{Clock: clock, PrincipalMap: principalMap}
	synchronous, err := Open(path, options)
	if err != nil {
		t.Fatal(err)
	}
	defer synchronous.Close()
	d := NewDeferred(path, options)
	defer d.Close()
	principalMap["current-owner"] = "wrong-owner"
	d.Start(context.Background())
	deferredWait(t, d.Done())
	if state, err := d.State(); state != "ready" || err != nil {
		t.Fatalf("archive failed: %s %v", state, err)
	}
	scope := wire.Scope{PrincipalID: "current-owner"}
	for _, arguments := range []map[string]any{
		{"action": "list"},
		{"action": "get", "run_id": "run_abcdefgh12345678", "section": "summary"},
		{"action": "get", "run_id": "run_abcdefgh12345678", "section": "operations"},
		{"action": "operation", "run_id": "run_abcdefgh12345678", "operation_id": "op_fixture"},
		{"action": "get", "run_id": "run_inaccessible12345678"},
	} {
		want, wantErr := synchronous.Call(context.Background(), scope, "work_status", arguments)
		got, gotErr := d.Call(context.Background(), scope, "work_status", arguments)
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotErr, wantErr) {
			t.Fatalf("steady archive contract changed for %#v: got=%#v/%v want=%#v/%v", arguments, got, gotErr, want, wantErr)
		}
		if !reflect.DeepEqual(d.AuditTarget(context.Background(), scope, arguments, got), synchronous.AuditTarget(context.Background(), scope, arguments, want)) {
			t.Fatal("steady archive audit attribution changed")
		}
	}
	var wantExport, gotExport bytes.Buffer
	wantSummary, wantErr := synchronous.Export(context.Background(), scope, &wantExport)
	gotSummary, gotErr := d.Export(context.Background(), scope, &gotExport)
	if wantErr != nil || gotErr != nil || wantSummary != gotSummary || !bytes.Equal(wantExport.Bytes(), gotExport.Bytes()) {
		t.Fatalf("deferred export changed: %v %v", wantErr, gotErr)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if err = synchronous.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("immutable archive changed: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err = os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("archive sidecar created: %s %v", suffix, err)
		}
	}
}

func TestArchiveContextCancellationAndEmptyDeferredHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenContext(ctx, filepath.Join(t.TempDir(), "absent.sqlite")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open touched or accepted archive: %v", err)
	}
	d := NewDeferred("")
	defer d.Close()
	if state, err := d.State(); state != "ready" || err != nil {
		t.Fatalf("no-archive configuration must be immediately ready: %s %v", state, err)
	}
	if rows, err := d.Runs(context.Background(), ""); err != nil || len(rows) != 0 {
		t.Fatalf("empty history changed: %#v %v", rows, err)
	}
	if _, err := d.Runs(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("reader ignored request cancellation: %v", err)
	}
}
