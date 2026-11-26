package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func resourceObservationEnvelope() map[string]any {
	envelope := scopeInspectionEnvelope()
	envelope["pending"] = false
	envelope["allocation_claim"] = "active"
	return envelope
}

func resourceObservationScope() *ScopeIdentity {
	return &ScopeIdentity{
		Version: 1, Unit: scopeInspectionUnit, BootID: scopeInspectionBootID,
		InvocationID: scopeInspectionInvocationID,
		Cgroup:       scopeInspectionGroupParent + scopeInspectionUnit,
	}
}

func TestObserveResourceOwnershipSeparatesTerminalQuietAndDurableProof(t *testing.T) {
	cases := []struct {
		name    string
		status  string
		exit    *int
		alive   bool
		quiet   bool
		durable bool
		claim   string
	}{
		{"active", "running", nil, true, false, false, "active"},
		{"terminal_still_alive", "failed", resourceObservationExit(7), true, false, false, "active"},
		{"quiet_without_durable_settlement", "running", nil, false, true, false, "settled"},
		{"durable_terminal", "failed", resourceObservationExit(7), false, true, true, "settled"},
		{"abandoned_without_recorded_exit", "abandoned", nil, false, true, true, "settled"},
		{"previous_boot_without_durable_settlement", "running", nil, false, true, false, "previous-boot"},
		{"bound_cancelled_receipt", "cancelled", resourceObservationExit(125), false, true, true, "revoked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := resourceObservationEnvelope()
			receipt := scopeInspectionReceipt(envelope)
			receipt["status"], receipt["exit_code"] = tc.status, tc.exit
			receipt["owner_quiescent_verified"] = tc.durable
			envelope["alive"], envelope["quiescent"], envelope["allocation_claim"] = tc.alive, tc.quiet, tc.claim
			// Current unit fields must not substitute a replacement activation
			// for the original receipt anchor, even after the original ends.
			envelope["unit_state"] = map[string]any{
				"InvocationID": strings.Repeat("f", 32),
				"ControlGroup": scopeInspectionGroupParent + scopeInspectionOtherUnit,
			}
			claim := scopeInspectionHelper(t, envelope, "")
			expected := resourceObservationScope()
			got, err := ObserveResourceOwnership(context.Background(), claim, scopeInspectionUnit, expected)
			if err != nil {
				t.Fatal(err)
			}
			if got.RunID != claim.RunID || got.RegistrationID != claim.RegistrationID ||
				got.ReceiptStatus != tc.status || got.Pending || got.Alive != tc.alive ||
				got.Quiescent != tc.quiet || got.OwnerQuiescentVerified != tc.durable || got.AllocationClaim != tc.claim {
				t.Fatalf("observation collapsed independent receipt facts: %+v", got)
			}
			if (got.ReceiptExitCode == nil) != (tc.exit == nil) ||
				tc.exit != nil && *got.ReceiptExitCode != *tc.exit {
				t.Fatalf("receipt exit changed: got %v, want %v", got.ReceiptExitCode, tc.exit)
			}
			if got.ScopeIdentity == nil || *got.ScopeIdentity != *expected ||
				got.Cgroup.Path != "/sys/fs/cgroup"+expected.Cgroup || got.Cgroup.Device != 29 ||
				got.Cgroup.Inode != 12345 || got.Cgroup.Boot != expected.BootID {
				t.Fatalf("physical original binding was lost: %+v", got)
			}
			assertScopeInspectionArgs(t, claim.Helper)
		})
	}
}

func resourceObservationExit(value int) *int { return &value }

func pendingResourceObservationEnvelope(status string) map[string]any {
	envelope := resourceObservationEnvelope()
	receipt := scopeInspectionReceipt(envelope)
	receipt["status"], receipt["invocation_id"] = status, nil
	receipt["owner"] = map[string]any{"kind": "unit", "unit": scopeInspectionUnit}
	envelope["pending"], envelope["alive"], envelope["quiescent"] = true, false, false
	envelope["allocation_claim"], envelope["unit_state"] = "reserved", map[string]any{}
	return envelope
}

func TestObserveResourceOwnershipPreservesExactPendingReservation(t *testing.T) {
	for _, status := range []string{"allocating", "created"} {
		for _, groupShape := range []string{"omitted", "null"} {
			t.Run(status+"/"+groupShape, func(t *testing.T) {
				envelope := pendingResourceObservationEnvelope(status)
				if groupShape == "null" {
					scopeInspectionOwner(envelope)["cgroup"] = nil
				}
				claim := scopeInspectionHelper(t, envelope, "")
				got, err := ObserveResourceOwnership(context.Background(), claim, scopeInspectionUnit, nil)
				if err != nil || !got.Pending || got.Alive || got.Quiescent || got.OwnerQuiescentVerified ||
					got.ScopeIdentity != nil || got.Cgroup != (ResourceCgroupIdentity{}) ||
					got.RunID != claim.RunID || got.RegistrationID != claim.RegistrationID ||
					got.ReceiptStatus != status || got.AllocationClaim != "reserved" || got.ReceiptExitCode != nil {
					t.Fatalf("valid pending reservation became quiet or unknown: %+v, %v", got, err)
				}
				assertScopeInspectionArgs(t, claim.Helper)
			})
		}
	}
	cases := []struct {
		name     string
		change   func(map[string]any)
		expected *ScopeIdentity
	}{
		{"activated_original", func(map[string]any) {}, resourceObservationScope()},
		{"wrong_registration", func(e map[string]any) { scopeInspectionReceipt(e)["registration_id"] = strings.Repeat("a", 32) }, nil},
		{"wrong_unit", func(e map[string]any) { scopeInspectionOwner(e)["unit"] = scopeInspectionOtherUnit }, nil},
		{"invocation_claim", func(e map[string]any) { scopeInspectionReceipt(e)["invocation_id"] = scopeInspectionInvocationID }, nil},
		{"owner_invocation_claim", func(e map[string]any) { scopeInspectionOwner(e)["invocation_id"] = scopeInspectionInvocationID }, nil},
		{"physical_claim", func(e map[string]any) {
			scopeInspectionOwner(e)["cgroup"] = scopeInspectionCgroup(resourceObservationEnvelope())
		}, nil},
		{"empty_physical_claim", func(e map[string]any) { scopeInspectionOwner(e)["cgroup"] = map[string]any{} }, nil},
		{"string_null_physical_claim", func(e map[string]any) { scopeInspectionOwner(e)["cgroup"] = "null" }, nil},
		{"array_physical_claim", func(e map[string]any) { scopeInspectionOwner(e)["cgroup"] = []any{} }, nil},
		{"current_activation_claim", func(e map[string]any) { e["unit_state"] = map[string]any{"InvocationID": scopeInspectionInvocationID} }, nil},
		{"not_pending", func(e map[string]any) { e["pending"] = false }, nil},
		{"quiet_claim", func(e map[string]any) { e["quiescent"] = true }, nil},
		{"settled_claim", func(e map[string]any) { e["allocation_claim"] = "settled" }, nil},
		{"durable_claim", func(e map[string]any) { scopeInspectionReceipt(e)["owner_quiescent_verified"] = true }, nil},
		{"terminal_exit", func(e map[string]any) { scopeInspectionReceipt(e)["exit_code"] = 0 }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := pendingResourceObservationEnvelope("created")
			// Explicit null may stand for no activated identity, but must not
			// bypass any other pending binding or contradiction check.
			scopeInspectionOwner(envelope)["cgroup"] = nil
			tc.change(envelope)
			claim := scopeInspectionHelper(t, envelope, "")
			got, err := ObserveResourceOwnership(context.Background(), claim, scopeInspectionUnit, tc.expected)
			if err == nil || got != (ResourceObservation{}) {
				t.Fatalf("contradictory pending receipt yielded a partial proof: %+v, %v", got, err)
			}
			assertScopeInspectionArgs(t, claim.Helper)
		})
	}
}

func TestObserveResourceOwnershipRejectsUnboundUnknownOrContradictoryEvidence(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"missing_receipt", func(e map[string]any) { delete(e, "receipt") }},
		{"null_receipt_with_quiet_fallback", func(e map[string]any) {
			e["receipt"], e["alive"], e["quiescent"], e["allocation_claim"] = nil, false, true, "settled"
		}},
		{"null_owner", func(e map[string]any) { scopeInspectionReceipt(e)["owner"] = nil }},
		{"wrong_run", func(e map[string]any) { scopeInspectionReceipt(e)["run_id"] = strings.Repeat("a", 32) }},
		{"wrong_registration", func(e map[string]any) { scopeInspectionReceipt(e)["registration_id"] = strings.Repeat("a", 32) }},
		{"wrong_owner_kind", func(e map[string]any) { scopeInspectionOwner(e)["kind"] = "cgroup" }},
		{"wrong_unit", func(e map[string]any) { scopeInspectionOwner(e)["unit"] = scopeInspectionOtherUnit }},
		{"different_invocation", func(e map[string]any) { scopeInspectionOwner(e)["invocation_id"] = strings.Repeat("a", 32) }},
		{"different_boot", func(e map[string]any) { scopeInspectionCgroup(e)["boot"] = "abcdefab-1234-1234-1234-123456789abc" }},
		{"invalid_boot", func(e map[string]any) { scopeInspectionReceipt(e)["boot_id"] = "invalid" }},
		{"missing_physical_owner", func(e map[string]any) { delete(scopeInspectionOwner(e), "cgroup") }},
		{"null_physical_owner", func(e map[string]any) { scopeInspectionOwner(e)["cgroup"] = nil }},
		{"zero_device", func(e map[string]any) { scopeInspectionCgroup(e)["device"] = 0 }},
		{"fractional_device", func(e map[string]any) { scopeInspectionCgroup(e)["device"] = 1.5 }},
		{"zero_inode", func(e map[string]any) { scopeInspectionCgroup(e)["inode"] = 0 }},
		{"negative_inode", func(e map[string]any) { scopeInspectionCgroup(e)["inode"] = -1 }},
		{"noncanonical_path", func(e map[string]any) { scopeInspectionCgroup(e)["path"] = "/sys/fs/cgroup//" + scopeInspectionUnit }},
		{"outside_physical_mount", func(e map[string]any) { scopeInspectionCgroup(e)["path"] = "/tmp/" + scopeInspectionUnit }},
		{"other_path_unit", func(e map[string]any) {
			scopeInspectionCgroup(e)["path"] = "/sys/fs/cgroup/" + scopeInspectionOtherUnit
		}},
		{"missing_alive", func(e map[string]any) { delete(e, "alive") }},
		{"null_quiescent", func(e map[string]any) { e["quiescent"] = nil }},
		{"null_pending", func(e map[string]any) { e["pending"] = nil }},
		{"not_boolean", func(e map[string]any) { e["alive"] = "true" }},
		{"alive_and_quiet", func(e map[string]any) { e["quiescent"] = true }},
		{"running_pending", func(e map[string]any) { e["pending"] = true }},
		{"null_durable_proof", func(e map[string]any) { scopeInspectionReceipt(e)["owner_quiescent_verified"] = nil }},
		{"durable_but_active", func(e map[string]any) { scopeInspectionReceipt(e)["owner_quiescent_verified"] = true }},
		{"unknown_status", func(e map[string]any) { scopeInspectionReceipt(e)["status"] = "unrecognized" }},
		{"unknown_allocation", func(e map[string]any) { e["allocation_claim"] = "unknown" }},
		{"missing_allocation", func(e map[string]any) { delete(e, "allocation_claim") }},
		{"active_but_settled", func(e map[string]any) { e["allocation_claim"] = "settled" }},
		{"wrong_exit_type", func(e map[string]any) { scopeInspectionReceipt(e)["exit_code"] = "0" }},
		{"out_of_range_exit", func(e map[string]any) { scopeInspectionReceipt(e)["exit_code"] = 256 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := resourceObservationEnvelope()
			tc.change(envelope)
			claim := scopeInspectionHelper(t, envelope, "")
			got, err := ObserveResourceOwnership(context.Background(), claim, scopeInspectionUnit, nil)
			if err == nil || got != (ResourceObservation{}) {
				t.Fatalf("invalid observation yielded a partial proof: %+v, %v", got, err)
			}
			assertScopeInspectionArgs(t, claim.Helper)
		})
	}
}

func TestObserveResourceOwnershipRequiresOriginalExpectedScope(t *testing.T) {
	for _, field := range []string{"boot", "invocation", "cgroup"} {
		t.Run(field, func(t *testing.T) {
			expected := resourceObservationScope()
			switch field {
			case "boot":
				expected.BootID = "abcdefab-1234-1234-1234-123456789abc"
			case "invocation":
				expected.InvocationID = strings.Repeat("a", 32)
			case "cgroup":
				expected.Cgroup = "/different.slice/" + scopeInspectionUnit
			}
			claim := scopeInspectionHelper(t, resourceObservationEnvelope(), "")
			got, err := ObserveResourceOwnership(context.Background(), claim, scopeInspectionUnit, expected)
			if err == nil || got != (ResourceObservation{}) {
				t.Fatalf("replacement scope accepted: %+v, %v", got, err)
			}
			assertScopeInspectionArgs(t, claim.Helper)
		})
	}
	for _, invalid := range []string{"claim", "unit", "scope"} {
		t.Run("invalid_"+invalid, func(t *testing.T) {
			claim := scopeInspectionHelper(t, resourceObservationEnvelope(), "")
			helper, unit, expected := claim.Helper, scopeInspectionUnit, resourceObservationScope()
			switch invalid {
			case "claim":
				claim.Helper = "ssd-dev"
			case "unit":
				unit = "unrelated.service"
			case "scope":
				expected.Version = 2
			}
			got, err := ObserveResourceOwnership(context.Background(), claim, unit, expected)
			if err == nil || got != (ResourceObservation{}) {
				t.Fatalf("invalid caller binding accepted: %+v, %v", got, err)
			}
			if _, err := os.Stat(helper + ".args"); !os.IsNotExist(err) {
				t.Fatalf("helper ran before input validation: %v", err)
			}
		})
	}
}

func TestObserveResourceOwnershipRejectsMalformedOrFailedHelper(t *testing.T) {
	for _, response := range []string{"null", "[]", "{", "{} {}", "{\"receipt\":[]}"} {
		t.Run(response, func(t *testing.T) {
			claim := scopeInspectionHelper(t, resourceObservationEnvelope(), "")
			if err := os.WriteFile(claim.Helper+".response", []byte(response), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := ObserveResourceOwnership(context.Background(), claim, scopeInspectionUnit, nil)
			if err == nil || got != (ResourceObservation{}) {
				t.Fatalf("malformed response yielded a partial proof: %+v, %v", got, err)
			}
			assertScopeInspectionArgs(t, claim.Helper)
		})
	}
	claim := scopeInspectionHelper(t, resourceObservationEnvelope(), "/bin/cat \"$0.response\"\nexit 73")
	got, err := ObserveResourceOwnership(context.Background(), claim, scopeInspectionUnit, nil)
	if !errors.Is(err, ErrHelperFailed) || got != (ResourceObservation{}) {
		t.Fatalf("failed helper response was accepted: %+v, %v", got, err)
	}
	assertScopeInspectionArgs(t, claim.Helper)
}

func TestObserveResourceOwnershipHonorsCallerCancellation(t *testing.T) {
	t.Run("already_cancelled", func(t *testing.T) {
		claim := scopeInspectionHelper(t, resourceObservationEnvelope(), "")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, err := ObserveResourceOwnership(ctx, claim, scopeInspectionUnit, nil)
		if !errors.Is(err, context.Canceled) || got != (ResourceObservation{}) {
			t.Fatalf("cancelled inspection yielded proof: %+v, %v", got, err)
		}
		if _, err := os.Stat(claim.Helper + ".args"); !os.IsNotExist(err) {
			t.Fatalf("helper ran after prior cancellation: %v", err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		claim := scopeInspectionHelper(t, resourceObservationEnvelope(), "exec /bin/sleep 3")
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		started := time.Now()
		got, err := ObserveResourceOwnership(ctx, claim, scopeInspectionUnit, nil)
		if !errors.Is(err, context.DeadlineExceeded) || got != (ResourceObservation{}) {
			t.Fatalf("deadline yielded proof: %+v, %v", got, err)
		}
		if time.Since(started) > 2*time.Second {
			t.Fatal("inspection ignored caller deadline")
		}
		assertScopeInspectionArgs(t, claim.Helper)
	})
}

func rawResourceInspection(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(resourceObservationEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func replaceRawResourceInspection(t *testing.T, data, old, replacement string) string {
	t.Helper()
	if strings.Count(data, old) != 1 {
		t.Fatalf("raw fixture must contain exactly one %q", old)
	}
	return strings.Replace(data, old, replacement, 1)
}

func assertRawResourceInspection(t *testing.T, data string, wantErr bool, device, inode uint64) {
	t.Helper()
	for _, api := range []string{"observe", "scope"} {
		t.Run(api, func(t *testing.T) {
			claim := scopeInspectionHelper(t, resourceObservationEnvelope(), "")
			if err := os.WriteFile(claim.Helper+".response", []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			expected := resourceObservationScope()
			if api == "observe" {
				got, err := ObserveResourceOwnership(context.Background(), claim, scopeInspectionUnit, expected)
				if wantErr {
					if err == nil || got != (ResourceObservation{}) {
						t.Fatalf("ambiguous or invalid receipt yielded observation: %+v, %v", got, err)
					}
				} else if err != nil || got.ScopeIdentity == nil || *got.ScopeIdentity != *expected ||
					got.Cgroup.Device != device || got.Cgroup.Inode != inode ||
					got.Cgroup.Path != "/sys/fs/cgroup"+expected.Cgroup || got.Cgroup.Boot != expected.BootID {
					t.Fatalf("exact physical binding changed: %+v, %v", got, err)
				}
			} else {
				got, err := InspectResourceScope(context.Background(), claim, scopeInspectionUnit)
				if wantErr {
					if err == nil || got != nil {
						t.Fatalf("ambiguous or invalid receipt yielded scope: %+v, %v", got, err)
					}
				} else if err != nil || got == nil || *got != *expected {
					t.Fatalf("exact scope binding changed: %+v, %v", got, err)
				}
			}
			assertScopeInspectionArgs(t, claim.Helper)
		})
	}
}

func TestResourceInspectionRejectsAmbiguousRawNames(t *testing.T) {
	base := rawResourceInspection(t)
	cases := []struct {
		name, old, replacement string
	}{
		{"duplicate_receipt_null_then_value", `"receipt":`, `"receipt":null,"receipt":`},
		{"duplicate_run_wrong_then_correct", `"run_id":`, `"run_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","run_id":`},
		{"duplicate_owner_null_then_value", `"owner":`, `"owner":null,"owner":`},
		{"duplicate_unit_wrong_then_correct", `"unit":`, `"unit":"foreign.scope","unit":`},
		{"duplicate_group_null_then_value", `"cgroup":`, `"cgroup":null,"cgroup":`},
		{"duplicate_inode_value_then_null", `"inode":12345`, `"inode":12345,"inode":null`},
		{"escaped_duplicate_receipt", `"receipt":`, `"\u0072eceipt":null,"receipt":`},
		{"escaped_duplicate_inode", `"inode":12345`, `"inode":12345,"in\u006fde":12345`},
		{"aliased_envelope_binding", `"receipt":`, `"Receipt":`},
		{"aliased_receipt_binding", `"run_id":`, `"Run_ID":`},
		{"aliased_owner_binding", `"unit":`, `"Unit":`},
		{"aliased_cgroup_binding", `"inode":`, `"Inode":`},
		{"aliased_wrong_then_correct", `"unit":`, `"unit":"foreign.scope","Unit":`},
		{"unicode_folded_binding", `"status":`, `"\u017ftatus":`},
		{"duplicate_extension_name", `"receipt":`, `"extension":{"value":1,"value":2},"receipt":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := replaceRawResourceInspection(t, base, tc.old, tc.replacement)
			assertRawResourceInspection(t, data, true, 0, 0)
		})
	}
}

func TestResourceInspectionPreservesEscapedNamesAndExtensions(t *testing.T) {
	t.Run("escaped_canonical_names", func(t *testing.T) {
		data := rawResourceInspection(t)
		for _, names := range [][2]string{
			{`"receipt":`, `"\u0072eceipt":`},
			{`"owner":`, `"\u006fwner":`},
			{`"cgroup":`, `"\u0063group":`},
			{`"inode":`, `"in\u006fde":`},
		} {
			data = replaceRawResourceInspection(t, data, names[0], names[1])
		}
		assertRawResourceInspection(t, data, false, 29, 12345)
	})
	t.Run("case_distinct_ignored_extensions", func(t *testing.T) {
		data := rawResourceInspection(t)
		// Unknown maps and array members do not acquire binding semantics just
		// because their names resemble fields in the authoritative receipt.
		for _, fields := range [][2]string{
			{`"receipt":`, `"extension":{"device":0,"Device":1,"receipt":{"run_id":"a","RUN_ID":"b"},"items":[{"owner":null,"OWNER":{}}],"large":1e1000},"Extension":true,"receipt":`},
			{`"status":`, `"note":1,"Note":2,"status":`},
			{`"kind":`, `"extra":1,"Extra":2,"kind":`},
			{`"inode":`, `"detail":1,"Detail":2,"inode":`},
		} {
			data = replaceRawResourceInspection(t, data, fields[0], fields[1])
		}
		assertRawResourceInspection(t, data, false, 29, 12345)
	})
}

func TestResourceInspectionPreservesExactUint64Bindings(t *testing.T) {
	base := rawResourceInspection(t)
	cases := []struct {
		name, number string
		want         uint64
		wantErr      bool
	}{
		{"minimum", "1", 1, false},
		{"above_float_precision", "9007199254740993", uint64(1)<<53 + 1, false},
		{"maximum", "18446744073709551615", ^uint64(0), false},
		{"overflow", "18446744073709551616", 0, true},
		{"zero", "0", 0, true},
		{"negative", "-1", 0, true},
		{"fractional", "1.5", 0, true},
		{"exponent", "1e0", 0, true},
		{"null", "null", 0, true},
	}
	for _, field := range []string{"device", "inode"} {
		t.Run(field, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					old, device, inode := `"device":29`, uint64(29), uint64(12345)
					if field == "device" {
						device = tc.want
					} else {
						old, inode = `"inode":12345`, tc.want
					}
					data := replaceRawResourceInspection(t, base, old, `"`+field+`":`+tc.number)
					assertRawResourceInspection(t, data, tc.wantErr, device, inode)
				})
			}
		})
	}
}
