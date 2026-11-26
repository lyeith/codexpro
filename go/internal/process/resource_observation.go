package process

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var observationBootPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

// ResourceCgroupIdentity is the physical owner binding recorded by SSD. It is
// kept separately from ScopeIdentity, whose historic shape lacks device/inode.
type ResourceCgroupIdentity struct {
	Path   string `json:"path"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Boot   string `json:"boot"`
}

// ResourceObservation reports one read-only inspect result. Quiescent is the
// helper's current observation; OwnerQuiescentVerified is its separate durable
// receipt proof. A terminal receipt alone establishes neither.
type ResourceObservation struct {
	RunID                  string                 `json:"run_id"`
	RegistrationID         string                 `json:"registration_id"`
	ReceiptStatus          string                 `json:"receipt_status"`
	ReceiptExitCode        *int                   `json:"receipt_exit_code"`
	Pending                bool                   `json:"pending"`
	Alive                  bool                   `json:"alive"`
	Quiescent              bool                   `json:"quiescent"`
	AllocationClaim        string                 `json:"allocation_claim"`
	OwnerQuiescentVerified bool                   `json:"owner_quiescent_verified"`
	ScopeIdentity          *ScopeIdentity         `json:"scope_identity"`
	Cgroup                 ResourceCgroupIdentity `json:"cgroup"`
}

type resourceInspection struct {
	Receipt *struct {
		RunID                  string          `json:"run_id"`
		RegistrationID         string          `json:"registration_id"`
		BootID                 string          `json:"boot_id"`
		InvocationID           string          `json:"invocation_id"`
		Status                 string          `json:"status"`
		ExitCode               *int            `json:"exit_code"`
		OwnerQuiescentVerified json.RawMessage `json:"owner_quiescent_verified"`
		Owner                  *struct {
			Kind         string          `json:"kind"`
			Unit         string          `json:"unit"`
			InvocationID string          `json:"invocation_id"`
			Cgroup       json.RawMessage `json:"cgroup"`
		} `json:"owner"`
	} `json:"receipt"`
	Pending         *bool                      `json:"pending"`
	Alive           *bool                      `json:"alive"`
	Quiescent       *bool                      `json:"quiescent"`
	AllocationClaim string                     `json:"allocation_claim"`
	UnitState       map[string]json.RawMessage `json:"unit_state"`
}

// ObserveResourceOwnership invokes only the persisted helper's top-level
// "inspect <run_id>". It never allocates, settles, cancels, or signals the owner.
// expectedScope, when present, is the original activated identity, not a new
// identity derived from current systemd state. Errors return no partial proof.
func ObserveResourceOwnership(ctx context.Context, c ResourceClaim, expectedUnit string, expectedScope *ScopeIdentity) (ResourceObservation, error) {
	var zero ResourceObservation
	if err := ValidateClaim(c); err != nil {
		return zero, err
	}
	if !scopeUnitPattern.MatchString(expectedUnit) ||
		expectedScope != nil && (!ValidScopeIdentity(expectedScope, expectedUnit) ||
			!observationBootPattern.MatchString(expectedScope.BootID) || strings.ContainsRune(expectedScope.Cgroup, 0) ||
			filepath.Clean(expectedScope.Cgroup) != expectedScope.Cgroup) {
		return zero, errors.New("Host job resource expected scope was invalid.")
	}
	data, err := RunBounded(ctx, 10*time.Second, 1024*1024, c.Helper, "inspect", c.RunID)
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if err != nil {
		return zero, ErrHelperFailed
	}
	var in resourceInspection
	if err := decodeResourceInspection(data, &in); err != nil {
		return zero, errors.New("Host job resource observation was invalid.")
	}
	r := in.Receipt
	if r == nil || r.Owner == nil || r.RunID != c.RunID || r.RegistrationID != c.RegistrationID ||
		r.Owner.Kind != "unit" || r.Owner.Unit != expectedUnit || !observationBootPattern.MatchString(r.BootID) {
		return zero, errors.New("Host job resource owner binding was invalid.")
	}
	if in.Pending == nil || in.Alive == nil || in.Quiescent == nil ||
		r.ExitCode != nil && (*r.ExitCode < 0 || *r.ExitCode > 255) {
		return zero, errors.New("Host job resource observation was incomplete.")
	}
	var verified bool
	if len(r.OwnerQuiescentVerified) != 0 {
		var value *bool
		if err := json.Unmarshal(r.OwnerQuiescentVerified, &value); err != nil || value == nil {
			return zero, errors.New("Host job resource durable owner proof was invalid.")
		}
		verified = *value
	}
	pending, alive, quiet := *in.Pending, *in.Alive, *in.Quiescent
	terminal := false
	switch r.Status {
	case "allocating", "created", "running":
	case "succeeded", "failed", "abandoned", "cancelled":
		terminal = true
	default:
		return zero, errors.New("Host job resource receipt status was unknown.")
	}
	if pending && (alive || quiet) || alive && quiet ||
		verified && (!terminal || pending || alive || !quiet) {
		return zero, errors.New("Host job resource observation was contradictory.")
	}
	switch in.AllocationClaim {
	case "reserved":
		if !pending {
			return zero, errors.New("Host job resource reservation was contradictory.")
		}
	case "active":
		if pending || !alive || quiet {
			return zero, errors.New("Host job resource active claim was contradictory.")
		}
	case "settled", "previous-boot":
		if pending || alive || !quiet {
			return zero, errors.New("Host job resource quiet claim was contradictory.")
		}
	case "revoked":
		if r.Status != "cancelled" || !verified || pending || alive || !quiet {
			return zero, errors.New("Host job resource revoked claim was contradictory.")
		}
	default:
		return zero, errors.New("Host job resource allocation claim was unknown.")
	}
	out := ResourceObservation{
		RunID: r.RunID, RegistrationID: r.RegistrationID,
		ReceiptStatus: r.Status, ReceiptExitCode: r.ExitCode,
		Pending: pending, Alive: alive, Quiescent: quiet,
		AllocationClaim: in.AllocationClaim, OwnerQuiescentVerified: verified,
	}
	if r.Status == "allocating" || r.Status == "created" {
		// Fresh-unit reservations precede activation and have no physical owner
		// yet. An omitted or null cgroup remains a pending blocker, never proof
		// of quiet. Every other pending-owner condition must still hold.
		groupAbsent := len(r.Owner.Cgroup) == 0 || strings.TrimSpace(string(r.Owner.Cgroup)) == "null"
		if expectedScope != nil || !pending || alive || quiet || verified ||
			in.AllocationClaim != "reserved" || r.ExitCode != nil ||
			r.InvocationID != "" || r.Owner.InvocationID != "" ||
			!groupAbsent || len(in.UnitState) != 0 {
			return zero, errors.New("Host job resource pending owner was contradictory.")
		}
		return out, nil
	}
	if pending || len(r.Owner.Cgroup) == 0 {
		return zero, errors.New("Host job resource activated owner was incomplete.")
	}
	var group *ResourceCgroupIdentity
	if err := json.Unmarshal(r.Owner.Cgroup, &group); err != nil || group == nil {
		return zero, errors.New("Host job resource physical owner was invalid.")
	}
	identity := &ScopeIdentity{
		Version: 1, Unit: r.Owner.Unit, BootID: r.BootID,
		InvocationID: r.Owner.InvocationID,
		Cgroup:       strings.TrimPrefix(group.Path, "/sys/fs/cgroup"),
	}
	if r.InvocationID != r.Owner.InvocationID || r.BootID != group.Boot ||
		group.Device == 0 || group.Inode == 0 ||
		!strings.HasPrefix(group.Path, "/sys/fs/cgroup/") || strings.ContainsRune(group.Path, 0) ||
		filepath.Clean(group.Path) != group.Path || !ValidScopeIdentity(identity, expectedUnit) ||
		expectedScope != nil && *identity != *expectedScope {
		return zero, errors.New("Host job resource physical owner binding was invalid.")
	}
	out.ScopeIdentity, out.Cgroup = identity, *group
	return out, nil
}
