package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/lyeith/codexpro/go/internal/jobs"
	"github.com/lyeith/codexpro/go/internal/oplog"
	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

const (
	maxPeopleProtocolInput  = 64 << 10
	maxPeopleProtocolOutput = 4 << 20
	peopleCommandTimeout    = 10 * time.Second
)

// PeopleExecutionGate applies only to fresh policy mutation commands. The read
// protocol remains inspection; a legacy direct child is recorded as unverified.
type PeopleExecutionGate interface {
	AcquireExecution(context.Context, wire.Scope, string, string) (context.Context, func(), error)
	RecordUnverifiedExecutionOwner(context.Context, jobs.ExecutionUnverifiedOwner) error
}

// PeopleCommand invokes one installed owner protocol. Both paths come from
// startup configuration and cannot be supplied by a People HTTP request.
type PeopleCommand struct {
	executable string
	policy     string
	// Execution is assigned once before serving. Nil preserves the standalone
	// owner-protocol contract used by existing integrations.
	Execution   PeopleExecutionGate
	evidenceLog oplog.Repeated
}

func NewPeopleCommand(executable, policy string) *PeopleCommand {
	return &PeopleCommand{executable: executable, policy: policy}
}

func (c *PeopleCommand) Execute(ctx context.Context, request PeopleRequest) PeopleResponse {
	if c == nil || c.executable == "" || c.policy == "" ||
		!filepath.IsAbs(c.executable) || filepath.Clean(c.executable) != c.executable ||
		!filepath.IsAbs(c.policy) || filepath.Clean(c.policy) != c.policy {
		return peopleFailure("unavailable")
	}
	if request.Version != 1 || (request.Operation != "read" && request.Operation != "apply") {
		return peopleFailure("invalid_request")
	}
	input, err := json.Marshal(request)
	if err != nil || len(input)+1 > maxPeopleProtocolInput {
		return peopleFailure("invalid_request")
	}
	applyGate := c.Execution != nil && request.Operation == "apply"
	if applyGate {
		identity := wire.IdentityFrom(ctx)
		principal := identity.PrincipalID
		if identity.Source == "named" {
			principal = "principal:" + principal
		}
		var release func()
		ctx, release, err = c.Execution.AcquireExecution(ctx, wire.Scope{Identity: identity, PrincipalID: principal, StartedAt: time.Now()}, "people_apply", "")
		if err != nil {
			if jobs.IsExecutionHeld(err) {
				return peopleFailure("execution_held")
			}
			return peopleFailure("unavailable")
		}
		defer release()
	}
	ctx, cancel := context.WithTimeout(ctx, peopleCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, c.executable, "people", "--file", c.policy)
	command.Stdin = bytes.NewReader(append(input, '\n'))
	var output peopleOutput
	command.Stdout = &output
	// Stderr can contain an operator path or output from an incompatible binary.
	// It is not an HTTP error, diagnostic log, or public job artifact.
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return peopleFailure("unavailable")
	}
	if applyGate {
		identity := process.ProcessIdentity(command.Process.Pid)
		boot, bootErr := process.BootID()
		reason := "Legacy direct-child command completion does not verify descendant quiescence."
		if identity == "" || bootErr != nil {
			reason += " Initial kernel identity was unavailable."
		}
		err := c.Execution.RecordUnverifiedExecutionOwner(ctx, jobs.ExecutionUnverifiedOwner{
			Kind: "people_apply", PID: command.Process.Pid, ProcessIdentity: identity, BootID: boot, Reason: reason,
		})
		if err != nil && c.evidenceLog.Allow() {
			log.Print("People execution owner evidence could not be retained; original result collection continues.")
		}
	}
	if err := command.Wait(); err != nil {
		return peopleProtocolFailure(request.Operation)
	}
	response, ok := decodePeopleResponse(output.Bytes(), request)
	if !ok {
		return peopleProtocolFailure(request.Operation)
	}
	return response
}

func peopleProtocolFailure(operation string) PeopleResponse {
	if operation == "apply" {
		return peopleFailure("uncertain")
	}
	return peopleFailure("unavailable")
}

type peopleOutput struct{ buffer bytes.Buffer }

func (b *peopleOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *peopleOutput) Write(data []byte) (int, error) {
	if len(data) > maxPeopleProtocolOutput-b.buffer.Len() {
		return 0, errors.New("people owner output exceeds limit")
	}
	return b.buffer.Write(data)
}

func decodePeopleResponse(data []byte, request PeopleRequest) (PeopleResponse, bool) {
	fields, valid := peopleObject(data, "ok", "roster", "self", "outcome", "error")
	if !valid || fields["ok"] == nil {
		return PeopleResponse{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var response PeopleResponse
	if decoder.Decode(&response) != nil {
		return PeopleResponse{}, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return PeopleResponse{}, false
	}
	if !response.OK {
		if len(fields) != 2 || !peopleRequiredObject(fields["error"], "code", "outcome") {
			return PeopleResponse{}, false
		}
		if response.Error == nil || response.Roster != nil || response.Self != nil || response.Outcome != "" || response.Error.Message != "" {
			return PeopleResponse{}, false
		}
		switch response.Error.Code {
		case "invalid_request", "forbidden", "not_found", "conflict", "unavailable":
			if response.Error.Outcome != "refused" {
				return PeopleResponse{}, false
			}
		case "uncertain":
			if response.Error.Outcome != "uncertain" || request.Operation != "apply" {
				return PeopleResponse{}, false
			}
		default:
			return PeopleResponse{}, false
		}
		return response, true
	}
	if response.Error != nil {
		return PeopleResponse{}, false
	}
	if request.Operation == "apply" {
		return response, len(fields) == 2 && response.Outcome == "confirmed" && response.Roster == nil && response.Self == nil
	}
	if len(fields) != 3 || !peopleRequiredObject(fields["self"], "principal_id", "agent_id", "credential_id", "role") {
		return PeopleResponse{}, false
	}
	roster, valid := peopleObject(fields["roster"], "principals", "agents", "devices")
	if !valid || len(roster) != 3 ||
		!peopleResponseRecords(roster["principals"], "id", "type", "role", "projects", "disabled", "has_delegations") ||
		!peopleResponseRecords(roster["agents"], "id", "principal_id", "kind", "address", "project_id", "disabled") ||
		!peopleResponseRecords(roster["devices"], "id", "principal_id", "agent_id", "device", "audience", "revoked", "can_revoke") {
		return PeopleResponse{}, false
	}
	if response.Roster == nil || response.Self == nil || response.Outcome != "" ||
		response.Roster.Principals == nil || response.Roster.Agents == nil || response.Roster.Devices == nil ||
		response.Self.PrincipalID != request.Actor.PrincipalID || response.Self.AgentID != request.Actor.AgentID ||
		response.Self.CredentialID != request.Actor.CredentialID || response.Self.Role != "owner" {
		return PeopleResponse{}, false
	}
	return response, true
}

func peopleRequiredObject(data []byte, keys ...string) bool {
	fields, valid := peopleObject(data, keys...)
	return valid && len(fields) == len(keys)
}

func peopleResponseRecords(data []byte, keys ...string) bool {
	var records []json.RawMessage
	if json.Unmarshal(data, &records) != nil || records == nil {
		return false
	}
	for _, record := range records {
		fields, valid := peopleObject(record, keys...)
		if !valid || len(fields) != len(keys) {
			return false
		}
		if projects, found := fields["projects"]; found {
			var items []json.RawMessage
			if json.Unmarshal(projects, &items) != nil || items == nil {
				return false
			}
			for _, item := range items {
				var id string
				if bytes.Equal(bytes.TrimSpace(item), []byte("null")) || json.Unmarshal(item, &id) != nil {
					return false
				}
			}
		}
	}
	return true
}
