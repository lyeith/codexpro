//go:build unix

package admin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lyeith/codexpro/go/internal/jobs"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type peopleExecutionFixture struct {
	events      []string
	scope       wire.Scope
	owner       jobs.ExecutionUnverifiedOwner
	recordError error
}
type peopleExecutionContextKey struct{}

func (g *peopleExecutionFixture) AcquireExecution(ctx context.Context, scope wire.Scope, tool, workspace string) (context.Context, func(), error) {
	g.events = append(g.events, "acquire")
	g.scope = scope
	if tool != "people_apply" || workspace != "" {
		return ctx, nil, errors.New("unexpected admission scope")
	}
	return context.WithValue(ctx, peopleExecutionContextKey{}, true), func() { g.events = append(g.events, "release") }, nil
}
func (g *peopleExecutionFixture) RecordUnverifiedExecutionOwner(ctx context.Context, owner jobs.ExecutionUnverifiedOwner) error {
	g.events = append(g.events, "record")
	if ctx.Value(peopleExecutionContextKey{}) != true {
		return errors.New("original execution context lost")
	}
	g.owner = owner
	return g.recordError
}
func peopleExecutionContext() context.Context {
	actor := peopleTestActor()
	return wire.WithIdentity(context.Background(), wire.Identity{
		Source: "named", PrincipalType: "person", Role: "owner", AgentKind: "concierge",
		PrincipalID: actor.PrincipalID, AgentID: actor.AgentID, CredentialID: actor.CredentialID,
	})
}

func TestPeopleExecutionHoldRefusesApplyAndPreservesRead(t *testing.T) {
	service, _, _ := fixture(t)
	gate, err := jobs.NewPaused(service.Config, service.Workspaces)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gate.Close() })
	held, err := gate.BeginExecutionHold("people-private-hold")
	if err != nil || !held.AdmissionHeld {
		t.Fatalf("hold: %#v %v", held, err)
	}
	apply, dir := peopleCommandFixture(t, peopleResponseBytes(t, PeopleResponse{OK: true, Outcome: "confirmed"}), 0)
	apply.Execution = gate
	requirePeoplePortFailure(t, apply.Execute(peopleExecutionContext(), peopleProtocolRequest("apply")), "execution_held", "refused")
	if _, err := os.Stat(filepath.Join(dir, "argv")); !os.IsNotExist(err) {
		t.Fatal("held apply started the policy owner command")
	}
	want := peopleTestReadResponse()
	read, readDir := peopleCommandFixture(t, peopleResponseBytes(t, want), 0)
	read.Execution = gate
	if got := read.Execute(peopleExecutionContext(), peopleProtocolRequest("read")); !reflect.DeepEqual(got, want) {
		t.Fatalf("held read inspection changed its contract: %#v", got)
	}
	requirePeopleCommandStarted(t, readDir)
	if !reflect.DeepEqual(gate.ExecutionStatus(), held) {
		t.Fatal("read inspection altered the execution hold")
	}
}

func TestPeopleExecutionGateRecordsOwnerAndKeepsOriginalOutcome(t *testing.T) {
	for _, tc := range []struct {
		name        string
		exitCode    int
		recordError error
	}{
		{"confirmed", 0, nil},
		{"uncertain after start", 2, nil},
		{"evidence failure still collects", 0, errors.New("private evidence failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := PeopleResponse{OK: true, Outcome: "confirmed"}
			command, dir := peopleCommandFixture(t, peopleResponseBytes(t, want), tc.exitCode)
			gate := &peopleExecutionFixture{recordError: tc.recordError}
			command.Execution = gate
			got := command.Execute(peopleExecutionContext(), peopleProtocolRequest("apply"))
			if tc.exitCode == 0 {
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("confirmed owner response changed: %#v", got)
				}
			} else {
				requirePeoplePortFailure(t, got, "uncertain", "uncertain")
			}
			requirePeopleCommandStarted(t, dir)
			if !reflect.DeepEqual(gate.events, []string{"acquire", "record", "release"}) {
				t.Fatalf("original command lifetime was not retained: %v", gate.events)
			}
			actor := peopleTestActor()
			if gate.scope.Identity.PrincipalID != actor.PrincipalID ||
				gate.scope.PrincipalID != "principal:"+actor.PrincipalID || gate.scope.Identity.AgentID != actor.AgentID {
				t.Fatalf("owner admission lost verified attribution: %#v", gate.scope)
			}
			if gate.owner.Kind != "people_apply" || gate.owner.PID <= 0 || gate.owner.Unit != "" || gate.owner.Reason == "" {
				t.Fatalf("legacy direct owner was not recorded honestly: %#v", gate.owner)
			}
		})
	}
}

func TestPeopleExecutionGateValidationPrecedesAdmission(t *testing.T) {
	command, dir := peopleCommandFixture(t, peopleResponseBytes(t, PeopleResponse{OK: true, Outcome: "confirmed"}), 0)
	gate := &peopleExecutionFixture{}
	command.Execution = gate
	request := peopleProtocolRequest("apply")
	request.Version = 2
	requirePeoplePortFailure(t, command.Execute(peopleExecutionContext(), request), "invalid_request", "refused")
	if len(gate.events) != 0 {
		t.Fatal("invalid protocol acquired execution admission")
	}
	if _, err := os.Stat(filepath.Join(dir, "argv")); !os.IsNotExist(err) {
		t.Fatal("invalid protocol started the owner command")
	}
}

func TestPeopleExecutionCompletedChildRemainsUnknownOwner(t *testing.T) {
	service, _, _ := fixture(t)
	gate, err := jobs.NewPaused(service.Config, service.Workspaces)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gate.Close() })
	want := PeopleResponse{OK: true, Outcome: "confirmed"}
	command, _ := peopleCommandFixture(t, peopleResponseBytes(t, want), 0)
	command.Execution = gate
	if got := command.Execute(peopleExecutionContext(), peopleProtocolRequest("apply")); !reflect.DeepEqual(got, want) {
		t.Fatalf("private owner command failed: %#v", got)
	}
	status, err := gate.BeginExecutionHold("people-unknown-owner")
	if err != nil {
		t.Fatal(err)
	}
	if !status.AdmissionHeld || status.DispatchHeld || len(status.PendingCalls) != 1 ||
		status.PendingCalls[0].Tool != "people_apply" || status.PendingCalls[0].References != 0 {
		t.Fatalf("direct-child completion was incorrectly treated as whole-owner quiescence: %#v", status)
	}
	if _, err := gate.ResumeExecution(status.HoldID); err == nil {
		t.Fatal("unknown legacy owner permitted resume")
	}
}
