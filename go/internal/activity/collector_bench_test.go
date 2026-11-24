package activity

import (
	"context"
	"sort"
	"testing"
	"time"
)

func BenchmarkCollectorCompletionRead(b *testing.B) {
	metrics := NewMetrics(nil)
	source := newCompletionJobs()
	h := New(Options{Metrics: metrics, Jobs: source})
	defer h.Close()
	metrics.begin("FP5", "read", time.Now())(false)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		value := h.CollectAgentStatus(context.Background(), "FP5")
		if !value.Activity.Known || value.Activity.Agents[0].CompletedCalls != 1 {
			b.Fatal("completion read lost known counters")
		}
	}
}

func BenchmarkCollectorCompletionWake(b *testing.B) {
	metrics := NewMetrics(nil)
	source := newCompletionJobs()
	source.observed = make(chan struct{}, 1)
	h := New(Options{Metrics: metrics, Jobs: source})
	defer h.Close()
	latencies := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		select {
		case <-source.observed:
		default:
		}
		finish := metrics.begin("FP5", "read", time.Now())
		baseline := uint64(n)
		done := make(chan CollectorStatus, 1)
		go func() {
			done <- h.waitCollector(context.Background(), CollectorQuery{Agent: "FP5", Wait: time.Second, SinceCompleted: &baseline})
		}()
		select {
		case <-source.observed:
		case <-time.After(time.Second):
			b.Fatal("wait did not observe current state")
		}
		start := time.Now()
		finish(false)
		value := <-done
		latencies = append(latencies, time.Since(start))
		if !value.Activity.Known || value.Activity.Agents[0].CompletedCalls != uint64(n+1) {
			b.Fatal("completion wait lost its transition")
		}
	}
	b.StopTimer()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	if len(latencies) > 0 {
		b.ReportMetric(float64(latencies[len(latencies)/2].Nanoseconds()), "p50-ns/wake")
		b.ReportMetric(float64(latencies[(len(latencies)-1)*95/100].Nanoseconds()), "p95-ns/wake")
		b.ReportMetric(float64(latencies[len(latencies)-1].Nanoseconds()), "max-ns/wake")
	}
}
