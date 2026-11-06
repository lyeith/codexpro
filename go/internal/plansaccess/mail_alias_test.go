//go:build unix

package plansaccess

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func mailAliasPolicy() (*Policy, string) {
	raw, digest := synthetic(31)
	return &Policy{
		Version: 1,
		Principals: []Principal{
			{ID: "plans", Type: "system"},
			{ID: "alice", Type: "person", Role: "engineer"},
		},
		Agents: []Agent{
			{ID: "coordinator", PrincipalID: "plans", Kind: "coordinator",
				Address: "plans/coordinator-codex", MailAliases: []string{"plans/coordinator"},
				Host: "fixture", NativeID: "native-coordinator"},
			{ID: "leaf", PrincipalID: "plans", Kind: "leaf", Address: "plans/leaf"},
			{ID: "crew", PrincipalID: "alice", Kind: "crew", Address: "people/alice/crew",
				MailAliases: []string{"people/alice/previous-crew"}},
		},
		Credentials: []Credential{{ID: "coordinator-device", Device: "fixture",
			PrincipalID: "plans", AgentID: "coordinator", Audience: "both", SHA256: digest}},
	}, raw
}

func TestMailAliasesKeepOneCanonicalIdentityAndNoLifecycleLookup(t *testing.T) {
	p, raw := mailAliasPolicy()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	p, err = parse(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"plans/coordinator-codex", "plans/coordinator"} {
		agent, ok := p.MailRecipientAt(address)
		if !ok || agent.ID != "coordinator" || agent.Kind != "coordinator" ||
			agent.Address != "plans/coordinator-codex" || agent.NativeID != "native-coordinator" {
			t.Fatal("recipient did not resolve to the original registration", address)
		}
		if agent.Allows("bind", address) || agent.Allows("start", address) {
			t.Fatal("mail alias conferred a lifecycle grant")
		}
	}
	if _, ok := p.AgentAt("plans/coordinator"); ok {
		t.Fatal("mail alias entered canonical identity/lifecycle lookup")
	}
	if _, ok := p.MailRecipientAt("plans/unregistered"); ok {
		t.Fatal("unregistered recipient resolved")
	}
	if agent, ok := p.MailRecipientAt("people/alice/previous-crew"); !ok || agent.ID != "crew" {
		t.Fatal("same-owner person alias did not resolve")
	}
	for _, audience := range []string{"agentmail", "codexpro"} {
		id, ok := p.Authenticate(raw, audience)
		if !ok || id.Address != "plans/coordinator-codex" || id.AgentID != "coordinator" {
			t.Fatal("alias changed authenticated canonical identity", audience)
		}
		p.Agents[0].MailAliases = nil
		withoutAliases, admitted := p.Authenticate(raw, audience)
		if !admitted || id != withoutAliases {
			t.Fatal("alias changed authentication or project authority")
		}
		p.Agents[0].MailAliases = []string{"plans/coordinator"}
	}
	if len(p.Agents) != 3 || len(p.Credentials) != 1 {
		t.Fatal("alias created a second identity or credential")
	}
	if got := p.Agents[0].MailAddresses(); !reflect.DeepEqual(got, []string{"plans/coordinator-codex", "plans/coordinator"}) {
		t.Fatal("wrong owned recipients", got)
	}
}

func TestMailAliasesRejectNamespaceAndPolicyWideCollisions(t *testing.T) {
	cases := map[string]func(*Policy){
		"own canonical":   func(p *Policy) { p.Agents[0].MailAliases = []string{p.Agents[0].Address} },
		"repeat alias":    func(p *Policy) { p.Agents[0].MailAliases = []string{"plans/old", "plans/old"} },
		"other canonical": func(p *Policy) { p.Agents[0].MailAliases = []string{"plans/leaf"} },
		"other alias": func(p *Policy) {
			p.Agents[0].MailAliases = []string{"plans/old"}
			p.Agents[1].MailAliases = []string{"plans/old"}
		},
		"disabled reservation": func(p *Policy) {
			p.Agents[1].Disabled = true
			p.Agents[1].MailAliases = []string{"plans/coordinator"}
		},
		"system in people":   func(p *Policy) { p.Agents[0].MailAliases = []string{"people/alice/other"} },
		"system people root": func(p *Policy) { p.Agents[0].MailAliases = []string{"people"} },
		"foreign person":     func(p *Policy) { p.Agents[2].MailAliases = []string{"people/bob/crew"} },
		"person in system":   func(p *Policy) { p.Agents[2].MailAliases = []string{"plans/crew"} },
		"external":           func(p *Policy) { p.Agents[0].MailAliases = []string{"external"} },
		"agentmail":          func(p *Policy) { p.Agents[0].MailAliases = []string{"agentmail"} },
		"empty":              func(p *Policy) { p.Agents[0].MailAliases = []string{""} },
		"empty component":    func(p *Policy) { p.Agents[0].MailAliases = []string{"plans//old"} },
		"trailing slash":     func(p *Policy) { p.Agents[0].MailAliases = []string{"plans/old/"} },
		"uppercase":          func(p *Policy) { p.Agents[0].MailAliases = []string{"plans/Old"} },
		"overlong":           func(p *Policy) { p.Agents[0].MailAliases = []string{strings.Repeat("a/", 128) + "a"} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p, _ := mailAliasPolicy()
			change(p)
			data, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parse(data); err == nil {
				t.Fatal("invalid alias policy admitted")
			}
		})
	}
	p, _ := mailAliasPolicy()
	data, _ := json.Marshal(p)
	valid := []byte(`"mail_aliases":["plans/coordinator"]`)
	for _, invalid := range []string{
		`"mail_aliases":null`, `"mail_aliases":"plans/coordinator"`,
		`"mail_aliases":[1]`, `"mail_alias":["plans/coordinator"]`,
		`"mail_aliases":[],"mail_aliases":["plans/coordinator"]`,
	} {
		if _, err := parse(bytes.Replace(data, valid, []byte(invalid), 1)); err == nil {
			t.Fatal("ambiguous or malformed alias field admitted")
		}
	}
}

func TestMailAliasesRespectCurrentOwnerAgentAndCredentialDenials(t *testing.T) {
	p, raw := mailAliasPolicy()
	for _, disable := range []func(){
		func() { p.Agents[0].Disabled = true },
		func() { p.Agents[0].Disabled = false; p.Principals[0].Disabled = true },
	} {
		disable()
		for _, address := range []string{"plans/coordinator-codex", "plans/coordinator"} {
			if _, ok := p.MailRecipientAt(address); ok {
				t.Fatal("disabled owner or agent retained a mail recipient")
			}
		}
		if _, admitted, recognized := p.AuthenticateWithRecognition(raw, "agentmail"); admitted || !recognized {
			t.Fatal("alias bypassed a recognized credential denial")
		}
	}
	p.Principals[0].Disabled = false
	p.Credentials[0].Revoked = true
	if _, admitted, recognized := p.AuthenticateWithRecognition(raw, "agentmail"); admitted || !recognized {
		t.Fatal("alias bypassed a revoked credential")
	}
	// Revoking one device does not retire the recipient registration.
	if _, ok := p.MailRecipientAt("plans/coordinator"); !ok {
		t.Fatal("device revocation incorrectly erased the enabled recipient")
	}
}
