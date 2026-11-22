package engine

import (
	"testing"

	"github.com/lyeith/codexpro/go/internal/workspace"
)

func TestDirectPrincipalPreservesConnectorIdentity(t *testing.T) {
	e, _, _ := auditTargetFixture(t)
	project, _ := e.Workspaces.Project(e.Workspaces.DefaultProject())
	legacyOwner := "connector_" + workspace.Hash(workspace.Hash(project.Root)[:48])[:32]
	for _, mode := range []struct {
		name, policy string
		bridge       bool
		want         string
	}{
		{name: "legacy", want: legacyOwner},
		{name: "bridge", policy: "/test/policy.json", bridge: true, want: legacyOwner},
		{name: "named only", policy: "/test/policy.json"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			e.Config.Set("principalsFile", mode.policy)
			e.Config.Set("legacyBridge", mode.bridge)
			if principal, err := e.PrincipalID(); principal != mode.want || err != nil {
				t.Fatalf("got principal %q, want %q: %v", principal, mode.want, err)
			}
		})
	}
}
