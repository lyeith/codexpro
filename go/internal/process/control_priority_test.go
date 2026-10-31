package process

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHostControlCommandsUseBackgroundCPUPriority(t *testing.T) {
	value, err := RunBounded(context.Background(), time.Second, 128, "python3", "-c", "import os; print(os.getpriority(os.PRIO_PROCESS,0))")
	if err != nil {
		t.Fatal(err)
	}
	priority, err := strconv.Atoi(strings.TrimSpace(string(value)))
	if err != nil || priority < 10 {
		t.Fatalf("control helper displaced server priority: nice=%q err=%v", value, err)
	}
}
