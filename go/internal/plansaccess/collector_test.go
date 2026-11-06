//go:build unix

package plansaccess

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func collectorPolicy(t *testing.T) (*Policy, string) {
	t.Helper()
	raw := "pa1_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{37}, 32))
	digest := sha256.Sum256([]byte(raw))
	p := &Policy{Version: 1,
		Principals: []Principal{{ID: "david", Type: "person", Role: "owner"}},
		Agents:     []Agent{{ID: "concierge", PrincipalID: "david", Kind: "concierge", Address: "people/david/concierge"}},
		Credentials: []Credential{{ID: "collector", PrincipalID: "david", AgentID: "concierge", Device: "fixture",
			Audience: StatusCollectorAudience, SHA256: hex.EncodeToString(digest[:])}}}
	return p, raw
}

func TestCollectorAudienceIsSeparateFromEveryOrdinaryAudience(t *testing.T) {
	p, raw := collectorPolicy(t)
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	p, err = parse(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, audience := range []string{StatusCollectorAudience, "codexpro", "agentmail", "agentmail-host", "both", "unknown"} {
		id, admitted, recognized := p.AuthenticateWithRecognition(raw, audience)
		want := audience == StatusCollectorAudience
		if admitted != want || !recognized {
			t.Fatalf("audience %s admitted=%v recognized=%v", audience, admitted, recognized)
		}
		if want && (id.PrincipalType != "person" || id.Role != "owner" || id.AgentKind != "concierge" || id.Audience != StatusCollectorAudience) {
			t.Fatal("collector lost its bound owner identity")
		}
	}
	p.Credentials[0].Audience = "both"
	for _, audience := range []string{"codexpro", "agentmail", StatusCollectorAudience, "agentmail-host"} {
		_, admitted, recognized := p.AuthenticateWithRecognition(raw, audience)
		want := audience == "codexpro" || audience == "agentmail"
		if admitted != want || !recognized {
			t.Fatalf("both changed authority for %s", audience)
		}
	}
}

func TestCollectorBindingRechecksRevocationRoleAndAgent(t *testing.T) {
	cases := map[string]func(*Policy){
		"revoked":            func(p *Policy) { p.Credentials[0].Revoked = true },
		"disabled person":    func(p *Policy) { p.Principals[0].Disabled = true },
		"disabled concierge": func(p *Policy) { p.Agents[0].Disabled = true },
		"demoted person":     func(p *Policy) { p.Principals[0].Role = "engineer" },
		"crew":               func(p *Policy) { p.Agents[0].Kind = "crew" },
		"missing binding":    func(p *Policy) { p.Credentials[0].AgentID = "" },
		"foreign binding":    func(p *Policy) { p.Agents[0].PrincipalID = "another" },
		"system leaf":        func(p *Policy) { p.Principals[0].Type = "system"; p.Principals[0].Role = ""; p.Agents[0].Kind = "leaf" },
		"host":               func(p *Policy) { p.Credentials[0].Host = "fixture-host" },
		"imported fleet":     func(p *Policy) { p.Credentials[0].Format = ImportedFleetV1 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p, raw := collectorPolicy(t)
			change(p)
			if _, admitted, recognized := p.AuthenticateWithRecognition(raw, StatusCollectorAudience); admitted || !recognized {
				t.Fatal("denied collector could fall through to another authority")
			}
			if _, admitted := p.ActiveCredential("collector", StatusCollectorAudience); admitted {
				t.Fatal("cached origin retained collector authority")
			}
		})
	}
	// Revocation and role changes must remain writable without deleting the
	// collector registration or invalidating otherwise valid ordinary records.
	for _, change := range []func(*Policy){
		func(p *Policy) { p.Principals[0].Role = "engineer" },
		func(p *Policy) { p.Agents[0].Disabled = true },
		func(p *Policy) { p.Credentials[0].Revoked = true },
	} {
		p, _ := collectorPolicy(t)
		change(p)
		if err := p.Validate(); err != nil {
			t.Fatal("safe authority reduction made policy unwritable")
		}
	}
}

func TestCollectorRejectsStructuralEscapesAndMalformedGeneratedTokens(t *testing.T) {
	for _, change := range []func(*Policy){
		func(p *Policy) { p.Credentials[0].Format = ImportedFleetV1 },
		func(p *Policy) { p.Credentials[0].AgentID = "" },
		func(p *Policy) { p.Credentials[0].Host = "host" },
		func(p *Policy) {
			p.Principals[0].Type = "system"
			p.Principals[0].Role = ""
			p.Agents[0].Kind = "leaf"
			p.Agents[0].Address = "plans/fleet"
		},
	} {
		p, _ := collectorPolicy(t)
		change(p)
		if p.Validate() == nil {
			t.Fatal("invalid collector shape accepted")
		}
	}
	p, raw := collectorPolicy(t)
	for _, bad := range []string{"pa1_", raw + "=", raw[:46] + "!", raw + "\n"} {
		digest := sha256.Sum256([]byte(bad))
		p.Credentials[0].SHA256 = hex.EncodeToString(digest[:])
		if _, admitted, recognized := p.AuthenticateWithRecognition(bad, StatusCollectorAudience); admitted || !recognized {
			t.Fatal("collector bypassed generated token grammar")
		}
	}
}
