package search

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
)

func fp5Fact(i int) (fileIdentity, *sourceFacts) {
	// Captures initially refer to an 8 KiB source backing string, like regexp results.
	text := fmt.Sprintf("name%04d", i) + strings.Repeat(" ", 8192)
	return fileIdentity{Abs: fmt.Sprintf("/fp5/private/file-%04d.ts", i%512), Size: int64(len(text)), ModifiedNS: int64(i + 1)},
		&sourceFacts{TextLen: len(text), Declarations: []declarationFact{{Name: text[:8], Kind: "variable", Line: 1}}}
}

func BenchmarkFP5FactCacheChurn(b *testing.B) {
	var c factCache
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		id, f := fp5Fact(i)
		c.storeSource(id, f)
		if c.sourceFact(id) == nil {
			b.Fatal("new small fact missing")
		}
	}
	runtime.KeepAlive(&c)
}

func TestFP5CacheMeasurement(t *testing.T) {
	if os.Getenv("FP5_PERF") != "1" {
		t.Skip("opt-in retention measurement")
	}
	var c factCache
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 10000; i++ {
		id, f := fp5Fact(i)
		c.storeSource(id, f)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("FP5_CACHE versions=10000 paths=512 heap_retained_bytes=%d allocated_bytes=%d", int64(after.HeapAlloc)-int64(before.HeapAlloc), after.TotalAlloc-before.TotalAlloc)
	runtime.KeepAlive(&c)
}
