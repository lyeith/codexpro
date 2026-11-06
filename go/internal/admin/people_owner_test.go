//go:build unix

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func peopleCommandFixture(t *testing.T, response []byte, exitCode int) (*PeopleCommand, string) {
	t.Helper()
	dir := t.TempDir()
	executable := filepath.Join(dir, "fixed-owner")
	script := `#!/bin/sh
set -eu
fixture_dir=$(dirname "$0")
printf '%s\n' "$@" > "$fixture_dir/argv"
cat > "$fixture_dir/request"
cat "$fixture_dir/response"
printf '%s\n' "$fixture_dir/private-diagnostic" >&2
exit ` + strconv.Itoa(exitCode) + "\n"
	for path, data := range map[string][]byte{
		executable:                     []byte(script),
		filepath.Join(dir, "response"): response,
	} {
		mode := os.FileMode(0600)
		if path == executable {
			mode = 0700
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	return NewPeopleCommand(executable, filepath.Join(dir, "fixed policy fixture.json")), dir
}

func peopleProtocolRequest(operation string) PeopleRequest {
	request := PeopleRequest{Version: 1, Actor: peopleTestActor(), Operation: operation}
	if operation == "apply" {
		request.Action = json.RawMessage(peopleAddJSON)
	}
	return request
}

func peopleResponseBytes(t *testing.T, response PeopleResponse) []byte {
	t.Helper()
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func requirePeoplePortFailure(t *testing.T, response PeopleResponse, code, outcome string) {
	t.Helper()
	if response.OK || response.Roster != nil || response.Self != nil || response.Outcome != "" ||
		response.Error == nil || response.Error.Code != code || response.Error.Outcome != outcome ||
		response.Error.Message != "" {
		t.Fatal("command result was not the expected fixed, fact-free failure")
	}
}

func requirePeopleCommandStarted(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "request")); err != nil {
		t.Fatal("fixture command never accepted its input")
	}
}

func TestPeopleCommandUsesFixedArgvAndServerActorOnPrivateInput(t *testing.T) {
	for _, operation := range []string{"read", "apply"} {
		t.Run(operation, func(t *testing.T) {
			want := peopleTestReadResponse()
			if operation == "apply" {
				want = PeopleResponse{OK: true, Outcome: "confirmed"}
			}
			command, dir := peopleCommandFixture(t, peopleResponseBytes(t, want), 0)
			request := peopleProtocolRequest(operation)
			response := command.Execute(context.Background(), request)
			if !reflect.DeepEqual(response, want) {
				t.Fatal("matching typed owner result did not survive the command boundary")
			}
			argv, err := os.ReadFile(filepath.Join(dir, "argv"))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(strings.Split(strings.TrimSuffix(string(argv), "\n"), "\n"),
				[]string{"people", "--file", filepath.Join(dir, "fixed policy fixture.json")}) {
				t.Fatal("command arguments were not the fixed owner protocol and trusted policy path")
			}
			input, err := os.ReadFile(filepath.Join(dir, "request"))
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(request)
			if err != nil || !bytes.Equal(input, append(encoded, '\n')) {
				t.Fatal("private input changed the server actor or finite request")
			}
			if bytes.Contains(input, []byte(dir)) {
				t.Fatal("command or policy path leaked into the caller request")
			}
		})
	}
}

func TestPeopleCommandRetainsClassifiedOwnerRefusal(t *testing.T) {
	for _, operation := range []string{"read", "apply"} {
		for _, code := range []string{"invalid_request", "forbidden", "not_found", "conflict", "unavailable"} {
			t.Run(operation+"/"+code, func(t *testing.T) {
				want := PeopleResponse{Error: &PeopleError{Code: code, Outcome: "refused"}}
				command, dir := peopleCommandFixture(t, peopleResponseBytes(t, want), 0)
				response := command.Execute(context.Background(), peopleProtocolRequest(operation))
				requirePeopleCommandStarted(t, dir)
				requirePeoplePortFailure(t, response, code, "refused")
			})
		}
	}
	command, dir := peopleCommandFixture(t, []byte(`{"ok":false,"error":{"code":"uncertain","outcome":"uncertain"}}`), 0)
	requirePeoplePortFailure(t, command.Execute(context.Background(), peopleProtocolRequest("apply")), "uncertain", "uncertain")
	requirePeopleCommandStarted(t, dir)
	requirePeoplePortFailure(t, command.Execute(context.Background(), peopleProtocolRequest("read")), "unavailable", "refused")
}

func TestPeopleCommandStartFailureIsARefusal(t *testing.T) {
	for _, operation := range []string{"read", "apply"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			command := NewPeopleCommand(filepath.Join(dir, "missing-owner"), filepath.Join(dir, "fixture-policy"))
			requirePeoplePortFailure(t, command.Execute(context.Background(), peopleProtocolRequest(operation)), "unavailable", "refused")
		})
	}
}

func TestPeopleCommandRejectsInvalidLocalRequestWithoutStarting(t *testing.T) {
	for _, condition := range []string{"version", "operation", "input bound", "relative executable", "relative policy"} {
		t.Run(condition, func(t *testing.T) {
			command, dir := peopleCommandFixture(t, []byte(`{"ok":true,"outcome":"confirmed"}`), 0)
			request := peopleProtocolRequest("apply")
			code := "invalid_request"
			switch condition {
			case "version":
				request.Version = 2
			case "operation":
				request.Operation = "issue"
			case "input bound":
				request.Action = json.RawMessage(`{"kind":"add_person","id":"` + strings.Repeat("x", 64<<10) + `","role":"tester"}`)
			case "relative executable":
				command = NewPeopleCommand("fixed-owner", command.policy)
				code = "unavailable"
			case "relative policy":
				command = NewPeopleCommand(command.executable, "fixture-policy")
				code = "unavailable"
			}
			requirePeoplePortFailure(t, command.Execute(context.Background(), request), code, "refused")
			if _, err := os.Stat(filepath.Join(dir, "argv")); !os.IsNotExist(err) {
				t.Fatal("invalid local request started the owner command")
			}
		})
	}
}

func TestPeopleCommandStartedFailuresDoNotClaimNoMutation(t *testing.T) {
	for _, operation := range []string{"read", "apply"} {
		for _, condition := range []string{"nonzero exit", "truncated", "malformed", "empty", "trailing JSON", "private diagnostic"} {
			t.Run(operation+"/"+condition, func(t *testing.T) {
				valid := peopleTestReadResponse()
				if operation == "apply" {
					valid = PeopleResponse{OK: true, Outcome: "confirmed"}
				}
				data, exitCode := peopleResponseBytes(t, valid), 0
				switch condition {
				case "nonzero exit":
					exitCode = 17
				case "truncated":
					data = []byte(`{"ok":true`)
				case "malformed":
					data = []byte("incompatible owner output")
				case "empty":
					data = nil
				case "trailing JSON":
					data = append(data, []byte("{}")...)
				case "private diagnostic":
					data = []byte(`{"ok":false,"error":{"code":"forbidden","outcome":"refused","message":"/private/fixture-policy: raw owner diagnostic"}}`)
				}
				command, dir := peopleCommandFixture(t, data, exitCode)
				response := command.Execute(context.Background(), peopleProtocolRequest(operation))
				requirePeopleCommandStarted(t, dir)
				code, outcome := "unavailable", "refused"
				if operation == "apply" {
					code, outcome = "uncertain", "uncertain"
				}
				requirePeoplePortFailure(t, response, code, outcome)
			})
		}
	}
}

func TestPeopleCommandRefusesPrivateFieldsAndMismatchedReadIdentity(t *testing.T) {
	valid := string(peopleResponseBytes(t, peopleTestReadResponse()))
	for _, tc := range []struct{ name, data string }{
		{"credential digest", strings.Replace(valid, `"device":"desktop"`, `"device":"desktop","sha256":"fixture-digest"`, 1)},
		{"raw policy", strings.Replace(valid, `"ok":true`, `"ok":true,"policy":{"version":1,"credentials":[]}`, 1)},
		{"private path", strings.Replace(valid, `"ok":true`, `"ok":true,"private_path":"/private/fixture-policy"`, 1)},
		{"native session", strings.Replace(valid, `"address":"people/owner-fixture/concierge"`, `"address":"people/owner-fixture/concierge","native_id":"private-native"`, 1)},
		{"success error", strings.Replace(valid, `"ok":true`, `"ok":true,"error":{"code":"forbidden","outcome":"refused"}`, 1)},
		{"read confirmation", strings.Replace(valid, `"ok":true`, `"ok":true,"outcome":"confirmed"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, dir := peopleCommandFixture(t, []byte(tc.data), 0)
			requirePeoplePortFailure(t, command.Execute(context.Background(), peopleProtocolRequest("read")), "unavailable", "refused")
			requirePeopleCommandStarted(t, dir)
		})
	}
	for _, field := range []string{"principal", "agent", "credential", "role", "missing roster", "null people", "null agents", "null devices"} {
		t.Run(field, func(t *testing.T) {
			response := peopleTestReadResponse()
			switch field {
			case "principal":
				response.Self.PrincipalID = "someone-else"
			case "agent":
				response.Self.AgentID = "other-agent"
			case "credential":
				response.Self.CredentialID = "other-device"
			case "role":
				response.Self.Role = "tester"
			case "missing roster":
				response.Roster = nil
			case "null people":
				response.Roster.Principals = nil
			case "null agents":
				response.Roster.Agents = nil
			case "null devices":
				response.Roster.Devices = nil
			}
			command, dir := peopleCommandFixture(t, peopleResponseBytes(t, response), 0)
			requirePeoplePortFailure(t, command.Execute(context.Background(), peopleProtocolRequest("read")), "unavailable", "refused")
			requirePeopleCommandStarted(t, dir)
		})
	}
}

func TestPeopleCommandRejectsDuplicateNullAndMissingResponseFields(t *testing.T) {
	validRead := string(peopleResponseBytes(t, peopleTestReadResponse()))
	for _, operation := range []string{"read", "apply"} {
		for _, condition := range []string{
			"duplicate ok", "duplicate code", "missing disabled", "null principal", "null agent", "null device",
			"null project", "empty error message", "null error message", "null ok", "numeric ok", "missing ok", "null success error",
		} {
			t.Run(operation+"/"+condition, func(t *testing.T) {
				data := validRead
				switch condition {
				case "duplicate ok":
					if operation == "apply" {
						data = `{"ok":true,"outcome":"confirmed"}`
					}
					data = strings.Replace(data, `"ok":true`, `"ok":true,"ok":true`, 1)
				case "duplicate code":
					data = `{"ok":false,"error":{"code":"forbidden","code":"forbidden","outcome":"refused"}}`
				case "missing disabled":
					data = strings.Replace(data, `,"disabled":false`, "", 1)
				case "null principal":
					data = strings.Replace(data, peoplePrincipalJSON, "null", 1)
				case "null agent":
					data = strings.Replace(data, peopleAgentJSON, "null", 1)
				case "null device":
					data = strings.Replace(data, peopleDeviceJSON, "null", 1)
				case "null project":
					data = strings.Replace(data, `"projects":[]`, `"projects":[null]`, 1)
				case "empty error message":
					data = `{"ok":false,"error":{"code":"forbidden","outcome":"refused","message":""}}`
				case "null error message":
					data = `{"ok":false,"error":{"code":"forbidden","outcome":"refused","message":null}}`
				case "null ok":
					data = `{"ok":null,"error":{"code":"forbidden","outcome":"refused"}}`
				case "numeric ok":
					data = `{"ok":1,"error":{"code":"forbidden","outcome":"refused"}}`
				case "missing ok":
					data = `{"error":{"code":"forbidden","outcome":"refused"}}`
				case "null success error":
					if operation == "apply" {
						data = `{"ok":true,"outcome":"confirmed"}`
					}
					data = strings.Replace(data, `"ok":true`, `"ok":true,"error":null`, 1)
				}
				command, dir := peopleCommandFixture(t, []byte(data), 0)
				response := command.Execute(context.Background(), peopleProtocolRequest(operation))
				requirePeopleCommandStarted(t, dir)
				code, outcome := "unavailable", "refused"
				if operation == "apply" {
					code, outcome = "uncertain", "uncertain"
				}
				requirePeoplePortFailure(t, response, code, outcome)
			})
		}
	}
}

func TestPeopleCommandRequiresAnApplyReceiptInsteadOfDesiredState(t *testing.T) {
	desired := peopleTestReadResponse()
	desired.Roster.Principals = append(desired.Roster.Principals,
		PeoplePrincipal{ID: "new-person", Type: "person", Role: "tester", Projects: []string{}})
	for _, response := range []PeopleResponse{
		{OK: true},
		{OK: true, Outcome: "refused"},
		desired,
		{OK: true, Outcome: "confirmed", Roster: desired.Roster},
	} {
		command, dir := peopleCommandFixture(t, peopleResponseBytes(t, response), 0)
		requirePeoplePortFailure(t, command.Execute(context.Background(), peopleProtocolRequest("apply")), "uncertain", "uncertain")
		requirePeopleCommandStarted(t, dir)
	}
}

// Hide the source's WriterTo so io.Copy must consider the destination ReaderFrom.
// Embedding bytes.Buffer in peopleOutput would bypass its bounded Write here.
type peopleReaderOnly struct{ io.Reader }

func TestPeopleCommandOutputBoundAlsoAppliesToIOCopy(t *testing.T) {
	const limit = 4 << 20
	var output peopleOutput
	n, err := io.Copy(&output, peopleReaderOnly{strings.NewReader(strings.Repeat("x", limit+1))})
	if err == nil || n > limit || len(output.Bytes()) > limit {
		t.Fatal("io.Copy bypassed the four MiB protocol output limit")
	}
}

func TestPeopleCommandBoundsActualProcessOutput(t *testing.T) {
	const limit = 4 << 20
	for _, operation := range []string{"read", "apply"} {
		for _, overflow := range []bool{false, true} {
			name := operation + "/at-limit"
			if overflow {
				name = operation + "/overflow"
			}
			t.Run(name, func(t *testing.T) {
				want := peopleTestReadResponse()
				if operation == "apply" {
					want = PeopleResponse{OK: true, Outcome: "confirmed"}
				}
				data := peopleResponseBytes(t, want)
				size := limit
				if overflow {
					size++
				}
				data = append(data, bytes.Repeat([]byte(" "), size-len(data))...)
				command, dir := peopleCommandFixture(t, data, 0)
				response := command.Execute(context.Background(), peopleProtocolRequest(operation))
				requirePeopleCommandStarted(t, dir)
				if !overflow {
					if !reflect.DeepEqual(response, want) {
						t.Fatal("exactly bounded valid protocol output was refused")
					}
					return
				}
				code, outcome := "unavailable", "refused"
				if operation == "apply" {
					code, outcome = "uncertain", "uncertain"
				}
				requirePeoplePortFailure(t, response, code, outcome)
			})
		}
	}
}
