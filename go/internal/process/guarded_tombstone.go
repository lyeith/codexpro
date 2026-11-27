package process

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
)

// The producer writes this exact durable tombstone only after revoking an
// original unallocated registration. Missing receipts and missing units never
// substitute for it.
func observeGuardedTombstone(data []byte, c ResourceClaim, in resourceInspection, expected *ScopeIdentity) (ResourceObservation, error) {
	var zero ResourceObservation
	if expected != nil || in.Pending == nil || in.Alive == nil || in.Quiescent == nil ||
		*in.Pending || *in.Alive || !*in.Quiescent || in.AllocationClaim != "revoked" || len(in.UnitState) != 0 {
		return zero, errors.New("guarded cancellation does not prove an original unallocated owner")
	}
	var outer map[string]json.RawMessage
	if err := decodeResourceInspection(data, &outer); err != nil {
		return zero, err
	}
	fields, err := exactObject(outer["receipt"], []string{"schema", "run_id", "registration_id", "owner_uid", "boot_id", "owner", "unit", "status",
		"pinned", "started_at", "finished_at", "exit_code", "scratch_removed_at", "cache_consumer", "checkout", "owner_quiescent_verified", "control_locator"},
		[]string{"policy_version", "purpose", "expires_at", "scratch", "go_cache", "cache_lock"})
	if err != nil {
		return zero, err
	}
	owner, err := exactObject(fields["owner"], []string{"kind"}, nil)
	if err != nil || strings.TrimSpace(string(owner["kind"])) != "\"revoked\"" {
		return zero, errors.New("guarded cancellation owner shape was invalid")
	}
	if _, err := exactObject(fields["control_locator"], []string{"version", "receipts_root", "default_home"}, nil); err != nil {
		return zero, err
	}
	var locator ResourceControlLocator
	schema, schemaErr := strictUint(fields["schema"], true)
	uid, uidErr := strictUint(fields["owner_uid"], false)
	if json.Unmarshal(fields["control_locator"], &locator) != nil ||
		locator != (ResourceControlLocator{Version: 1, ReceiptsRoot: c.ReceiptsRoot, DefaultHome: c.DefaultHome}) ||
		schemaErr != nil || schema != 1 || uidErr != nil || uid != uint64(os.Geteuid()) {
		return zero, errors.New("guarded cancellation changed its original control locator")
	}
	for _, key := range []string{"unit", "checkout", "scratch", "go_cache", "cache_lock"} {
		if raw, ok := fields[key]; ok && strings.TrimSpace(string(raw)) != "null" {
			return zero, errors.New("guarded unallocated cancellation retained allocated state")
		}
	}
	for _, key := range []string{"pinned", "cache_consumer"} {
		if strings.TrimSpace(string(fields[key])) != "false" {
			return zero, errors.New("guarded cancellation retained an allocation pin")
		}
	}
	if strings.TrimSpace(string(fields["owner_quiescent_verified"])) != "true" {
		return zero, errors.New("guarded cancellation has no durable owner proof")
	}
	clock := strings.TrimSpace(string(fields["scratch_removed_at"]))
	number, err := strconv.ParseFloat(clock, 64)
	if err != nil || number <= 0 || math.IsInf(number, 0) || math.IsNaN(number) ||
		!equalJSON(fields["scratch_removed_at"], fields["started_at"]) || !equalJSON(fields["scratch_removed_at"], fields["finished_at"]) {
		return zero, errors.New("guarded cancellation clocks were inconsistent")
	}
	r := in.Receipt
	if r == nil || r.RunID != c.RunID || r.RegistrationID != c.RegistrationID ||
		!observationBootPattern.MatchString(r.BootID) || r.Status != "cancelled" ||
		r.ExitCode == nil || *r.ExitCode != 125 || r.InvocationID != "" {
		return zero, errors.New("guarded cancellation does not match the original registration")
	}
	return ResourceObservation{RunID: r.RunID, RegistrationID: r.RegistrationID, ReceiptStatus: r.Status, ReceiptExitCode: r.ExitCode,
		Quiescent: true, AllocationClaim: "revoked", OwnerQuiescentVerified: true}, nil
}
