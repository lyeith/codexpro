//go:build unix

package plansaccess

import (
	"encoding/json"
	"strings"
	"testing"
)

// The wire names and omitted project_id match plans-access grant-rotation.
// Credentials are unnecessary for validating this shared registration shape.
const rotationPolicyFixtureJSON = `{
	"version": 1,
	"principals": [
		{"id":"alice","type":"person","role":"tester","projects":["example"],"rotation_grants":[
			{"target":"coord","target_principal":"svc","mailbox":"plans/coordinator-codex"},
			{"target":"scoped","target_principal":"svc","mailbox":"example/worker","project_id":"example"},
			{"target":"desk","target_principal":"alice","mailbox":"people/alice/desk"}
		]},
		{"id":"bob","type":"person","role":"owner"},
		{"id":"svc","type":"system","projects":["example","other"]}
	],
	"agents": [
		{"id":"coord","principal_id":"svc","kind":"coordinator","address":"plans/coordinator-codex","mail_aliases":["plans/coordinator"]},
		{"id":"scoped","principal_id":"svc","kind":"leaf","address":"example/worker","project_id":"example"},
		{"id":"desk","principal_id":"alice","kind":"concierge","address":"people/alice/desk"},
		{"id":"crew","principal_id":"alice","kind":"crew","address":"people/alice/crew"},
		{"id":"foreign","principal_id":"svc","kind":"leaf","address":"other/worker","project_id":"other"},
		{"id":"bob-crew","principal_id":"bob","kind":"crew","address":"people/bob/crew"}
	]
}`

func rotationFixture(t *testing.T) *Policy {
	t.Helper()
	p, err := parse([]byte(rotationPolicyFixtureJSON))
	if err != nil {
		t.Fatal("valid Plans Access rotation record made the shared policy unreadable:", err)
	}
	return p
}

func parseRotationPolicy(t *testing.T, p *Policy) (*Policy, error) {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return parse(data)
}

func TestRotationGrantPolicyAcceptsWriterForm(t *testing.T) {
	p := rotationFixture(t)
	if got := p.Principals[0].RotationGrants; len(got) != 3 ||
		got[0] != (RotationGrant{Target: "coord", TargetPrincipal: "svc", Mailbox: "plans/coordinator-codex"}) ||
		got[1].ProjectID != "example" {
		t.Fatal("writer-form grants lost their exact registration or project scope")
	}
	for _, kind := range []string{"coordinator", "supervisor", "leaf"} {
		t.Run("system-"+kind, func(t *testing.T) {
			p := rotationFixture(t)
			p.Agents[0].Kind = kind
			if _, err := parseRotationPolicy(t, p); err != nil {
				t.Fatal("supported registered system role was rejected:", err)
			}
		})
	}
	for _, kind := range []string{"concierge", "crew"} {
		t.Run("personal-"+kind, func(t *testing.T) {
			p := rotationFixture(t)
			p.Agents[2].Kind = kind
			if _, err := parseRotationPolicy(t, p); err != nil {
				t.Fatal("supported own personal role was rejected:", err)
			}
		})
	}
}

func TestRotationGrantPolicyRejectsInvalidBindings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Policy)
	}{
		{"duplicate", func(p *Policy) {
			p.Principals[0].RotationGrants = append(p.Principals[0].RotationGrants, p.Principals[0].RotationGrants[0])
		}},
		{"wildcard", func(p *Policy) { p.Principals[0].RotationGrants[0].Target = "*" }},
		{"unknown-target", func(p *Policy) { p.Principals[0].RotationGrants[0].Target = "missing" }},
		{"unknown-owner", func(p *Policy) { p.Principals[0].RotationGrants[0].TargetPrincipal = "missing" }},
		{"wrong-owner", func(p *Policy) { p.Principals[0].RotationGrants[0].TargetPrincipal = "alice" }},
		{"mailbox-alias", func(p *Policy) { p.Principals[0].RotationGrants[0].Mailbox = "plans/coordinator" }},
		{"wrong-project", func(p *Policy) { p.Principals[0].RotationGrants[0].ProjectID = "example" }},
		{"missing-project", func(p *Policy) { p.Principals[0].RotationGrants[1].ProjectID = "" }},
		{"renamed-registration", func(p *Policy) { p.Agents[0].Address = "plans/new-coordinator" }},
		{"changed-registration-project", func(p *Policy) { p.Agents[0].ProjectID = "example" }},
		{"foreign-person", func(p *Policy) { p.Principals[0].RotationGrants[2] = rotationGrant(p.Agents[5]) }},
		{"foreign-project", func(p *Policy) { p.Principals[0].RotationGrants[0] = rotationGrant(p.Agents[4]) }},
		{"project-no-longer-granted", func(p *Policy) { p.Principals[0].Projects = nil }},
		{"system-delegation", func(p *Policy) { p.Principals[2].RotationGrants = p.Principals[0].RotationGrants[:1] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := rotationFixture(t)
			tc.mutate(p)
			if loaded, err := parseRotationPolicy(t, p); err == nil || loaded != nil {
				t.Fatal("invalid or stale rotation grant was accepted")
			}
		})
	}
}

func TestRotationGrantPolicyKeepsStrictJSON(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement string
	}{
		{"unknown-field", `"target":"coord"`, `"target":"coord","extra":true`},
		{"wrong-case", `"target":"coord"`, `"Target":"coord"`},
		{"duplicate-field", `"target":"coord"`, `"target":"coord","target":"coord"`},
		{"wrong-type", `"target":"coord"`, `"target":42`},
		{"null-project", `"project_id":"example"`, `"project_id":null`},
		{"unknown-principal-field", `"rotation_grants":[`, `"future_grants":[],"rotation_grants":[`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := strings.Replace(rotationPolicyFixtureJSON, tc.old, tc.replacement, 1)
			if changed == rotationPolicyFixtureJSON {
				t.Fatal("fixture mutation did not apply")
			}
			if loaded, err := parse([]byte(changed)); err == nil || loaded != nil {
				t.Fatal("malformed rotation policy JSON was accepted")
			}
		})
	}
}
