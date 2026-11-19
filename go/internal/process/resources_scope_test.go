package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	scopeInspectionRunID          = "11111111111111111111111111111111"
	scopeInspectionRegistrationID = "22222222222222222222222222222222"
	scopeInspectionInvocationID   = "33333333333333333333333333333333"
	scopeInspectionBootID         = "12345678-1234-1234-1234-123456789abc"
	scopeInspectionUnit           = "codexpro-job_deadbeef-44444444444444444444444444444444.scope"
	scopeInspectionOtherUnit      = "codexpro-job_cafebabe-55555555555555555555555555555555.scope"
	scopeInspectionGroupParent    = "/user.slice/user-1000.slice/user@1000.service/app.slice/"
)

func scopeInspectionEnvelope() map[string]any {
	return map[string]any{
		"receipt": map[string]any{
			"run_id":          scopeInspectionRunID,
			"registration_id": scopeInspectionRegistrationID,
			"boot_id":         scopeInspectionBootID,
			"invocation_id":   scopeInspectionInvocationID,
			"status":          "running",
			"owner": map[string]any{
				"kind":          "unit",
				"unit":          scopeInspectionUnit,
				"invocation_id": scopeInspectionInvocationID,
				"cgroup": map[string]any{
					"path":   "/sys/fs/cgroup" + scopeInspectionGroupParent + scopeInspectionUnit,
					"device": 29,
					"inode":  12345,
					"boot":   scopeInspectionBootID,
				},
			},
		},
		"unit_state": map[string]any{
			"LoadState":    "loaded",
			"ActiveState":  "active",
			"InvocationID": scopeInspectionInvocationID,
			"ControlGroup": scopeInspectionGroupParent + scopeInspectionUnit,
		},
		"alive":     true,
		"pending":   true,
		"quiescent": false,
	}
}

func scopeInspectionReceipt(envelope map[string]any) map[string]any {
	return envelope["receipt"].(map[string]any)
}

func scopeInspectionOwner(envelope map[string]any) map[string]any {
	return scopeInspectionReceipt(envelope)["owner"].(map[string]any)
}

func scopeInspectionCgroup(envelope map[string]any) map[string]any {
	return scopeInspectionOwner(envelope)["cgroup"].(map[string]any)
}

// These private helpers exercise receipt inspection, not live admission,
// systemctl, scope signalling, or cgroup settlement.
func scopeInspectionHelper(t *testing.T, response any, body string) ResourceClaim {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("host resource receipt inspection requires Linux")
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "ssd-dev")
	if err := os.WriteFile(helper+".response", data, 0600); err != nil {
		t.Fatal(err)
	}
	if body == "" {
		body = "exec /bin/cat \"$0.response\""
	}
	script := "#!/bin/sh\numask 077\nprintf '%s\\n' \"$@\" >> \"$0.args\"\n" + body + "\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return ResourceClaim{
		Version:        1,
		Helper:         helper,
		RunID:          scopeInspectionRunID,
		RegistrationID: scopeInspectionRegistrationID,
	}
}

func assertScopeInspectionArgs(t *testing.T, helper string) {
	t.Helper()
	got, err := os.ReadFile(helper + ".args")
	if err != nil {
		t.Fatal(err)
	}
	want := "inspect\n" + scopeInspectionRunID + "\n"
	if string(got) != want {
		t.Fatalf("helper must receive exactly one top-level inspect call: got %q, want %q", got, want)
	}
}

func TestInspectResourceScopeUsesOriginalReceiptIdentity(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "matching_live_fields"
		if changed {
			name = "changed_live_fields_do_not_replace_original"
		}
		t.Run(name, func(t *testing.T) {
			envelope := scopeInspectionEnvelope()
			if changed {
				envelope["unit_state"] = map[string]any{
					"LoadState":    "loaded",
					"ActiveState":  "active",
					"InvocationID": strings.Repeat("f", 32),
					"ControlGroup": scopeInspectionGroupParent + scopeInspectionOtherUnit,
				}
				envelope["alive"] = false
				envelope["pending"] = false
				envelope["quiescent"] = true
			}
			claim := scopeInspectionHelper(t, envelope, "")
			got, err := InspectResourceScope(context.Background(), claim, scopeInspectionUnit)
			if err != nil || got == nil {
				t.Fatalf("original bound receipt was not recovered: identity=%+v err=%v", got, err)
			}
			want := ScopeIdentity{
				Version:      1,
				Unit:         scopeInspectionUnit,
				BootID:       scopeInspectionBootID,
				InvocationID: scopeInspectionInvocationID,
				Cgroup:       scopeInspectionGroupParent + scopeInspectionUnit,
			}
			if *got != want {
				t.Fatalf("inspection substituted or lost original ownership: got %+v, want %+v", *got, want)
			}
			assertScopeInspectionArgs(t, claim.Helper)
		})
	}
}

func TestInspectResourceScopeRejectsMissingOrMismatchedOriginal(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"missing_receipt", func(e map[string]any) { delete(e, "receipt") }},
		{"null_receipt", func(e map[string]any) { e["receipt"] = nil }},
		{"missing_owner", func(e map[string]any) { delete(scopeInspectionReceipt(e), "owner") }},
		{"null_owner", func(e map[string]any) { scopeInspectionReceipt(e)["owner"] = nil }},
		{"unbound_owner", func(e map[string]any) {
			scopeInspectionReceipt(e)["owner"] = map[string]any{"kind": "unit", "unit": scopeInspectionUnit}
		}},
		{"wrong_run", func(e map[string]any) { scopeInspectionReceipt(e)["run_id"] = strings.Repeat("a", 32) }},
		{"wrong_registration", func(e map[string]any) { scopeInspectionReceipt(e)["registration_id"] = strings.Repeat("b", 32) }},
		{"wrong_owner_kind", func(e map[string]any) { scopeInspectionOwner(e)["kind"] = "process" }},
		{"different_bound_scope", func(e map[string]any) {
			scopeInspectionOwner(e)["unit"] = scopeInspectionOtherUnit
			scopeInspectionCgroup(e)["path"] = "/sys/fs/cgroup" + scopeInspectionGroupParent + scopeInspectionOtherUnit
		}},
		{"changed_receipt_invocation", func(e map[string]any) { scopeInspectionReceipt(e)["invocation_id"] = strings.Repeat("a", 32) }},
		{"changed_owner_invocation", func(e map[string]any) { scopeInspectionOwner(e)["invocation_id"] = strings.Repeat("a", 32) }},
		{"invalid_matching_invocations", func(e map[string]any) {
			scopeInspectionReceipt(e)["invocation_id"] = "not-an-invocation"
			scopeInspectionOwner(e)["invocation_id"] = "not-an-invocation"
		}},
		{"changed_receipt_boot", func(e map[string]any) { scopeInspectionReceipt(e)["boot_id"] = "abcdefab-1234-1234-1234-123456789abc" }},
		{"changed_owner_boot", func(e map[string]any) { scopeInspectionCgroup(e)["boot"] = "abcdefab-1234-1234-1234-123456789abc" }},
		{"invalid_matching_boots", func(e map[string]any) {
			scopeInspectionReceipt(e)["boot_id"] = "not-a-boot"
			scopeInspectionCgroup(e)["boot"] = "not-a-boot"
		}},
		{"missing_cgroup", func(e map[string]any) { delete(scopeInspectionOwner(e), "cgroup") }},
		{"null_cgroup", func(e map[string]any) { scopeInspectionOwner(e)["cgroup"] = nil }},
		{"outside_cgroup_mount", func(e map[string]any) { scopeInspectionCgroup(e)["path"] = "/tmp/" + scopeInspectionUnit }},
		{"prefix_lookalike", func(e map[string]any) {
			scopeInspectionCgroup(e)["path"] = "/sys/fs/cgroup-other/" + scopeInspectionUnit
		}},
		{"relative_cgroup_path", func(e map[string]any) { scopeInspectionCgroup(e)["path"] = "sys/fs/cgroup/" + scopeInspectionUnit }},
		{"mount_root", func(e map[string]any) { scopeInspectionCgroup(e)["path"] = "/sys/fs/cgroup" }},
		{"different_path_unit", func(e map[string]any) {
			scopeInspectionCgroup(e)["path"] = "/sys/fs/cgroup" + scopeInspectionGroupParent + scopeInspectionOtherUnit
		}},
		{"parent_traversal", func(e map[string]any) {
			scopeInspectionCgroup(e)["path"] = "/sys/fs/cgroup/user.slice/../" + scopeInspectionUnit
		}},
		{"dot_component", func(e map[string]any) {
			scopeInspectionCgroup(e)["path"] = "/sys/fs/cgroup/user.slice/./" + scopeInspectionUnit
		}},
		{"duplicate_separator", func(e map[string]any) {
			scopeInspectionCgroup(e)["path"] = "/sys/fs/cgroup//user.slice/" + scopeInspectionUnit
		}},
		{"trailing_separator", func(e map[string]any) {
			scopeInspectionCgroup(e)["path"] = "/sys/fs/cgroup" + scopeInspectionGroupParent + scopeInspectionUnit + "/"
		}},
		{"missing_device", func(e map[string]any) { delete(scopeInspectionCgroup(e), "device") }},
		{"zero_device", func(e map[string]any) { scopeInspectionCgroup(e)["device"] = 0 }},
		{"negative_device", func(e map[string]any) { scopeInspectionCgroup(e)["device"] = -1 }},
		{"fractional_device", func(e map[string]any) { scopeInspectionCgroup(e)["device"] = 1.5 }},
		{"missing_inode", func(e map[string]any) { delete(scopeInspectionCgroup(e), "inode") }},
		{"zero_inode", func(e map[string]any) { scopeInspectionCgroup(e)["inode"] = 0 }},
		{"negative_inode", func(e map[string]any) { scopeInspectionCgroup(e)["inode"] = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := scopeInspectionEnvelope()
			// Even a plausible current scope cannot supply a missing original.
			envelope["quiescent"] = true
			tc.change(envelope)
			claim := scopeInspectionHelper(t, envelope, "")
			got, err := InspectResourceScope(context.Background(), claim, scopeInspectionUnit)
			if err == nil || got != nil {
				t.Fatalf("invalid original produced a usable identity: identity=%+v err=%v", got, err)
			}
			assertScopeInspectionArgs(t, claim.Helper)
		})
	}
}

func TestInspectResourceScopeRejectsInvalidClaimOrExpectedUnitBeforeHelper(t *testing.T) {
	cases := []struct {
		name   string
		change func(*ResourceClaim, *string)
	}{
		{"claim_version", func(c *ResourceClaim, _ *string) { c.Version = 2 }},
		{"claim_run", func(c *ResourceClaim, _ *string) { c.RunID = "invalid" }},
		{"claim_registration", func(c *ResourceClaim, _ *string) { c.RegistrationID = "invalid" }},
		{"relative_helper", func(c *ResourceClaim, _ *string) { c.Helper = "ssd-dev" }},
		{"missing_expected_unit", func(_ *ResourceClaim, u *string) { *u = "" }},
		{"non_job_unit", func(_ *ResourceClaim, u *string) { *u = "other.service" }},
		{"noncanonical_expected_unit", func(_ *ResourceClaim, u *string) { *u = "../" + scopeInspectionUnit }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claim := scopeInspectionHelper(t, scopeInspectionEnvelope(), "")
			helper := claim.Helper
			unit := scopeInspectionUnit
			tc.change(&claim, &unit)
			got, err := InspectResourceScope(context.Background(), claim, unit)
			if err == nil || got != nil {
				t.Fatalf("invalid claim/unit was admitted: identity=%+v err=%v", got, err)
			}
			if _, err := os.Stat(helper + ".args"); !os.IsNotExist(err) {
				t.Fatalf("helper ran before input validation: %v", err)
			}
		})
	}
}

func TestInspectResourceScopeRejectsMalformedInspection(t *testing.T) {
	for _, response := range []string{"{", "[]", "null", "{} {}", "{\"receipt\":[]}"} {
		t.Run(response, func(t *testing.T) {
			claim := scopeInspectionHelper(t, scopeInspectionEnvelope(), "")
			if err := os.WriteFile(claim.Helper+".response", []byte(response), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := InspectResourceScope(context.Background(), claim, scopeInspectionUnit)
			if err == nil || got != nil {
				t.Fatalf("malformed helper response produced an identity: identity=%+v err=%v", got, err)
			}
		})
	}
}

func TestInspectResourceScopeRejectsFailedHelperEvenWithValidReceipt(t *testing.T) {
	claim := scopeInspectionHelper(t, scopeInspectionEnvelope(), "/bin/cat \"$0.response\"\nexit 73")
	got, err := InspectResourceScope(context.Background(), claim, scopeInspectionUnit)
	if !errors.Is(err, ErrHelperFailed) || got != nil {
		t.Fatalf("failed helper output was accepted: identity=%+v err=%v", got, err)
	}
	assertScopeInspectionArgs(t, claim.Helper)
}

func TestInspectResourceScopeHonorsAlreadyCancelledContext(t *testing.T) {
	claim := scopeInspectionHelper(t, scopeInspectionEnvelope(), "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := InspectResourceScope(ctx, claim, scopeInspectionUnit)
	if err == nil || got != nil {
		t.Fatalf("cancelled inspection produced an identity: identity=%+v err=%v", got, err)
	}
	if _, err := os.Stat(claim.Helper + ".args"); !os.IsNotExist(err) {
		t.Fatalf("helper ran for an already-cancelled context: %v", err)
	}
}

func TestInspectResourceScopeBoundsHelperByCallerContext(t *testing.T) {
	claim := scopeInspectionHelper(t, scopeInspectionEnvelope(), "printf 'started\\n' > \"$0.started\"\nexec /bin/sleep 3")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	got, err := InspectResourceScope(ctx, claim, scopeInspectionUnit)
	elapsed := time.Since(started)
	if !errors.Is(err, ErrHelperFailed) || got != nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("helper ignored the caller deadline: identity=%+v err=%v context=%v", got, err, ctx.Err())
	}
	if elapsed > 2*time.Second {
		t.Fatalf("inspection exceeded its caller's short bound: %v", elapsed)
	}
	seen, err := os.ReadFile(claim.Helper + ".started")
	if err != nil || string(seen) != "started\n" {
		t.Fatalf("blocking helper was not observed started: %q %v", seen, err)
	}
	assertScopeInspectionArgs(t, claim.Helper)
}
