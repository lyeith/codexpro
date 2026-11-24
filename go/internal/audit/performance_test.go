package audit

import (
	"context"
	"fmt"
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
		input := recordInput(fmt.Sprintf("project-%02d", i%20), fmt.Sprintf("seed-%d", i))
		input.Scope.Agent, input.ToolName = fmt.Sprintf("SP%d", i%9+1), "read"
		input.StartedAt, input.FinishedAt = at, at
		if receipt, err := j.Record(context.Background(), input); err != nil || !receipt.Recorded {
			tb.Fatalf("seed %d: %+v %v", i, receipt, err)
		}
	}
	// Receipt publication precedes maintenance; wait for that final seed batch.
	j.mu.RLock()
	j.mu.RUnlock()
	return j
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
