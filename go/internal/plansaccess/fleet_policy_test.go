package plansaccess

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestFleetImportTokenGrammar(t *testing.T) {
	base := strings.Repeat("x", 32)
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"minimum", base, true},
		{"maximum", strings.Repeat("z", 4096), true},
		{"alphabet and padding", base + "AZ79-._~+/==", true},
		{"short", base[1:], false},
		{"oversize", strings.Repeat("x", 4097), false},
		{"reserved", "pa1_" + base, false},
		{"whitespace", base + " ", false},
		{"file ending", base + "\r\n", false},
		{"extra line", base + "\n" + base, false},
		{"control", base + "\x00", false},
		{"non ASCII", base + "é", false},
		{"other punctuation", base + "!", false},
		{"interior padding", base + "=a", false},
		{"only padding", strings.Repeat("=", 32), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ValidFleetImportToken(tc.raw) != tc.want {
				t.Fatal("unexpected bearer grammar decision")
			}
		})
	}
}

func fleetPolicyForTest(raw string) Policy {
	sum := sha256.Sum256([]byte(raw))
	return Policy{
		Version:     1,
		Principals:  []Principal{{ID: "fleet", Type: "system"}},
		Agents:      []Agent{{ID: "sp-fleet", PrincipalID: "fleet", Kind: "leaf", Address: "sp/fleet"}},
		Credentials: []Credential{{ID: "fleet-connector", Device: "connector", PrincipalID: "fleet", AgentID: "sp-fleet", Audience: "both", Format: ImportedFleetV1, SHA256: hex.EncodeToString(sum[:])}},
	}
}

func TestFleetCredentialRecognitionAndBinding(t *testing.T) {
	raw := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name     string
		change   func(*Policy)
		audience string
		admit    bool
	}{
		{"codexpro", nil, "codexpro", true},
		{"agentmail", nil, "agentmail", true},
		{"host", nil, "agentmail-host", false},
		{"revoked", func(p *Policy) { p.Credentials[0].Revoked = true }, "codexpro", false},
		{"disabled principal", func(p *Policy) { p.Principals[0].Disabled = true }, "agentmail", false},
		{"retired agent", func(p *Policy) { p.Agents[0].Disabled = true }, "agentmail", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := fleetPolicyForTest(raw)
			if tc.change != nil {
				tc.change(&p)
			}
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := parse(b)
			if err != nil {
				t.Fatal(err)
			}
			id, ok, known := loaded.AuthenticateWithRecognition(raw, tc.audience)
			if !known || ok != tc.admit {
				t.Fatal("wrong credential recognition or admission")
			}
			if ok && (id.PrincipalType != "system" || id.PrincipalID != "fleet" || id.AgentKind != "leaf" || id.AgentID != "sp-fleet" || id.Address != "sp/fleet" || id.Role != "" || id.CredentialID != "fleet-connector") {
				t.Fatal("incorrect fleet identity")
			}
			if !ok && id != (Identity{}) {
				t.Fatal("refusal exposed an identity")
			}
		})
	}

	p := fleetPolicyForTest(raw)
	for _, tc := range []struct {
		raw   string
		known bool
	}{
		{strings.Repeat("b", 40), false}, {"pa1_", true}, {"pa1_" + strings.Repeat("A", 43), true},
	} {
		if _, ok, known := p.AuthenticateWithRecognition(tc.raw, "codexpro"); ok || known != tc.known {
			t.Fatal("incorrect unknown or reserved credential disposition")
		}
	}
	// A matched digest cannot borrow the other credential format.
	p.Credentials[0].Format = ""
	if _, ok, known := p.AuthenticateWithRecognition(raw, "agentmail"); ok || !known {
		t.Fatal("format mismatch fell through")
	}
}

func TestFleetCredentialSchemaAndTombstones(t *testing.T) {
	raw := strings.Repeat("a", 40)
	for name, change := range map[string]func(*Policy){
		"unknown format": func(p *Policy) { p.Credentials[0].Format = "future" },
		"personal": func(p *Policy) {
			p.Principals[0].Type = "person"
			p.Principals[0].Role = "owner"
			p.Agents[0].Kind = "crew"
			p.Agents[0].Address = "people/fleet/crew"
		},
		"system coordinator": func(p *Policy) { p.Agents[0].Kind = "coordinator" },
		"one service":        func(p *Policy) { p.Credentials[0].Audience = "agentmail" },
		"host": func(p *Policy) {
			c := &p.Credentials[0]
			c.Audience = "agentmail-host"
			c.AgentID = ""
			c.Host = "test-host"
		},
		"missing registration": func(p *Policy) { p.Credentials[0].AgentID = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			p := fleetPolicyForTest(raw)
			change(&p)
			if p.Validate() == nil {
				t.Fatal("invalid fleet binding accepted")
			}
		})
	}

	p := fleetPolicyForTest(raw)
	nextRaw := strings.Repeat("b", 40)
	next := fleetPolicyForTest(nextRaw).Credentials[0]
	next.ID = "fleet-next"
	p.Credentials = append(p.Credentials, next)
	if p.Validate() == nil {
		t.Fatal("multiple active fleet bindings")
	}
	p.Credentials[0].Revoked = true
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, known := p.AuthenticateWithRecognition(raw, "agentmail"); ok || !known {
		t.Fatal("revoked fleet binding not retained")
	}
	if _, ok, known := p.AuthenticateWithRecognition(nextRaw, "codexpro"); !ok || !known {
		t.Fatal("next fleet binding refused")
	}
	p.Credentials[1].SHA256 = p.Credentials[0].SHA256
	if p.Validate() == nil {
		t.Fatal("duplicate tombstoned digest permitted")
	}
}
