package jobs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// Do not use resultData here: its JSON round trip concealed the raw handler
// boundary that batch consumes and that failed the actual differential replay.
func TestBashRawExitCodeMatchesItsPublicJSONValue(t *testing.T) {
	s, _, resolver := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, code := range []int{0, 7} {
		result, err := s.Call(ctx, wire.Scope{}, "bash", map[string]any{
			"workspace_id": resolver.workspace.ID,
			"command":      fmt.Sprintf("printf raw-boundary; exit %d", code),
			"timeout_ms":   4000, "on_timeout": "kill",
		})
		if err != nil || result.IsError {
			t.Fatalf("Bash outcome failed: %v %#v", err, result)
		}
		if value, ok := result.StructuredContent["exit_code"].(int); !ok || value != code {
			t.Fatalf("raw Bash exit code is not its JSON number: %#v", result.StructuredContent["exit_code"])
		}
		id := wire.String(result.StructuredContent, "job_id", "")
		record, ok := s.Get(id, resolver.workspace.ID)
		if !ok || record.ExitCode == nil || *record.ExitCode != code {
			t.Fatal("normalizing the DTO changed stored command outcome")
		}
		view := s.jobView(record, nil, false)
		if value, ok := view["exit_code"].(int); !ok || value != code {
			t.Fatalf("raw jobs exit code is not its JSON number: %#v", view["exit_code"])
		}
	}
	if got := s.jobView(Record{Status: "running"}, nil, false)["exit_code"]; got != nil {
		t.Fatalf("unfinished job changed JSON null to %#v", got)
	}
}
