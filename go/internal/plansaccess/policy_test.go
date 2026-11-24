//go:build unix

package plansaccess

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func readFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/v1.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func fixture(t *testing.T) *Policy {
	t.Helper()
	p, err := parse(readFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func synthetic(n byte) (string, string) {
	b := bytes.Repeat([]byte{n}, 32)
	raw := "pa1_" + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(raw))
	return raw, hex.EncodeToString(sum[:])
}
func TestAuthenticationAndReload(t *testing.T) {
	p := fixture(t)
	var tokens [3]string
	for i := range tokens {
		tokens[i], p.Credentials[i].SHA256 = synthetic(byte(i))
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "policy.json")
	save := func() {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(file, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	authorize := func(i int, s string) (Identity, bool) {
		loaded, err := Load(file)
		if err != nil {
			t.Fatal(err)
		}
		return loaded.Authenticate(tokens[i], s)
	}
	save()
	who, ok := authorize(0, "codexpro")
	if !ok || who.PrincipalID != "alice" || who.AgentKind != "crew" || who.Address != "people/alice/crew" || who.Device != "lab" || who.CredentialID != "device-1" || who.Role != "tester" {
		t.Fatal("missing bound identity")
	}
	if _, ok := authorize(0, "agentmail-host"); ok {
		t.Fatal("person host impersonation")
	}
	if _, ok := authorize(1, "codexpro"); ok {
		t.Fatal("cross-service credential")
	}
	if _, ok := authorize(2, "agentmail"); ok {
		t.Fatal("callback used as person")
	}
	host, ok := authorize(2, "agentmail-host")
	if !ok || host.Host != "lab-worker" || host.AgentID != "" {
		t.Fatal("host binding lost")
	}
	for _, raw := range []string{"pa1_", tokens[0][:46] + "!", tokens[0] + "="} {
		if !IsNamedToken(raw) {
			t.Fatal("named prefix lost")
		}
		if _, ok := p.Authenticate(raw, "codexpro"); ok {
			t.Fatal("malformed credential accepted")
		}
	}
	p.Credentials[0].Revoked = true
	p.Principals[0].Role = "engineer"
	save()
	if _, ok := authorize(0, "agentmail"); ok {
		t.Fatal("revoked device")
	}
	second, ok := authorize(1, "agentmail")
	if !ok || second.Role != "engineer" {
		t.Fatal("independent device/current role")
	}
	p.Agents[0].Disabled = true
	save()
	if _, ok := authorize(1, "agentmail"); ok {
		t.Fatal("retired agent")
	}
	p.Agents[0].Disabled = false
	p.Principals[0].Disabled = true
	save()
	if _, ok := authorize(1, "agentmail"); ok {
		t.Fatal("disabled person")
	}
	if err := os.WriteFile(file, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if prior, err := Load(file); err == nil || prior != nil {
		t.Fatal("malformed replacement returned policy")
	}
}
func TestRejectAmbiguousOrInvalidPolicy(t *testing.T) {
	for _, bad := range []string{
		`{"version":1,"version":1}`, `{"Version":1}`,
		`{"version":1}{}`, `{"version":1,"credentials":null}`, `{"version":2}`,
		`{"verſion":1}`,
	} {
		if _, err := parse([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	cases := map[string]func(*Policy){
		"duplicate owner":     func(p *Policy) { p.Principals = append(p.Principals, p.Principals[0]) },
		"duplicate agent":     func(p *Policy) { p.Agents[1].ID = p.Agents[0].ID },
		"duplicate address":   func(p *Policy) { p.Agents[1].Address = p.Agents[0].Address },
		"duplicate device id": func(p *Policy) { p.Credentials[1].ID = p.Credentials[0].ID },
		"duplicate digest":    func(p *Policy) { p.Credentials[1].SHA256 = p.Credentials[0].SHA256 },
		"wrong owner":         func(p *Policy) { p.Credentials[1].PrincipalID = "svc" },
		"system role":         func(p *Policy) { p.Principals[1].Role = "owner" },
		"person elevated":     func(p *Policy) { p.Agents[0].Kind = "coordinator" },
		"reserved id":         func(p *Policy) { p.Principals[1].ID = "unattributed" },
		"reserved sender":     func(p *Policy) { p.Agents[1].Address = "agentmail" },
		"cross mailbox":       func(p *Policy) { p.Agents[0].Address = "people/other/crew" },
		"bad project":         func(p *Policy) { p.Agents[0].ProjectID = "elsewhere" },
		"credential audience": func(p *Policy) { p.Credentials[0].Audience = "any" },
		"personal callback":   func(p *Policy) { p.Credentials[2].PrincipalID = "alice" },
	}
	for n, m := range cases {
		t.Run(n, func(t *testing.T) {
			p := fixture(t)
			m(p)
			if p.Validate() == nil {
				t.Fatal("accepted invalid policy")
			}
		})
	}
}
func TestPrivatePolicyPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(f, readFixture(t), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "file-link", "fifo", "linked-dir/policy.json"} {
		switch name {
		case "file-link":
			if err := os.Symlink(f, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		case "fifo":
			if err := unix.Mkfifo(filepath.Join(dir, name), 0600); err != nil {
				t.Fatal(err)
			}
		case "linked-dir/policy.json":
			if err := os.Symlink(dir, filepath.Join(dir, "linked-dir")); err != nil {
				t.Fatal(err)
			}
		}
		if p, err := Load(filepath.Join(dir, name)); p != nil || err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	if err := os.Chmod(f, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(f); err == nil {
		t.Fatal("accepted group-readable file")
	}
	if err := os.Chmod(f, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(f); err == nil {
		t.Fatal("accepted writable parent")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(strings.Repeat("x", maxSize+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(f); err == nil {
		t.Fatal("accepted oversized file")
	}
}
