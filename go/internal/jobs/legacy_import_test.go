package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type legacyFixture struct {
	base, source, original, workspace string
	cfg                               *config.Config
	options                           LegacyImportOptions
	records                           []map[string]any
	metadata                          []byte
}

func newLegacyFixture(t *testing.T) *legacyFixture {
	t.Helper()
	t.Setenv("CODEXPRO_JOB_SCOPES", "0")
	base := t.TempDir()
	f := &legacyFixture{base: base, source: filepath.Join(base, "snapshot"), original: filepath.Join(base, "private-ts", "jobs"), workspace: filepath.Join(base, "workspace")}
	for _, dir := range []string{f.source, f.workspace} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.cfg = config.New(filepath.Join(base, "imported"))
	f.cfg.Set("jobRetentionMs", 86400000)
	f.options = LegacyImportOptions{SourceDir: f.source, OriginalJobsDir: f.original}
	return f
}

func (f *legacyFixture) record(id, status string) map[string]any {
	now := time.Now().Add(-time.Second).UTC().Truncate(time.Millisecond)
	return map[string]any{"id": id, "workspace_id": "test-workspace", "project_id": "test", "root": f.workspace, "cwd": ".", "command": "historical command", "command_label": "history", "pid": 987654321, "origin": "background", "status": status, "started_at": now.Format("2006-01-02T15:04:05.000Z"), "started_at_ms": now.UnixMilli(), "finished_at": now.Add(50 * time.Millisecond).Format("2006-01-02T15:04:05.000Z"), "finished_at_ms": now.UnixMilli() + 50, "exit_code": 0, "signal": nil, "deadline_ms": now.UnixMilli() + 5000, "timeout_ms": 5000, "output_limit_bytes": 8192, "stdout_path": filepath.Join(f.original, id+".out"), "stderr_path": filepath.Join(f.original, id+".err"), "exit_path": filepath.Join(f.original, id+".exit"), "result_path": filepath.Join(f.original, id+".result.json"), "control_path": filepath.Join(f.original, id+".control"), "grant_path": filepath.Join(f.original, id+".grant"), "acknowledged": false, "runner_version": 2, "launch_state": "granted", "quiescent": true, "quiescence_scope": "systemd_scope", "input_job_ids": []string{}, "work": map[string]any{"work_id": "historical-work", "run_id": "old-run"}}
}

func (f *legacyFixture) output(t *testing.T, r map[string]any, rawOut, rawErr, renderedOut, renderedErr []byte) {
	t.Helper()
	id := r["id"].(string)
	sum := sha256.Sum256([]byte(r["workspace_id"].(string)))
	dir := filepath.Join(f.source, "output", hex.EncodeToString(sum[:])[:24], id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{filepath.Join(f.source, id+".out"): rawOut, filepath.Join(f.source, id+".err"): rawErr, filepath.Join(dir, "stdout.log"): renderedOut, filepath.Join(dir, "stderr.log"): renderedErr} {
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *legacyFixture) save(t *testing.T) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"version": 1, "jobs": f.records})
	if err != nil {
		t.Fatal(err)
	}
	f.metadata = b
	sum := sha256.Sum256(b)
	f.options.ExpectedMetadataSHA256 = hex.EncodeToString(sum[:])
	if err = os.WriteFile(filepath.Join(f.source, "jobs.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyImportPreservesHistoryCursorAndNeverAdoptsOwners(t *testing.T) {
	f := newLegacyFixture(t)
	child := exec.Command("sleep", "20")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	good := f.record("job_00000001", "succeeded")
	good["scope_unit"] = "never-adopt-this.scope"
	good["process_identity"] = "foreign"
	good["resource_claim"] = map[string]any{"version": 1, "helper": "/must-not-execute", "run_id": strings.Repeat("a", 32), "registration_id": strings.Repeat("b", 32)}
	expired := f.record("job_00000002", "failed")
	expired["exit_code"] = 7
	expired["output_expired"] = true
	expired["captured_stdout_bytes"] = 120
	expired["captured_stderr_bytes"] = 9
	active := f.record("job_00000003", "running")
	active["pid"] = child.Process.Pid
	active["quiescent"] = false
	prepared := f.record("job_00000004", "failed")
	prepared["launch_state"] = "prepared"
	unsettled := f.record("job_00000005", "stopped")
	unsettled["quiescent"] = false
	unsettled["scope_unit"] = "unsettled.scope"
	f.records = []map[string]any{good, expired, active, prepared, unsettled}
	rendered := []byte(strings.Repeat("αβγδ\n", 60))
	raw := append([]byte("raw secret\x00"), rendered...)
	f.output(t, good, raw, []byte("warn\n"), rendered, []byte("warn\n"))
	f.save(t)
	before, err := os.Stat(filepath.Join(f.source, "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := ImportLegacySnapshot(context.Background(), f.cfg, f.options)
	if err != nil {
		t.Fatal(err)
	}
	if report.ImportedRecords != 2 || report.ArchiveOnlyRecords != 3 || report.ExpiredRecords != 1 || report.RawBytes != int64(len(raw)+5) || report.RenderedBytes != int64(len(rendered)+5) {
		t.Fatalf("wrong import receipt: %+v", report)
	}
	archive, err := os.ReadFile(report.ArchivePath)
	if err != nil || !bytes.Equal(archive, f.metadata) {
		t.Fatalf("archive lost exact source record: %v", err)
	}
	after, err := os.Stat(filepath.Join(f.source, "jobs.json"))
	if err != nil || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("source metadata was modified")
	}
	resolver := testResolver{wire.Workspace{ID: "test-workspace", ProjectID: "test", Root: f.workspace, Mode: "workspace"}}
	s, err := New(f.cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if !s.closed.Load() {
			_ = s.Close()
		}
	}()
	var completions atomic.Int32
	s.SetCompletionSink(func(Record) error { completions.Add(1); return nil })
	if len(s.Snapshot()) != 2 {
		t.Fatal("archive-only owner entered live jobs table")
	}
	r, ok := s.Get("job_00000001", "")
	if !ok {
		t.Fatal("missing imported history")
	}
	if r.PID != 0 || r.ProcessIdentity != "" || r.ScopeUnit != "" || r.ResourceClaim != nil || r.ScopeIdentity != nil || r.ControlPath != "" || r.GrantPath != "" || r.ResultPath != "" || r.SpecPath != "" || r.ExitPath != "" || r.LaunchState != "historical" || !r.Quiescent {
		t.Fatalf("foreign process authority survived: %+v", r)
	}
	var pending int
	if err = s.db.QueryRow("SELECT count(*) FROM job_completions").Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("history re-emitted completion: %d %v", pending, err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM job_inputs").Scan(&pending); err != nil || pending != 0 {
		t.Fatal("historical dependency became an active pin")
	}
	metadata := invoke(t, s, "jobs", map[string]any{"job_ids": []string{r.ID}, "output": "none", "wait_ms": 0})
	view := metadata["jobs"].([]any)[0].(map[string]any)
	if view["stdout_bytes"] != float64(len(raw)) || view["available_stdout_bytes"] != float64(len(rendered)) || view["output_growing"] != false {
		t.Fatalf("raw/rendered count semantics changed: %#v", view)
	}
	// This is an existing TypeScript cursor: JSON.stringify key order and byte
	// offsets are kept, rather than assigning a new identity or regenerating logs.
	cursorJSON := `{"v":1,"id":"test-workspace:` + r.ID + `:` + r.StartedAt + `","offsets":[9,2]}`
	cursor := base64.RawURLEncoding.EncodeToString([]byte(cursorJSON))
	var out, stderr strings.Builder
	for pageCount := 0; pageCount < 10; pageCount++ {
		page, err := s.page(r, cursor, 256)
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString(page.Stdout)
		stderr.WriteString(page.Stderr)
		cursor = page.NextCursor
		if page.OutputComplete {
			break
		}
		if pageCount == 9 {
			t.Fatal("imported cursor did not finish")
		}
	}
	if out.String() != string(rendered[9:]) || stderr.String() != "rn\n" {
		t.Fatal("legacy cursor output omitted or duplicated bytes")
	}
	r, _ = s.Get(r.ID, "")
	if r.Acknowledged {
		t.Fatal("metadata/cursor inspection acknowledged history")
	}
	expiredRecord, _ := s.Get("job_00000002", "")
	expiredMeta := s.OutputMetadata(expiredRecord)
	if expiredMeta["stdout_bytes"] != int64(120) || expiredMeta["output_available"] != false || expiredMeta["available_stdout_bytes"] != int64(0) {
		t.Fatalf("expired receipt changed: %#v", expiredMeta)
	}
	if _, err = s.page(expiredRecord, "", 256); err == nil {
		t.Fatal("expired history exposed invented output")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(f.cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	r, _ = restarted.Get(r.ID, "")
	page, err := restarted.page(r, cursor, 256)
	if err != nil || page.ReturnedBytes != 0 || !page.OutputComplete {
		t.Fatal("restart invalidated final legacy cursor")
	}
	if err = child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("import/recovery controlled the source-owned live process")
	}
	if completions.Load() != 0 {
		t.Fatal("completion sink ran for historical import")
	}
	firstManifest, err := os.Stat(filepath.Join(f.cfg.Home, legacyImportManifest))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := ImportLegacySnapshot(context.Background(), f.cfg, f.options)
	if err != nil || !replayed.Replayed || replayed.ImportedAt != report.ImportedAt {
		t.Fatalf("repeat import was not an idempotent receipt: %+v %v", replayed, err)
	}
	lastManifest, _ := os.Stat(filepath.Join(f.cfg.Home, legacyImportManifest))
	if !firstManifest.ModTime().Equal(lastManifest.ModTime()) {
		t.Fatal("receipt replay rewrote state")
	}
}

func TestLegacyImportRejectsIncompleteAndEscapingSnapshots(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *legacyFixture, map[string]any)
	}{
		{"foreign output path", func(t *testing.T, f *legacyFixture, r map[string]any) { r["stdout_path"] = "/tmp/foreign.out" }},
		{"file symlink", func(t *testing.T, f *legacyFixture, r map[string]any) {
			p := filepath.Join(f.source, r["id"].(string)+".out")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(f.source, r["id"].(string)+".err"), p); err != nil {
				t.Fatal(err)
			}
		}},
		{"rendered directory symlink", func(t *testing.T, f *legacyFixture, r map[string]any) {
			p := filepath.Join(f.source, "output")
			if err := os.Rename(p, filepath.Join(f.base, "elsewhere")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(f.base, "elsewhere"), p); err != nil {
				t.Fatal(err)
			}
		}},
		{"hard link", func(t *testing.T, f *legacyFixture, r map[string]any) {
			if err := os.Link(filepath.Join(f.source, r["id"].(string)+".out"), filepath.Join(f.base, "second-name")); err != nil {
				t.Fatal(err)
			}
		}},
		{"fifo", func(t *testing.T, f *legacyFixture, r map[string]any) {
			p := filepath.Join(f.source, r["id"].(string)+".out")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(p, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"invalid rendered UTF8", func(t *testing.T, f *legacyFixture, r map[string]any) {
			f.output(t, r, []byte("ok"), nil, []byte{0xff}, nil)
		}},
		{"missing rendered legacy", func(t *testing.T, f *legacyFixture, r map[string]any) {
			delete(r, "runner_version")
			if err := os.RemoveAll(filepath.Join(f.source, "output")); err != nil {
				t.Fatal(err)
			}
		}},
		{"per-file bound", func(t *testing.T, f *legacyFixture, r map[string]any) { f.options.MaxFileBytes = 1 }},
		{"duplicate IDs", func(t *testing.T, f *legacyFixture, r map[string]any) { f.records = append(f.records, r) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newLegacyFixture(t)
			r := f.record("job_00000001", "succeeded")
			f.records = []map[string]any{r}
			f.output(t, r, []byte("ok"), nil, []byte("ok"), nil)
			test.change(t, f, r)
			f.save(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := ImportLegacySnapshot(ctx, f.cfg, f.options); err == nil {
				t.Fatal("unsafe/incomplete snapshot accepted")
			}
			if _, err := os.Lstat(filepath.Join(f.cfg.Home, legacyImportMarker)); err == nil {
				resolver := testResolver{wire.Workspace{ID: "test-workspace", ProjectID: "test", Root: f.workspace}}
				if s, err := New(f.cfg, resolver); err == nil {
					_ = s.Close()
					t.Fatal("incomplete import was opened for process recovery")
				}
			}
		})
	}
}

func TestLegacyImportDigestReplayAndReservation(t *testing.T) {
	f := newLegacyFixture(t)
	r := f.record("job_00000001", "succeeded")
	f.records = []map[string]any{r}
	f.output(t, r, []byte("ok"), nil, []byte("ok"), nil)
	f.save(t)
	bad := f.options
	bad.ExpectedMetadataSHA256 = strings.Repeat("0", 64)
	if _, err := ImportLegacySnapshot(context.Background(), f.cfg, bad); err == nil {
		t.Fatal("wrong source digest accepted")
	}
	if _, err := os.Lstat(f.cfg.Home); !os.IsNotExist(err) {
		t.Fatal("invalid digest reserved destination")
	}
	if _, err := ImportLegacySnapshot(context.Background(), f.cfg, f.options); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.source, r["id"].(string)+".out"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportLegacySnapshot(context.Background(), f.cfg, f.options); err == nil {
		t.Fatal("changed snapshot replay accepted")
	}
	fresh := filepath.Join(f.base, "quarantine")
	if err := os.Mkdir(fresh, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := lockJobsHome(fresh, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lockJobsHome(fresh, false); err == nil {
		t.Fatal("constructor overlapped import reservation")
	}
	if err = process.ExclusiveWrite(filepath.Join(fresh, legacyImportMarker), []byte("incomplete\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lock.Close()
	if _, err = lockJobsHome(fresh, false); err == nil {
		t.Fatal("crash marker did not quarantine destination")
	}
}

func TestLegacyImportNeverReadsOriginalDirectory(t *testing.T) {
	f := newLegacyFixture(t)
	r := f.record("job_00000001", "succeeded")
	delete(r, "quiescent")
	delete(r, "launch_state")
	delete(r, "runner_version")
	f.records = []map[string]any{r}
	f.output(t, r, []byte("copied"), nil, []byte("rendered"), nil)
	f.save(t)
	// The declared original directory does not exist. All reads must be fixed
	// relative paths in the private snapshot, including old rendered outputs.
	if _, err := os.Stat(f.original); !os.IsNotExist(err) {
		t.Fatal("fixture accidentally supplied original state")
	}
	report, err := ImportLegacySnapshot(context.Background(), f.cfg, f.options)
	if err != nil || report.ImportedRecords != 1 {
		t.Fatalf("private snapshot needed original state: %+v %v", report, err)
	}
}
