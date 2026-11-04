// Package plansaccess reads the v1 principal and credential policy.
package plansaccess

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxSize = 1 << 20

var errUnavailable = errors.New("access policy unavailable")
var errInvalid = errors.New("invalid access policy")
var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var fieldRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type Policy struct {
	Version     int          `json:"version"`
	Principals  []Principal  `json:"principals,omitempty"`
	Agents      []Agent      `json:"agents,omitempty"`
	Credentials []Credential `json:"credentials,omitempty"`
}

type Principal struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Role        string         `json:"role,omitempty"`
	Projects    []string       `json:"projects,omitempty"`
	Delegations []ProjectGrant `json:"delegations,omitempty"`
	Disabled    bool           `json:"disabled,omitempty"`
}

type Agent struct {
	ID          string  `json:"id"`
	PrincipalID string  `json:"principal_id"`
	Kind        string  `json:"kind"`
	Address     string  `json:"address"`
	Host        string  `json:"host,omitempty"`
	NativeID    string  `json:"native_id,omitempty"`
	Repo        string  `json:"repo,omitempty"`
	ProjectID   string  `json:"project_id,omitempty"`
	Grants      []Grant `json:"grants,omitempty"`
	Disabled    bool    `json:"disabled,omitempty"`
}

type Credential struct {
	ID          string `json:"id"`
	Device      string `json:"device"`
	PrincipalID string `json:"principal_id"`
	AgentID     string `json:"agent_id,omitempty"`
	Audience    string `json:"audience"`
	Host        string `json:"host,omitempty"`
	SHA256      string `json:"sha256"`
	Revoked     bool   `json:"revoked,omitempty"`
}

// Identity only contains attributable metadata. It never retains bearer material.
type Identity struct {
	PrincipalID, PrincipalType, Role  string
	CredentialID, Device, Audience    string
	AgentID, AgentKind, Address, Host string
}

func (p *Policy) Principal(id string) (Principal, bool) {
	for _, v := range p.Principals {
		if v.ID == id {
			return v, true
		}
	}
	return Principal{}, false
}
func (p *Policy) Agent(id string) (Agent, bool) {
	for _, v := range p.Agents {
		if v.ID == id {
			return v, true
		}
	}
	return Agent{}, false
}
func (p *Policy) AgentAt(address string) (Agent, bool) {
	for _, v := range p.Agents {
		if v.Address == address {
			return v, true
		}
	}
	return Agent{}, false
}

// IsNamedToken reserves the full token family, including malformed or revoked
// presentations. A miss in this family must never fall back to another lane.
func IsNamedToken(raw string) bool { return strings.HasPrefix(raw, "pa1_") }
func validNamedToken(raw string) bool {
	if !IsNamedToken(raw) || len(raw) != 47 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw[4:])
	return err == nil && len(decoded) == 32
}
func (p *Policy) Authenticate(raw, audience string) (Identity, bool) {
	if !validNamedToken(raw) {
		return Identity{}, false
	}
	digest := sha256.Sum256([]byte(raw))
	hexDigest := hex.EncodeToString(digest[:])
	for _, c := range p.Credentials {
		if subtle.ConstantTimeCompare([]byte(c.SHA256), []byte(hexDigest)) == 1 {
			return p.binding(c, audience)
		}
	}
	return Identity{}, false
}

// ActiveCredential rechecks a previously authenticated, server-persisted
// origin before deferred work. A client-supplied ID is never authentication.
func (p *Policy) ActiveCredential(id, audience string) (Identity, bool) {
	for _, c := range p.Credentials {
		if c.ID == id {
			return p.binding(c, audience)
		}
	}
	return Identity{}, false
}

func (p *Policy) binding(c Credential, audience string) (Identity, bool) {
	if c.Revoked || !slices.Contains([]string{"codexpro", "agentmail", "agentmail-host"}, audience) {
		return Identity{}, false
	}
	if c.Audience != audience && !(c.Audience == "both" && audience != "agentmail-host") {
		return Identity{}, false
	}
	owner, ok := p.Principal(c.PrincipalID)
	if !ok || owner.Disabled {
		return Identity{}, false
	}
	result := Identity{PrincipalID: owner.ID, PrincipalType: owner.Type, Role: owner.Role, CredentialID: c.ID, Device: c.Device, Audience: c.Audience, Host: c.Host}
	if c.Audience != "agentmail-host" {
		a, ok := p.Agent(c.AgentID)
		if !ok || a.Disabled || a.PrincipalID != owner.ID {
			return Identity{}, false
		}
		result.AgentID, result.AgentKind, result.Address, result.Host = a.ID, a.Kind, a.Address, a.Host
	}
	return result, true
}

func unique(m map[string]bool, value string) bool {
	if m[value] {
		return false
	}
	m[value] = true
	return true
}
func reserved(id string) bool {
	for _, prefix := range []string{"legacy", "unattributed"} {
		if id == prefix || strings.HasPrefix(id, prefix+"-") || strings.HasPrefix(id, prefix+".") || strings.HasPrefix(id, prefix+"_") {
			return true
		}
	}
	return false
}
func (p *Policy) Validate() error {
	if p.Version != 1 {
		return errInvalid
	}
	owners := make(map[string]Principal, len(p.Principals))
	agents := make(map[string]Agent, len(p.Agents))
	addresses, ids, digests := map[string]bool{}, map[string]bool{}, map[string]bool{}
	native := map[string]bool{}
	for _, v := range p.Principals {
		if _, ok := owners[v.ID]; ok || !idRE.MatchString(v.ID) || reserved(v.ID) {
			return errInvalid
		}
		switch v.Type {
		case "person":
			if !slices.Contains([]string{"owner", "engineer", "product", "tester"}, v.Role) {
				return errInvalid
			}
		case "system":
			if v.Role != "" {
				return errInvalid
			}
		default:
			return errInvalid
		}
		projects := map[string]bool{}
		for _, project := range v.Projects {
			if !idRE.MatchString(project) || !unique(projects, project) {
				return errInvalid
			}
		}
		if err := v.validateDelegations(projects); err != nil {
			return err
		}
		owners[v.ID] = v
	}
	for _, a := range p.Agents {
		o, ok := owners[a.PrincipalID]
		if !ok || !idRE.MatchString(a.ID) || reserved(a.ID) || !unique(addresses, a.Address) {
			return errInvalid
		}
		if _, exists := agents[a.ID]; exists {
			return errInvalid
		}
		if a.Host != "" && !idRE.MatchString(a.Host) || a.ProjectID != "" && !slices.Contains(o.Projects, a.ProjectID) {
			return errInvalid
		}
		if a.NativeID != "" && (a.Host == "" || len(a.NativeID) > 256 || strings.IndexFunc(a.NativeID, unicode.IsControl) >= 0 || !unique(native, a.Host+"\x00"+a.NativeID)) {
			return errInvalid
		}
		if a.Repo != "" && (!path.IsAbs(a.Repo) || path.Clean(a.Repo) != a.Repo || a.Repo == "/" || strings.ContainsAny(a.Repo, "\x00\r\n")) {
			return errInvalid
		}
		if len(a.Address) > 256 || a.Address == "agentmail" || a.Address == "external" {
			return errInvalid
		}
		for _, s := range strings.Split(a.Address, "/") {
			if !idRE.MatchString(s) {
				return errInvalid
			}
		}
		if o.Type == "person" {
			if (a.Kind != "concierge" && a.Kind != "crew") || !strings.HasPrefix(a.Address, "people/"+o.ID+"/") {
				return errInvalid
			}
		} else if !slices.Contains([]string{"coordinator", "supervisor", "leaf"}, a.Kind) || a.Address == "people" || strings.HasPrefix(a.Address, "people/") {
			return errInvalid
		}
		agents[a.ID] = a
	}
	for _, a := range agents {
		if err := a.validateGrants(agents); err != nil {
			return err
		}
	}
	for _, c := range p.Credentials {
		o, ok := owners[c.PrincipalID]
		if !ok || !idRE.MatchString(c.ID) || reserved(c.ID) || !unique(ids, c.ID) || !idRE.MatchString(c.Device) || !digestRE.MatchString(c.SHA256) || !unique(digests, c.SHA256) {
			return errInvalid
		}
		switch c.Audience {
		case "agentmail-host":
			if o.Type != "system" || c.AgentID != "" || !idRE.MatchString(c.Host) {
				return errInvalid
			}
		case "both", "agentmail", "codexpro":
			a, ok := agents[c.AgentID]
			if !ok || a.PrincipalID != o.ID || c.Host != "" {
				return errInvalid
			}
		default:
			return errInvalid
		}
	}
	return nil
}

func parse(data []byte) (*Policy, error) {
	// encoding/json normally permits duplicate names, case-folds field names,
	// and silently replaces invalid UTF-8. These are unsafe for a policy file.
	if len(data) > maxSize || !utf8.Valid(data) {
		return nil, errInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := checkValue(d, 0); err != nil {
		return nil, errInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errInvalid
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var p Policy
	if err := d.Decode(&p); err != nil {
		return nil, errInvalid
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func checkValue(d *json.Decoder, depth int) error {
	if depth > 10 {
		return errInvalid
	}
	t, err := d.Token()
	if err != nil || t == nil {
		return errInvalid
	}
	delim, compound := t.(json.Delim)
	if !compound {
		return nil
	}
	if delim != '{' && delim != '[' {
		return errInvalid
	}
	keys := map[string]bool{}
	for d.More() {
		if delim == '{' {
			k, err := d.Token()
			name, ok := k.(string)
			if err != nil || !ok || !fieldRE.MatchString(name) || !unique(keys, name) {
				return errInvalid
			}
		}
		if err := checkValue(d, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}
