// Package legacyworkspace imports immutable legacy workspace snapshots as
// read-only history. It never creates an active lease or touches a checkout.
package legacyworkspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const SchemaVersion = 1

type Options struct {
	SnapshotRoot string
	CatalogFile  string
	ManagedRoot  string
	Destination  string
}

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Lease struct {
	File
	Version      int     `json:"source_version"`
	WorkspaceID  string  `json:"workspace_id"`
	ProjectID    *string `json:"project_id"`
	HasProjectID bool    `json:"has_project_id"`
	RepositoryID string  `json:"repository_id"`
	OwnerID      string  `json:"owner_id"`
	State        string  `json:"state"`
}

type Ignored struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type Manifest struct {
	SchemaVersion        int            `json:"schema_version"`
	SnapshotRoot         string         `json:"snapshot_root"`
	ManagedRoot          string         `json:"managed_root"`
	Catalog              File           `json:"catalog"`
	Identity             *File          `json:"connector_identity"`
	ProjectIDs           []string       `json:"project_ids"`
	CatalogRootsResolved bool           `json:"catalog_roots_resolved"`
	Leases               []Lease        `json:"leases"`
	Ignored              []Ignored      `json:"ignored"`
	States               map[string]int `json:"state_counts"`
	Versions             map[string]int `json:"source_version_counts"`
}

type Receipt struct {
	SchemaVersion   int      `json:"schema_version"`
	ImportID        string   `json:"import_id"`
	ImportedAt      string   `json:"imported_at"`
	Destination     string   `json:"destination_database"`
	ReadOnlyHistory bool     `json:"read_only_history"`
	ActiveAdoptions int      `json:"active_lease_adoptions"`
	AlreadyImported bool     `json:"already_imported"`
	Manifest        Manifest `json:"manifest"`
}

type Record struct {
	Lease
	// RawJSON preserves every original byte, including unknown and optional
	// fields. The indexed projection does not replace the legacy document.
	RawJSON []byte `json:"raw_json"`
}

type Snapshot struct {
	Receipt
	CatalogJSON []byte   `json:"catalog_json"`
	Identity    []byte   `json:"connector_identity_bytes"`
	Records     []Record `json:"records"`
}

func hash(raw []byte) string {
	value := sha256.Sum256(raw)
	return hex.EncodeToString(value[:])
}

func importID(manifest Manifest) (string, error) {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return "legacy_workspaces_" + hash(raw), nil
}

// SourceLease validates the exact FileLeaseStore.isLease predicate. In
// particular, the source does not validate revision or optional label/release/
// retry fields. Those values are preserved in RawJSON, without granting them
// runtime authority. A version-1 projectId is allowed to be absent or non-string.
func SourceLease(raw []byte, name string) (Lease, error) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return Lease{}, fmt.Errorf("invalid lease JSON: %w", err)
	}
	if value == nil {
		return Lease{}, fmt.Errorf("lease schema or filename does not match")
	}
	version, ok := value["version"].(float64)
	if !ok || version != 1 && version != 2 {
		return Lease{}, fmt.Errorf("lease schema or filename does not match")
	}
	required := []string{
		"workspaceId", "repositoryId", "repositoryCommonDir", "checkoutRoot",
		"workspaceRoot", "branch", "baseCommit", "ownerId", "createdAt",
		"updatedAt", "lastUsedAt",
	}
	if version == 2 {
		required = append(required, "projectId")
	}
	for _, field := range required {
		if _, ok := value[field].(string); !ok {
			return Lease{}, fmt.Errorf("lease schema or filename does not match: %s", field)
		}
	}
	state, ok := sourceState(value["state"])
	if !ok {
		return Lease{}, fmt.Errorf("lease schema or filename does not match: state")
	}
	id := value["workspaceId"].(string)
	if name != hash([]byte(id))+".json" {
		return Lease{}, fmt.Errorf("lease schema or filename does not match")
	}
	var project *string
	projectValue, present := value["projectId"]
	if id, ok := projectValue.(string); ok {
		project = &id
	}
	return Lease{
		Version: int(version), WorkspaceID: id, ProjectID: project,
		HasProjectID: present, RepositoryID: value["repositoryId"].(string),
		OwnerID: value["ownerId"].(string), State: state,
	}, nil
}

// The baseline tests String(lease.state), rather than typeof state === "string".
// Single-element nested arrays therefore pass for the five exact state words.
// Other JSON values cannot stringify to any of those words.
func sourceState(value any) (string, bool) {
	for {
		switch value := value.(type) {
		case string:
			switch value {
			case "provisioning", "ready", "removing", "orphaned", "failed":
				return value, true
			}
			return "", false
		case []any:
			if len(value) != 1 {
				return "", false
			}
			return sourceState(value[0])
		default:
			return "", false
		}
	}
}

func sourceIdentity(raw []byte) error {
	// Match ECMAScript trim exactly; Go TrimSpace additionally accepts U+0085,
	// which the reference's persistent-identity reader rejects.
	identity := trimJS(string(raw))
	if len(identity) != 48 || strings.ToLower(identity) != identity {
		return fmt.Errorf("invalid persistent connector identity in snapshot")
	}
	if _, err := hex.DecodeString(identity); err != nil {
		return fmt.Errorf("invalid persistent connector identity in snapshot")
	}
	return nil
}

func trimJS(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		switch r {
		case '\u0009', '\u000a', '\u000b', '\u000c', '\u000d', '\u0020',
			'\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
			return true
		default:
			return r >= '\u2000' && r <= '\u200a'
		}
	})
}

func equalJSON(left, right any) bool {
	a, e1 := json.Marshal(left)
	b, e2 := json.Marshal(right)
	return e1 == nil && e2 == nil && bytes.Equal(a, b)
}
