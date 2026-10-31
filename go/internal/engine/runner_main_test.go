package engine

import (
	"os"
	"testing"

	"github.com/lyeith/codexpro/go/internal/process"
)

// Jobs executes the current binary as its detached runner. Native engine
// integration tests must preserve the production executable's runner dispatch.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "runner" {
		if err := process.RunRunner(os.Args[2:]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
