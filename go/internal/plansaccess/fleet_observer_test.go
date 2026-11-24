//go:build unix

package plansaccess

import (
	"encoding/json"
	"testing"
)

func TestFleetObserverPolicyRecordHasNoCodexProAuthority(t *testing.T) {
	p := fixture(t)
	named, digest := synthetic(36)
	p.Credentials[0].SHA256 = digest
	want, ok := p.Authenticate(named, "codexpro")
	if !ok {
		t.Fatal("existing actor fixture was not admitted")
	}

	observer, digest := synthetic(37)
	valid := Credential{ID: "factory-host-observer-ssd", PrincipalID: "svc", Device: "factory-host",
		Audience: "fleet-observer", Host: "ssd", SHA256: digest}
	p.Credentials = append(p.Credentials, valid)
	parseWith := func(credential Credential) (*Policy, error) {
		t.Helper()
		p.Credentials[len(p.Credentials)-1] = credential
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return parse(data)
	}

	loaded, err := parseWith(valid)
	if err != nil {
		t.Fatal("valid Fleet observer record made the shared policy unreadable:", err)
	}
	if got, ok := loaded.Authenticate(named, "codexpro"); !ok || got != want {
		t.Fatal("adding an observer changed existing named actor access")
	}
	for _, audience := range []string{"codexpro", "agentmail", "agentmail-host", StatusCollectorAudience} {
		if id, admitted, recognized := loaded.AuthenticateWithRecognition(observer, audience); admitted || !recognized || id != (Identity{}) {
			t.Fatalf("unexpected bearer admission or ownership for %q", audience)
		}
		if id, admitted := loaded.ActiveCredential(valid.ID, audience); admitted || id != (Identity{}) {
			t.Fatalf("cached identity admitted for %q", audience)
		}
	}
	observerID, admitted, recognized := loaded.AuthenticateWithRecognition(observer, FleetObserverAudience)
	if !admitted || !recognized || observerID.CredentialID != valid.ID || observerID.PrincipalID != valid.PrincipalID || observerID.Host != valid.Host || observerID.Audience != FleetObserverAudience || observerID.AgentID != "" {
		t.Fatal("shared policy did not admit the observer for its own audience")
	}
	if id, admitted := loaded.ActiveCredential(valid.ID, FleetObserverAudience); !admitted || id != observerID {
		t.Fatal("shared policy did not preserve the active observer binding")
	}

	for name, change := range map[string]func(*Credential){
		"missing host":    func(c *Credential) { c.Host = "" },
		"invalid host":    func(c *Credential) { c.Host = "bad/host" },
		"agent binding":   func(c *Credential) { c.AgentID = p.Agents[0].ID },
		"personal owner":  func(c *Credential) { c.PrincipalID = "alice" },
		"missing owner":   func(c *Credential) { c.PrincipalID = "unknown" },
		"imported bearer": func(c *Credential) { c.Format = ImportedFleetV1 },
		"duplicate hash":  func(c *Credential) { c.SHA256 = p.Credentials[0].SHA256 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid
			change(&bad)
			if loaded, err := parseWith(bad); err == nil || loaded != nil {
				t.Fatal("malformed Fleet observer record accepted")
			}
		})
	}
}
