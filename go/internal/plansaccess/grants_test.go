//go:build unix

package plansaccess

import "testing"

func TestStoredCredentialRevalidation(t *testing.T) {
	p := fixture(t)
	if id, ok := p.ActiveCredential("device-1", "agentmail"); !ok || id.PrincipalID != "alice" || id.AgentID != "crew-alice" {
		t.Fatal("stored origin did not resolve")
	}
	for _, aud := range []string{"both", "agentmail-host", "unknown"} {
		if _, ok := p.ActiveCredential("device-1", aud); ok {
			t.Fatal("stored person accepted invalid audience")
		}
	}
	p.Credentials[0].Revoked = true
	if _, ok := p.ActiveCredential("device-1", "agentmail"); ok {
		t.Fatal("revoked stored credential")
	}
	if _, ok := p.ActiveCredential("device-2", "agentmail"); !ok {
		t.Fatal("unrelated credential unavailable")
	}
}

func TestExplicitTargetAndProjectGrants(t *testing.T) {
	p := fixture(t)
	p.Principals[0].Role = "product"
	p.Principals[0].Delegations = []ProjectGrant{{ProjectID: "example", DocsPaths: []string{"docs/research"}, DecisionAreas: []string{"roadmap"}}}
	manager := Agent{ID: "desk", PrincipalID: "alice", Kind: "concierge", Address: "people/alice/desk", Grants: []Grant{{Action: "resume", Target: "crew-alice"}}}
	p.Agents = append(p.Agents, manager)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if !manager.Allows("resume", "crew-alice") || manager.Allows("stop", "crew-alice") || manager.Allows("resume", "other") {
		t.Fatal("action/target pair mismatch")
	}
	if paths := p.Principals[0].DocsFor("other"); len(paths) != 0 {
		t.Fatal("cross-project docs permission")
	}
	if paths := p.Principals[0].DocsFor("example"); len(paths) != 1 || paths[0] != "docs/research" {
		t.Fatal("missing docs permission")
	}
	p.Agents[2].Grants = []Grant{{Action: "start", Target: "leaf-worker"}}
	if p.Validate() == nil {
		t.Fatal("cross-system personal lifecycle delegation")
	}
	p.Agents[2].Grants = nil
	p.Agents[0].Grants = []Grant{{Action: "start", Target: "crew-alice"}}
	if p.Validate() == nil {
		t.Fatal("crew cannot gain start permissions")
	}
	p.Agents[0].Grants = nil
	for _, path := range []string{"docs", "docs/", "docs/../other", "docs/./research", "docs/research/", "docs/.git/config", "docs/*", "docs/a\\b", "docs/test\nfile"} {
		p.Principals[0].Delegations[0].DocsPaths = []string{path}
		if p.Validate() == nil {
			t.Fatalf("accepted invalid docs path %q", path)
		}
	}
	p.Principals[0].Delegations = nil
	p.Agents[0].NativeID = "native-id"
	p.Agents[2].NativeID = "native-id"
	p.Agents[2].Host = "alice-lab"
	if p.Validate() == nil {
		t.Fatal("native binding collision")
	}
	p.Agents[2].Host = "other-lab"
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}
