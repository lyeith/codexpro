package main

import (
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestActivityBatchDirectAttributionDoesNotResolveOwnership(t *testing.T) {
	project := wire.Project{ID: "fixture", Root: "/private/fixture"}
	for _, id := range []string{"", "arbitrary-display-label"} {
		t.Run(id, func(t *testing.T) {
			ws := activityBatchWorkspace(project, id)
			wantID := id
			if wantID == "" {
				wantID = "activity_fixture"
			}
			if ws.ID != wantID || ws.Root != project.Root || ws.ProjectID != project.ID {
				t.Fatalf("direct attribution changed: ws=%+v", ws)
			}
		})
	}
}
