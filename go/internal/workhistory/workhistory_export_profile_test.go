package workhistory

// Diagnostic only: capture the actual exporter hot path under a short budget.
// This produces no contract pass and cannot replace the full export proof.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/pprof"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type workExportProfileSink struct{ bytes int64 }

func (w *workExportProfileSink) Write(value []byte) (int, error) {
	if w.bytes+int64(len(value)) > workExportMaximumBytes {
		return 0, errors.New("export profile reached its byte bound")
	}
	n, err := io.Discard.Write(value)
	w.bytes += int64(n)
	return n, err
}

func workExportCPUSeconds(value syscall.Timeval) float64 {
	return float64(value.Sec) + float64(value.Usec)/1000000
}

func TestHistoricalExportCPUProfile(t *testing.T) {
	planName := os.Getenv("SP6_WORK_EXPORT_PROFILE_PLAN")
	if planName == "" {
		t.Skip("explicit disposable export profile not supplied")
	}
	destinationName := os.Getenv("SP6_WORK_EXPORT_PROFILE_DEST")
	if destinationName == "" {
		t.Fatal("SP6_WORK_EXPORT_PROFILE_DEST is required")
	}
	planPath, err := contractPrivatePath(planName)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(planPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		t.Fatal("invalid bounded private profile plan")
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	var plan workExportPlan
	if err = json.Unmarshal(data, &plan); err != nil || plan.SchemaVersion != 1 || !plan.DisposableCopy || plan.SourceCommit != "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a" || plan.Archive.SHA256 != workExportCaptureSHA || !reflect.DeepEqual(plan.PreservedTableCounts, workExportCaptureCounts) {
		t.Fatal("profile requires the approved complete archive plan")
	}
	root := filepath.Dir(planPath)
	archive, err := contractPrivatePath(plan.Archive.Path)
	if err != nil || !contractWithin(root, archive) {
		t.Fatal("profile archive escapes its disposable fixture")
	}
	initial, err := os.Lstat(plan.Archive.Path)
	if err != nil || !initial.Mode().IsRegular() || initial.Mode().Perm()&0222 != 0 {
		t.Fatal("profile requires a real read-only consumer archive")
	}
	if st, ok := initial.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
		t.Fatal("profile consumer must not have another hard link")
	}
	if err = workExportNoSidecars(archive); err != nil {
		t.Fatal(err)
	}
	beforeHash, beforeBytes, err := contractDigest(archive)
	if err != nil || beforeHash != workExportCaptureSHA || beforeBytes != plan.Archive.Bytes {
		t.Fatal("profile consumer bytes differ from the approved capture")
	}
	destination, err := filepath.Abs(destinationName)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := contractPrivatePath(filepath.Dir(destination))
	if err != nil || parent != root || filepath.Dir(destination) != root || destination == filepath.Dir(archive) {
		t.Fatal("profile output must be a new direct child of the private plan directory")
	}
	if _, err = os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatal("profile output already exists")
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	clock, err := time.Parse(time.RFC3339Nano, plan.Clock)
	if err != nil {
		t.Fatal("invalid profile clock")
	}
	service, err := Open(archive, Options{Clock: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	principalSet := map[string]bool{}
	for _, run := range service.runs {
		principal, ok := run["principal_id"].(string)
		if !ok || principal == "" {
			t.Fatal("profile archive lacks valid ownership")
		}
		principalSet[principal] = true
	}
	principals := []string{}
	for principal := range principalSet {
		principals = append(principals, principal)
	}
	sort.Strings(principals)
	if len(principals) == 0 {
		t.Fatal("profile archive is empty")
	}
	profilePath := filepath.Join(destination, "cpu.pprof")
	profile, err := os.OpenFile(profilePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = pprof.StartCPUProfile(profile); err != nil {
		profile.Close()
		t.Fatal(err)
	}
	var beforeCPU, afterCPU syscall.Rusage
	var beforeMemory, afterMemory runtime.MemStats
	if err = syscall.Getrusage(syscall.RUSAGE_SELF, &beforeCPU); err != nil {
		pprof.StopCPUProfile()
		profile.Close()
		t.Fatal(err)
	}
	runtime.ReadMemStats(&beforeMemory)
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	sink := &workExportProfileSink{}
	summary, exportErr := service.Export(ctx, wire.Scope{PrincipalID: principals[0]}, sink)
	cancel()
	elapsed := time.Since(started).Seconds()
	runtime.ReadMemStats(&afterMemory)
	cpuErr := syscall.Getrusage(syscall.RUSAGE_SELF, &afterCPU)
	pprof.StopCPUProfile()
	syncErr, closeErr := profile.Sync(), profile.Close()
	if cpuErr != nil || syncErr != nil || closeErr != nil {
		t.Fatal("profile resource snapshot or durable profile write failed")
	}
	outcome := "completed"
	if exportErr != nil {
		if !errors.Is(exportErr, context.DeadlineExceeded) {
			t.Fatalf("unexpected export diagnostic failure: %v", exportErr)
		}
		outcome = "bounded-context-expiry"
	}
	if err = service.Close(); err != nil {
		t.Fatal(err)
	}
	afterHash, afterBytes, err := contractDigest(archive)
	after, statErr := os.Stat(archive)
	if err != nil || statErr != nil || afterHash != beforeHash || afterBytes != beforeBytes || !os.SameFile(initial, after) || !initial.ModTime().Equal(after.ModTime()) {
		t.Fatal("export profiling changed the private archive")
	}
	if err = workExportNoSidecars(archive); err != nil {
		t.Fatal(err)
	}
	profileHash, profileBytes, err := contractDigest(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	report := map[string]any{
		"schema_version": 1, "lane": "historical-export-cpu-diagnostic", "compatibility_pass": false,
		"outcome": outcome, "budget_seconds": 20, "elapsed_seconds": elapsed,
		"user_cpu_seconds":   workExportCPUSeconds(afterCPU.Utime) - workExportCPUSeconds(beforeCPU.Utime),
		"system_cpu_seconds": workExportCPUSeconds(afterCPU.Stime) - workExportCPUSeconds(beforeCPU.Stime),
		"peak_rss_kib":       afterCPU.Maxrss, "allocated_bytes": afterMemory.TotalAlloc - beforeMemory.TotalAlloc,
		"gc_cycles": afterMemory.NumGC - beforeMemory.NumGC, "emitted_bytes": sink.bytes,
		"partial_summary": summary, "archive_sha256": afterHash, "archive_unchanged": true,
		"profile_sha256": profileHash, "profile_bytes": profileBytes,
		"output_sink": "counting io.Discard; serialization/redaction/hash retained; physical output IO excluded",
		"scope":       "First original principal only; no verifier or second redaction pass; no live SQLite, coordinator, launcher or audit.",
	}
	if err = contractWriteJSON(filepath.Join(destination, "profile-summary.json"), report); err != nil {
		t.Fatal(err)
	}
	t.Logf("export diagnostic: outcome=%s wall=%.3fs user_cpu=%.3fs emitted_bytes=%d records=%d; compatibility_pass=false", outcome, elapsed, workExportCPUSeconds(afterCPU.Utime)-workExportCPUSeconds(beforeCPU.Utime), sink.bytes, summary.Records)
}
