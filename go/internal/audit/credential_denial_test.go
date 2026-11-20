package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func credentialDenialInput(project, request string) RecordInput {
	input := recordInput(project, request)
	input.ToolName, input.Mutating = "write", true
	input.Args = map[string]any{"workspace_id": "ws_test", "path": "safe.go", "content": "synthetic submitted content"}
	input.Result = wire.Result{}
	input.Error = &wire.Error{Code: credentialDenialCode, Message: "credential denial fixture",
		Data: map[string]any{"retry_unchanged": false, "rule_names": []string{"known_provider_token"}}}
	input.Target = &Target{Scope: "workspace", ProjectID: project, WorkspaceID: "ws_test"}
	return input
}

func TestCredentialDenialMetadataKeepsOnlyFixedNames(t *testing.T) {
	for _, resultEnvelope := range []bool{false, true} {
		name := "wrapped_error"
		if resultEnvelope {
			name = "result_envelope"
		}
		t.Run(name, func(t *testing.T) {
			options := testOptions(t.TempDir())
			j := openTest(t, options)
			identifier := "PRIVATE_" + "AccountToken"
			literal := "ghp_" + strings.Repeat("a1B2", 10)
			payload := identifier + " = " + strconv.Quote(literal)
			input := credentialDenialInput("alpha", name)
			input.Args["content"] = payload
			data := map[string]any{
				"retry_unchanged": false,
				"rule_names": []any{
					"credential_named_assignment", identifier, literal, "",
					"credential_named_assignment", "known_provider_token", 7,
					map[string]any{"rule": "authorization_bearer", "value": literal},
				},
				"private_identifier": identifier, "private_value": literal, "patch": payload,
			}
			denied := &wire.Error{Code: credentialDenialCode, Message: "private-denial-message " + payload, Data: data}
			if resultEnvelope {
				input.ToolName = "apply_patch"
				input.Args = map[string]any{"workspace_id": "ws_test", "patch": "*** Begin Patch\n*** Add File: safe.go\n+" + payload + "\n*** End Patch\n"}
				input.Error = nil
				input.Result = wire.ErrorResult(denied)
			} else {
				input.Error = fmt.Errorf("wrapped: %w", denied)
			}
			receipt := writeTest(t, j, input)
			action, found, err := j.Get(context.Background(), receipt.ActionID)
			if err != nil || !found || action["status"] != "blocked" || action["error_code"] != credentialDenialCode {
				t.Fatalf("denial receipt unavailable: found=%t err=%v", found, err)
			}
			want := []string{"credential_named_assignment", "known_provider_token"}
			if got := credentialRuleNames(object(action["result_metadata"])["rule_names"]); !reflect.DeepEqual(got, want) {
				t.Fatalf("stored rule names: %v", got)
			}
			j.mu.RLock()
			private, readErr := j.readEntry(j.byID[receipt.ActionID])
			j.mu.RUnlock()
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, record := range []Action{action, private, ProjectReceipt(action)} {
				encoded, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				for _, forbidden := range []string{identifier, literal, payload, "private-denial-message", "private_identifier", "private_value"} {
					if strings.Contains(string(encoded), forbidden) {
						t.Fatal("denial receipt retained submitted content or arbitrary error text")
					}
				}
			}
			projected := ProjectReceipt(action)
			if len(projected) != 6 || !reflect.DeepEqual(credentialRuleNames(projected["rule_names"]), want) {
				t.Fatalf("minimal visible denial projection lost fixed names: %#v", projected)
			}
			if got := j.ConfirmCredentialDenial(context.Background(), input, receipt); got != receipt.ActionID {
				t.Fatal("durable matching denial could not publish its receipt")
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openTest(t, options)
			restored, found, err := reopened.Get(context.Background(), receipt.ActionID)
			if err != nil || !found || !reflect.DeepEqual(restored, action) {
				t.Fatal("denial receipt or fixed names changed after reopening", err)
			}
		})
	}
}

func TestCredentialDenialProjectionRevalidatesImportedNames(t *testing.T) {
	action := Action{
		"action_id": "cpa_fixture", "project_id": "alpha", "workspace_id": "own",
		"audit_scope": "workspace", "tool_name": "write", "status": "blocked", "error_code": credentialDenialCode,
		"result_metadata": map[string]any{"rule_names": []any{
			"known_provider_token", "private-source-identifier", "known_provider_token", 17,
			map[string]any{"rule": "authorization_bearer"},
		}},
	}
	if !ProjectReceiptVisible(action, map[string]bool{"own": true}) || ProjectReceiptVisible(action, map[string]bool{"foreign": true}) {
		t.Fatal("denial metadata changed the existing visibility boundary")
	}
	projected := ProjectReceipt(action)
	if len(projected) != 6 || !reflect.DeepEqual(projected["rule_names"], []string{"known_provider_token"}) {
		t.Fatalf("unvalidated values reached the restricted projection: %#v", projected)
	}
	for _, change := range []struct{ field, value string }{{"status", "succeeded"}, {"error_code", "policy_blocked"}} {
		other := clone(action)
		other[change.field] = change.value
		if got := ProjectReceipt(other); len(got) != 5 || got["rule_names"] != nil {
			t.Fatal("ordinary receipt gained denial-only metadata")
		}
	}
	for _, value := range []any{"known_provider_token", []any{"private-source-identifier", 17}, map[string]any{"known_provider_token": true}} {
		other := clone(action)
		object(other["result_metadata"])["rule_names"] = value
		if got := ProjectReceipt(other); len(got) != 5 || got["rule_names"] != nil {
			t.Fatal("untrusted rule-name shape expanded the minimal projection")
		}
	}
}

func TestCredentialDenialConfirmationRequiresMatchingRetainedReceipt(t *testing.T) {
	options := testOptions(t.TempDir())
	options.RetainActions = 2
	j := openTest(t, options)
	ctx := context.Background()
	input := credentialDenialInput("alpha", "same-request")
	first := writeTest(t, j, input)
	duplicate, err := j.Record(ctx, input)
	if err != nil || !duplicate.Duplicate || duplicate.Recorded || duplicate.ActionID != first.ActionID {
		t.Fatalf("fixture did not retain the original request receipt: %+v %v", duplicate, err)
	}
	if got := j.ConfirmCredentialDenial(ctx, input, duplicate); got != first.ActionID {
		t.Fatal("matching duplicate lost the stable committed ID")
	}
	beginID := j.Begin(input)
	if got := j.ConfirmCredentialDenial(ctx, input, Receipt{Enabled: true, Status: "blocked", ActionID: beginID}); got != "" {
		t.Fatal("an in-flight Begin ID was published as durable")
	}
	j.End(beginID)

	for _, tc := range []struct {
		name   string
		change func(*RecordInput)
	}{
		{"tool", func(in *RecordInput) { in.ToolName = "edit" }},
		{"principal", func(in *RecordInput) { in.Scope.PrincipalID = "another-verified-principal" }},
		{"transport", func(in *RecordInput) { in.Scope.TransportID = "another-transport" }},
		{"request", func(in *RecordInput) { in.Scope.RequestID = "another-request" }},
		{"identity", func(in *RecordInput) {
			in.Scope.Identity = wire.Identity{Source: "named", PrincipalID: "another-caller"}
		}},
		{"project", func(in *RecordInput) {
			in.Target = &Target{Scope: "workspace", ProjectID: "beta", WorkspaceID: "ws_test"}
		}},
		{"workspace", func(in *RecordInput) {
			in.Target = &Target{Scope: "workspace", ProjectID: "alpha", WorkspaceID: "other"}
		}},
		{"file", func(in *RecordInput) {
			in.Args = map[string]any{"workspace_id": "ws_test", "path": "different.go", "content": "synthetic submitted content"}
		}},
		{"rules", func(in *RecordInput) {
			in.Error = &wire.Error{Code: credentialDenialCode, Data: map[string]any{"rule_names": []string{"authorization_bearer"}}}
		}},
		{"ordinary_error", func(in *RecordInput) { in.Error = wire.Errorf("policy_blocked", "ordinary policy denial") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := input
			tc.change(&current)
			if got := j.ConfirmCredentialDenial(ctx, current, duplicate); got != "" {
				t.Fatal("a different result or verified scope reused an earlier receipt")
			}
		})
	}

	// Reusing a request fingerprint must not label an earlier success as the
	// receipt for a subsequent credential denial.
	prior := credentialDenialInput("alpha", "previous-success")
	prior.Error = nil
	prior.Result = wire.Success("write", "Write", map[string]any{"path": "safe.go"}, "written")
	success := writeTest(t, j, prior)
	current := credentialDenialInput("alpha", "previous-success")
	reused, err := j.Record(ctx, current)
	if err != nil || !reused.Duplicate || reused.ActionID != success.ActionID {
		t.Fatal("fixture did not exercise prior-result deduplication", err)
	}
	if got := j.ConfirmCredentialDenial(ctx, current, reused); got != "" {
		t.Fatal("a successful prior action was advertised as a credential denial")
	}
	writeTest(t, j, credentialDenialInput("alpha", "later-denial"))
	if _, found, err := j.Get(ctx, first.ActionID); err != nil || found {
		t.Fatal("fixture did not exercise bounded retention", err)
	}
	if got := j.ConfirmCredentialDenial(ctx, input, first); got != "" {
		t.Fatal("a pruned receipt was advertised as currently retrievable")
	}
}
