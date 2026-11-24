package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func fp5History(n int) []Record {
	rows := make([]Record, n)
	now := time.Now().UnixMilli()
	for i := range rows {
		rows[i] = Record{ID: fmt.Sprintf("fp5-%06d", i), WorkspaceID: "fp5-private", Status: "succeeded", Origin: "background", StartedAtMS: now - int64(i), FinishedAtMS: now - int64(i), Quiescent: true, StoredBytes: 256, ExitCode: intPointer(0)}
	}
	return rows
}

func BenchmarkFP5QueueHistory(b *testing.B) {
	for _, n := range []int{500, 5000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			s := &Service{cfg: config.New(b.TempDir()), records: map[string]*jobEntry{}}
			for _, r := range fp5History(n) {
				s.records[r.ID] = newEntry(r, false)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if s.nextQueued() != nil {
					b.Fatal("unexpected queued work")
				}
			}
		})
	}
}

func fp5ProcessCounters(t *testing.T) (cpu float64, writes, writeBytes, rss uint64) {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	cpu = float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
	read := func(path string) map[string]uint64 {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]uint64{}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				v, _ := strconv.ParseUint(f[1], 10, 64)
				out[strings.TrimSuffix(f[0], ":")] = v
			}
		}
		return out
	}
	io := read("/proc/self/io")
	status := read("/proc/self/status")
	return cpu, io["syscw"], io["write_bytes"], status["VmRSS"] * 1024
}

// Opt-in, real local Service instance with synthetic history and private SQLite.
// Setup is excluded; the same 60-second phase is used before and after.
func TestFP5IdleMeasurement(t *testing.T) {
	if os.Getenv("FP5_PERF") != "1" {
		t.Skip("opt-in one-minute measurement")
	}
	t.Setenv("CODEXPRO_JOB_SCOPES", "0")
	root := t.TempDir()
	cfg := config.New(filepath.Join(root, "state"))
	cfg.Set("maxJobHistoryPerWorkspace", 10000)
	cfg.Set("jobRetentionMs", 3600000)
	cfg.Set("maxRetainedJobBytes", 1<<30)
	s, err := NewPaused(cfg, testResolver{wire.Workspace{ID: "fp5-private", Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.insertRecords(fp5History(5000)); err != nil {
		t.Fatal(err)
	}
	s.StartBackground()
	time.Sleep(250 * time.Millisecond)
	runtime.GC()
	c0, w0, b0, r0 := fp5ProcessCounters(t)
	start := time.Now()
	timer := time.NewTimer(60 * time.Second)
	<-timer.C
	c1, w1, b1, r1 := fp5ProcessCounters(t)
	elapsed := time.Since(start).Seconds()
	t.Logf("FP5_IDLE history=5000 seconds=%.3f cpu_seconds_per_minute=%.6f writes_per_minute=%.3f write_bytes_per_minute=%.3f rss_start_bytes=%d rss_end_bytes=%d", elapsed, (c1-c0)*60/elapsed, float64(w1-w0)*60/elapsed, float64(b1-b0)*60/elapsed, r0, r1)
}
