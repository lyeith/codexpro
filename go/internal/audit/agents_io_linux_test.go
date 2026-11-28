//go:build linux

package audit

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func processIO(t *testing.T) map[string]int64 {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Skip("no /proc/self/io")
	}
	out := map[string]int64{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var key string
		var value int64
		fmt.Sscanf(strings.Replace(line, ":", " ", 1), "%s %d", &key, &value)
		out[key] = value
	}
	return out
}

// Agent tracking must keep the cost of an append constant and small: about
// one rewritten clustered b-tree page per append compared with unlabelled
// records, not growing with history. An agent read must not write and must
// read a bounded amount. wchar counts bytes passed to write(2), so it measures
// the segment append and SQLite WAL frames without depending on the disk.
func TestAgentTrackingKeepsAppendAndReadIOBounded(t *testing.T) {
	ctx := context.Background()
	// Warm up past every agent's slot set, so the windows measure the steady
	// state in which an append rewrites one existing slot.
	const agents = 5
	perAppend := func(labelled bool, warmup, measured int) (early, late int64) {
		options := testOptions(t.TempDir())
		options.SegmentBytes = 1 << 20
		options.MaxBytes = 1 << 20
		options.RetainActions = 100
		j := openTest(t, options)
		record := func(i int) {
			input := recordInput(fmt.Sprintf("p%02d", i%20), fmt.Sprintf("io-%d", i))
			if labelled {
				input.Scope.Agent = fmt.Sprintf("SP%d", i%agents)
				input.Scope.Identity = wire.Identity{Source: "named"}
			}
			writeTest(t, j, input)
		}
		window := func(from int) int64 {
			before := processIO(t)["wchar"]
			for i := from; i < from+measured; i++ {
				record(i)
			}
			return (processIO(t)["wchar"] - before) / int64(measured)
		}
		for i := 0; i < warmup; i++ {
			record(i)
		}
		early = window(warmup)
		for i := warmup + measured; i < 3*warmup; i++ {
			record(i)
		}
		late = window(3 * warmup)
		return early, late
	}
	plainEarly, plainLate := perAppend(false, 200, 150)
	labelledEarly, labelledLate := perAppend(true, 200, 150)
	t.Logf("wchar per append: unlabelled %d then %d, labelled %d then %d", plainEarly, plainLate, labelledEarly, labelledLate)
	const page = 4096 + 24 // one SQLite WAL frame
	if extra := labelledLate - plainLate; extra > page+page/2 {
		t.Fatalf("agent tracking adds %d bytes per append; budget is one WAL frame", extra)
	}
	if labelledLate > labelledEarly+labelledEarly/4 {
		t.Fatalf("per-append writes grew with history: %d then %d", labelledEarly, labelledLate)
	}

	j := openTest(t, testOptions(t.TempDir()))
	for i := 0; i < 3*agentActionsPerAgent; i++ {
		input := recordInput("alpha", fmt.Sprintf("read-%d", i))
		input.Scope.Agent = "SP16"
		writeTest(t, j, input)
	}
	before := processIO(t)
	for i := 0; i < 50; i++ {
		activity, err := j.AgentActivity(ctx, "SP16")
		if err != nil || len(activity["actions"].([]any)) != agentActionsPerAgent {
			t.Fatalf("agent read: %v %v", activity, err)
		}
	}
	after := processIO(t)
	if written := after["wchar"] - before["wchar"]; written != 0 {
		t.Fatalf("agent reads wrote %d bytes", written)
	}
	if read := (after["rchar"] - before["rchar"]) / 50; read > 64<<10 {
		t.Fatalf("agent read cost %d bytes per call", read)
	}
}
