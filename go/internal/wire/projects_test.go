package wire

import (
	"context"
	"testing"
)

func TestRestrictedIdentitiesAndRequestGrants(t *testing.T) {
	for _, id := range []Identity{
		{Source: "named", PrincipalType: "person", Role: "owner", AgentKind: "concierge"},
		{Source: "named", PrincipalType: "person", Role: "owner", AgentKind: "crew"},
		{Source: "named", PrincipalType: "system", AgentKind: "unregistered"},
		{Source: "named", PrincipalType: "person", AgentKind: "coordinator"},
		{Source: "unverified"},
		{Source: "legacy", PrincipalID: "untrusted"},
	} {
		if !id.Restricted() {
			t.Fatalf("unexpected operator access for %+v", id)
		}
	}
	for _, kind := range []string{"coordinator", "supervisor", "leaf"} {
		id := Identity{Source: "named", PrincipalType: "system", PrincipalID: "service", AgentID: "registered", CredentialID: "device", AgentKind: kind}
		if id.Restricted() {
			t.Fatalf("trusted system %s is restricted", kind)
		}
	}
	projects := []string{"fixture"}
	ctx := WithProjects(context.Background(), projects)
	projects[0] = "foreign"
	ProjectsFrom(ctx)[0] = "foreign"
	if ProjectsFrom(ctx)[0] != "fixture" || len(ProjectsFrom(context.Background())) != 0 {
		t.Fatal("request grants were shared or appeared without a policy")
	}
}
