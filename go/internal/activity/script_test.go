package activity

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Node is a development-only syntax checker already used by the side-by-side
// TypeScript contract harness. The Go service does not invoke it.
func TestEmbeddedDashboardScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable for development-only JavaScript syntax verification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--check", "--input-type=commonjs")
	command.Stdin = strings.NewReader(dashboardScript)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("dashboard JavaScript does not parse: %v\n%s", err, output)
	}
}
