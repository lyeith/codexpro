package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

func deniedFixturePayload() (string, string, string) {
	identifier := "PRIVATE_" + "AccountToken"
	literal := "ghp_" + strings.Repeat("a1B2", 10)
	return identifier, literal, identifier + " = " + strconv.Quote(literal)
}

func denialOperatorScope(request string) wire.Scope {
	return wire.Scope{
		PrincipalID: "principal:audit-operator", SessionID: "audit-session", TransportID: "audit-transport",
		RequestID: request, Agent: "AP1",
		Identity: wire.Identity{Source: "named", PrincipalType: "system", PrincipalID: "audit-operator",
			CredentialID: "operator-device", AgentID: "operator-service", AgentKind: "coordinator"},
	}
}

func denialObject(t *testing.T, value any) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(renderPersonalResult(t, value)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func denialNames(t *testing.T, value any) []string {
	t.Helper()
	var names []string
	if err := json.Unmarshal([]byte(renderPersonalResult(t, value)), &names); err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("credential denial omitted its fixed rule names")
	}
	for _, name := range names {
		if !wire.ValidSecretRuleName(name) {
			t.Fatal("credential denial included a non-rule value")
		}
	}
	return names
}

func TestCredentialDenialPublishesSafeRetrievableReceiptWithExistingAccess(t *testing.T) {
	f := newPersonalReadFixture(t)
	identifier, literal, payload := deniedFixturePayload()
	ids := map[string]string{}
	rules := map[string][]string{}
	for _, project := range []struct{ id, root, tool string }{
		{"alpha", f.alpha, "write"},
		{"beta", f.beta, "apply_patch"},
	} {
		scope := denialOperatorScope("open-" + project.id)
		ctx := wire.WithIdentity(context.Background(), scope.Identity)
		opened, err := f.engine.Call(ctx, scope, "open_workspace", map[string]any{"project_id": project.id, "include_tree": false})
		if err != nil || opened.IsError {
			t.Fatalf("open denial fixture: %v", err)
		}
		scope.RequestID = "deny-" + project.id
		args := map[string]any{"workspace_id": workspace.IDForRoot(project.root), "agent": "AP1"}
		if project.tool == "write" {
			args["path"], args["content"] = "blocked.go", payload
		} else {
			args["patch"] = "*** Begin Patch\n*** Add File: blocked.go\n+" + payload + "\n*** End Patch\n"
		}
		for attempt := 0; attempt < 2; attempt++ {
			result, err := f.engine.Call(ctx, scope, project.tool, args)
			if err != nil || !result.IsError || result.StructuredContent["error_code"] != "secret_content_blocked" {
				t.Fatalf("real file operation was not denied: tool=%s transport=%v", project.tool, err)
			}
			if _, err := os.Stat(filepath.Join(project.root, "blocked.go")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("denied operation wrote its submitted content", err)
			}
			id, _ := result.StructuredContent["action_id"].(string)
			if !strings.HasPrefix(id, "cpa_") || len(id) != 36 || attempt > 0 && id != ids[project.id] {
				t.Fatal("credential denial did not return one stable committed action ID")
			}
			var visible strings.Builder
			for _, item := range result.Content {
				if item["type"] == "text" {
					text, _ := item["text"].(string)
					visible.WriteString(text)
				}
			}
			if strings.Count(visible.String(), id) != 1 {
				t.Fatal("text-only caller cannot recover the confirmed denial ID")
			}
			ids[project.id] = id
			rules[project.id] = denialNames(t, result.StructuredContent["rule_names"])
			if !slices.Contains(rules[project.id], "known_provider_token") {
				t.Fatal("synthetic provider literal did not exercise its existing rule")
			}
			for _, forbidden := range []string{identifier, literal, payload} {
				if strings.Contains(renderPersonalResult(t, result), forbidden) {
					t.Fatal("denial response disclosed submitted content")
				}
			}
		}

		// Broad operators retain their existing visibility across verified
		// principals and display labels; labels are never an access boundary.
		operator := denialOperatorScope("inspect-" + project.id)
		operator.Agent, operator.PrincipalID = "SP9", "principal:another-operator"
		operator.Identity.PrincipalID = "another-operator"
		operator.Identity.CredentialID, operator.Identity.AgentID = "another-device", "another-service"
		operatorCtx := wire.WithIdentity(context.Background(), operator.Identity)
		got, err := f.engine.Call(operatorCtx, operator, "activity_get", map[string]any{"action_id": ids[project.id], "agent": "SP9"})
		if err != nil || got.IsError {
			t.Fatal("existing broad operator access was restricted", err)
		}
		action := denialObject(t, got.StructuredContent["action"])
		meta := denialObject(t, action["result_metadata"])
		if action["action_id"] != ids[project.id] || action["status"] != "blocked" ||
			action["tool_name"] != project.tool || action["agent"] != "AP1" ||
			!reflect.DeepEqual(denialNames(t, meta["rule_names"]), rules[project.id]) {
			t.Fatal("activity_get did not return the matching safe denial receipt")
		}
		for _, forbidden := range []string{identifier, literal, payload} {
			if strings.Contains(renderPersonalResult(t, got), forbidden) {
				t.Fatal("operator activity retrieval disclosed submitted content")
			}
		}
	}

	allowed := f.call(t, f.ctx("alpha"), "activity_get", map[string]any{"action_id": ids["alpha"]}, false)
	projected := denialObject(t, allowed.StructuredContent["action"])
	if len(projected) != 6 || projected["project_id"] != "alpha" ||
		!reflect.DeepEqual(denialNames(t, projected["rule_names"]), rules["alpha"]) {
		t.Fatal("authorized project receipt lost its minimal safe denial names")
	}
	for key := range projected {
		if !slices.Contains([]string{"action_id", "project_id", "occurred_at", "tool_name", "status", "rule_names"}, key) {
			t.Fatalf("operator field %q entered the restricted projection", key)
		}
	}
	list := f.call(t, f.ctx("alpha"), "activity_list", map[string]any{"project_id": "alpha", "workspace_id": workspace.IDForRoot(f.alpha)}, false)
	var listed []map[string]any
	if err := json.Unmarshal([]byte(renderPersonalResult(t, list.StructuredContent["actions"])), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || !reflect.DeepEqual(listed[0], projected) {
		t.Fatal("restricted list and detail expose different denial projections")
	}
	for _, response := range []wire.Result{allowed, list} {
		for _, forbidden := range []string{identifier, literal, payload, "blocked.go", f.parent, "operator-device", "operator-service"} {
			if strings.Contains(renderPersonalResult(t, response), forbidden) {
				t.Fatal("restricted denial receipt disclosed operator or submitted detail")
			}
		}
	}
	missing := f.call(t, f.ctx("alpha"), "activity_get", map[string]any{"action_id": "cpa_" + strings.Repeat("0", 32)}, true)
	foreign := f.call(t, f.ctx("alpha"), "activity_get", map[string]any{"action_id": ids["beta"]}, true)
	revoked := f.call(t, f.ctx(), "activity_get", map[string]any{"action_id": ids["alpha"]}, true)
	if renderPersonalResult(t, foreign) != renderPersonalResult(t, missing) || renderPersonalResult(t, revoked) != renderPersonalResult(t, missing) {
		t.Fatal("foreign or revoked caller scope disclosed a denial receipt")
	}

	ordinary := denialOperatorScope("ordinary-failure")
	result, err := f.engine.Call(wire.WithIdentity(context.Background(), ordinary.Identity), ordinary, "read",
		map[string]any{"workspace_id": workspace.IDForRoot(f.alpha), "path": "missing.go"})
	if err != nil || !result.IsError || result.StructuredContent["action_id"] != nil {
		t.Fatal("ordinary errors gained a credential-denial receipt field", err)
	}
}

func TestCredentialDenialOmitsReceiptWhenAuditCannotConfirmCommit(t *testing.T) {
	for _, stage := range []string{"disabled", "before_append", "after_sync", "before_index_commit", "after_index_commit"} {
		t.Run(stage, func(t *testing.T) {
			t.Setenv("CODEXPRO_JOB_SCOPES", "0")
			fixture, scope, ws := auditTargetFixture(t)
			e, err := NewPausedWithWorkspaces(fixture.Config, fixture.Workspaces)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = e.Close() })
			opened, err := e.Call(context.Background(), scope, "open_workspace", map[string]any{"project_id": "test", "include_tree": false})
			if err != nil || opened.IsError {
				t.Fatal("failed to open fault fixture", err)
			}
			if err := e.Audit.Close(); err != nil {
				t.Fatal(err)
			}
			// No background worker is started. Swap only this disposable
			// journal, using its existing fault points around durable commit.
			e.Audit, err = audit.Open(audit.Options{
				Directory: t.TempDir(), Enabled: stage != "disabled",
				Fault: func(at string) error {
					if at == stage {
						return errors.New("injected audit durability failure")
					}
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _, payload := deniedFixturePayload()
			scope.RequestID = "fault-denial"
			result, err := e.Call(context.Background(), scope, "write",
				map[string]any{"workspace_id": ws, "path": "blocked.go", "content": payload})
			if err != nil || !result.IsError || result.StructuredContent["error_code"] != "secret_content_blocked" {
				t.Fatal("journal failure changed the actual credential denial", err)
			}
			if _, ok := result.StructuredContent["action_id"]; ok {
				t.Fatal("disabled or failed journal advertised a durable denial ID")
			}
			denialNames(t, result.StructuredContent["rule_names"])
			if len(e.Audit.InFlight()) != 0 {
				t.Fatal("failed audit retained an uncommitted Begin action")
			}
			project, found := e.Workspaces.Project("test")
			if !found {
				t.Fatal("fixture project disappeared")
			}
			if _, err := os.Stat(filepath.Join(project.Root, "blocked.go")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("denial mutated the file despite audit failure", err)
			}
		})
	}
}
