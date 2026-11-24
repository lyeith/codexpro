package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
)

func fp5CaptureRepo(tb testing.TB) string {
	tb.Helper()
	root := tb.TempDir()
	cmd := exec.Command("git", "init", "-q", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("git init: %v %s", err, out)
	}
	for i := 0; i < 100; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%03d.txt", i)), []byte("fixture\n"), 0600); err != nil {
			tb.Fatal(err)
		}
	}
	return root
}

func BenchmarkFP5GitCapture(b *testing.B) {
	root := fp5CaptureRepo(b)
	e := &Engine{Config: config.New(b.TempDir())}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := e.captureGitEvidence(context.Background(), root); got == nil || got["changed_path_count"] != 100 {
			b.Fatalf("capture: %#v", got)
		}
	}
}
