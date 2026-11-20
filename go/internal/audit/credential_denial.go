package audit

import (
	"context"
	"errors"
	"reflect"

	"github.com/lyeith/codexpro/go/internal/wire"
)

const credentialDenialCode = "secret_content_blocked"

// Rule names are a fixed vocabulary, never a description copied from the
// submitted identifier, literal, patch, or error message.
func credentialRuleNames(value any) []string {
	var names []string
	seen := map[string]bool{}
	for _, item := range array(value) {
		name, ok := item.(string)
		if ok && wire.ValidSecretRuleName(name) && !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	return names
}

func credentialDenialRules(input RecordInput) (bool, []string) {
	if input.Error != nil {
		var denied *wire.Error
		if !errors.As(input.Error, &denied) || denied.Code != credentialDenialCode {
			return false, nil
		}
		return true, credentialRuleNames(denied.Data["rule_names"])
	}
	data := input.Result.StructuredContent
	if input.Result.IsError && text(data["error_code"]) == credentialDenialCode {
		return true, credentialRuleNames(data["rule_names"])
	}
	return false, nil
}

// ConfirmCredentialDenial returns only a still-retained, committed receipt for
// this denial. A duplicate request may refer to another result or target; a
// Begin ID, failed append, or pruned record is not a retrievable denial receipt.
// This validates the link being returned, not permission to read activity.
func (j *Journal) ConfirmCredentialDenial(ctx context.Context, input RecordInput, receipt Receipt) string {
	denied, names := credentialDenialRules(input)
	if !denied || len(names) == 0 || !receipt.Enabled || (!receipt.Recorded && !receipt.Duplicate) ||
		receipt.Status != "blocked" || receipt.ActionID == "" {
		return ""
	}
	action, found, err := j.Get(ctx, receipt.ActionID)
	if err != nil || !found {
		return ""
	}
	expected, err := j.build(input, receipt.Sequence)
	if err != nil {
		return ""
	}
	expected = clone(expected)
	for _, key := range []string{
		"tool_name", "status", "error_code", "actor_ref", "request_ref",
		"transport_session_ref", "project_id", "workspace_id", "audit_scope", "run_id",
	} {
		if text(action[key]) != text(expected[key]) {
			return ""
		}
	}
	for _, key := range []string{"identity", "targets", "request_metadata"} {
		if !reflect.DeepEqual(action[key], expected[key]) {
			return ""
		}
	}
	if !reflect.DeepEqual(object(action["result_metadata"])["rule_names"], object(expected["result_metadata"])["rule_names"]) {
		return ""
	}
	return receipt.ActionID
}
