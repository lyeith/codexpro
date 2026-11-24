package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The fixture uses the ordinary durable writer and a fixed spread of projects.
// It never reads a configured journal or starts a service.
func auditPerformanceFixture(tb testing.TB, count int) *Journal {
	tb.Helper()
	options := Options{Directory: tb.TempDir(), Enabled: true, MaxBytes: 64 << 20,
		RetainActions: 350, SegmentBytes: 1 << 20}
	j, err := Open(options)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := j.Close(); err != nil {
			tb.Error(err)
		}
	})
	at := time.Now().Add(-time.Minute)
	for i := 0; i < count; i++ {
		input := collectorInput(fmt.Sprintf("project-%02d", i%20), fmt.Sprintf("seed-%d", i),
			fmt.Sprintf("SP%d", i%9+1), "read", at, at)
		if receipt, err := j.Record(context.Background(), input); err != nil || !receipt.Recorded {
			tb.Fatalf("seed %d: %+v %v", i, receipt, err)
		}
	}
	// Receipt publication precedes maintenance; wait for that final seed batch.
	j.mu.RLock()
	j.mu.RUnlock()
	return j
}

func BenchmarkAuditCollectorRetained(b *testing.B) {
	j := auditPerformanceFixture(b, 7000)
	at := time.Now()
	if got := j.CollectEventTimes(context.Background(), at); !got.Known {
		b.Fatal("fixture was not available")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := j.CollectEventTimes(context.Background(), at); !got.Known {
			b.Fatal("idle journal became unavailable")
		}
	}
}

func BenchmarkAuditCollectorAfterAppend(b *testing.B) {
	j := auditPerformanceFixture(b, 7000)
	at := time.Now()
	if got := j.CollectEventTimes(context.Background(), at); !got.Known {
		b.Fatal("fixture was not available")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		input := collectorInput("collector-append", fmt.Sprintf("poll-%d", i), "SP1", "read", at, at)
		if receipt, err := j.Record(context.Background(), input); err != nil || !receipt.Recorded {
			b.Fatalf("append: %+v %v", receipt, err)
		}
		j.mu.RLock()
		j.mu.RUnlock()
		b.StartTimer()
		if got := j.CollectEventTimes(context.Background(), at); !got.Known {
			b.Fatal("collector unavailable after settled append")
		}
	}
}

func BenchmarkAuditDurableAppend(b *testing.B) {
	j := auditPerformanceFixture(b, 7000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		input := recordInput("append-project", fmt.Sprintf("append-%d", i))
		if receipt, err := j.Record(context.Background(), input); err != nil || !receipt.Recorded {
			b.Fatalf("append: %+v %v", receipt, err)
		}
	}
}

type auditProcessCounters struct {
	CPUSeconds float64
	IO         map[string]uint64
	RSSKiB     uint64
}

func auditReadProcessCounters(t *testing.T) auditProcessCounters {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	out := auditProcessCounters{
		CPUSeconds: float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6,
		IO:         make(map[string]uint64),
	}
	for _, path := range []string{"/proc/self/io", "/proc/self/status"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			name := strings.TrimSuffix(fields[0], ":")
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				continue
			}
			if path == "/proc/self/io" {
				out.IO[name] = value
			} else if name == "VmRSS" {
				out.RSSKiB = value
			}
		}
	}
	return out
}

// Run explicitly with FP5_AUDIT_IDLE_SECONDS=60. Setup and the warm collector
// observation are outside the measured idle interval; the poll cadence is 1 Hz.
func TestAuditIdleMeasurement(t *testing.T) {
	raw := os.Getenv("FP5_AUDIT_IDLE_SECONDS")
	if raw == "" {
		t.Skip("explicit local performance fixture")
	}
	if runtime.GOOS != "linux" {
		t.Skip("process I/O and RSS counters require Linux")
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 1 || seconds > 300 {
		t.Fatal("FP5_AUDIT_IDLE_SECONDS must be between 1 and 300")
	}
	j := auditPerformanceFixture(t, 7000)
	if got := j.CollectEventTimes(context.Background(), time.Now()); !got.Known {
		t.Fatal("fixture was not available")
	}
	runtime.GC()
	before := auditReadProcessCounters(t)
	started := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for i := 0; i < seconds; i++ {
		<-ticker.C
		if got := j.CollectEventTimes(context.Background(), time.Now()); !got.Known {
			t.Fatal("idle journal became unavailable")
		}
	}
	elapsed := time.Since(started).Seconds()
	after := auditReadProcessCounters(t)
	ioPerMinute := make(map[string]float64)
	for name, value := range after.IO {
		ioPerMinute[name] = float64(value-before.IO[name]) * 60 / elapsed
	}
	body, err := json.Marshal(map[string]any{
		"retained_actions": len(j.entries), "projects": 20, "polls": seconds, "poll_hz": 1,
		"elapsed_seconds": elapsed, "cpu_seconds_per_minute": (after.CPUSeconds - before.CPUSeconds) * 60 / elapsed,
		"io_per_minute": ioPerMinute, "rss_start_kib": before.RSSKiB, "rss_end_kib": after.RSSKiB,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("AUDIT_IDLE_METRICS %s", body)
}
