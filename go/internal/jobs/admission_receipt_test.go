package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

const r844ReceiptGate = "printf 'once\\n' >> receipt-started; while [ ! -f receipt-release ]; do sleep 0.01; done"

func r844ReceiptScope() wire.Scope {
	return wire.Scope{
		Agent: "SP16",
		Identity: wire.Identity{
			Source: "named", PrincipalType: "system", PrincipalID: "r844-private-principal",
			CredentialID: "r844-private-credential", AgentID: "r844-private-agent", AgentKind: "leaf",
		},
		PrincipalID: "r844-private-owner", SessionID: "r844-private-session", RequestID: "r844-private-request",
	}
}

func r844ReceiptRelease(t *testing.T, resolver testResolver) {
	t.Helper()
	t.Cleanup(func() {
		if err := os.WriteFile(filepath.Join(resolver.workspace.Root, "receipt-release"), nil, 0600); err != nil {
			t.Error(err)
		}
	})
}

func r844ReceiptRecord(t *testing.T, s *Service, command string) Record {
	t.Helper()
	for _, record := range s.Snapshot() {
		if record.Command == command {
			return record
		}
	}
	t.Fatalf("accepted command has no retained record: %q", command)
	return Record{}
}

func r844ReceiptPublic(t *testing.T, value any) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			switch key {
			case "pid", "process_identity", "scope_unit", "scope_identity", "resource_claim",
				"run_id", "registration_id", "helper", "spec_path", "grant_path", "result_path",
				"control_path", "identity", "owner_principal_id", "owner_session_id",
				"credential_id", "parent_action_id", "request_id", "queued_cwd_abs":
				t.Errorf("public receipt exposed private authority field %q", key)
			}
			r844ReceiptPublic(t, child)
		}
	case []any:
		for _, child := range value {
			r844ReceiptPublic(t, child)
		}
	}
}

func r844ReceiptError(t *testing.T, s *Service, err error, code string, records []Record, labels []string, failedID string, retry bool) map[string]any {
	t.Helper()
	if err == nil {
		t.Fatal("expected a handler error after the controlled failure")
	}
	var typed *wire.Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Errorf("error code/cause was not preserved: %T %v, want %s", err, err, code)
	}
	rendered := wire.ErrorResult(err)
	data := resultData(t, rendered)
	wantIDs := make([]any, len(records))
	for i, record := range records {
		wantIDs[i] = record.ID
	}
	if !reflect.DeepEqual(data["job_ids"], wantIDs) {
		t.Errorf("ordered accepted IDs = %#v, want %#v", data["job_ids"], wantIDs)
	}
	if failedID == "" {
		if value, present := data["job_id"]; present {
			t.Errorf("pre-admission failure invented a failing job_id: %#v", value)
		}
	} else if data["job_id"] != failedID {
		t.Errorf("failing job_id = %#v, want %s", data["job_id"], failedID)
	}
	if data["retry_unchanged"] != retry {
		t.Errorf("retry_unchanged = %#v, want %v", data["retry_unchanged"], retry)
	}
	var text strings.Builder
	for _, block := range rendered.Content {
		if part, ok := block["text"].(string); ok {
			text.WriteString(part)
			text.WriteByte('\n')
		}
	}
	encodedIDs, marshalErr := json.Marshal(wantIDs)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if !strings.Contains(text.String(), "jobs(job_ids="+string(encodedIDs)) {
		t.Errorf("ordinary error text lost the ordered collection instruction: %s", text.String())
	}
	r844ReceiptPublic(t, data)
	encoded, marshalErr := json.Marshal(data)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, private := range []string{"r844-private-credential", "r844-private-owner", "r844-private-session", "r844-private-request"} {
		if strings.Contains(string(encoded), private) {
			t.Errorf("public receipt exposed private value %q", private)
		}
	}
	views, ok := data["jobs"].([]any)
	if !ok || len(views) != len(records) {
		t.Errorf("public jobs = %#v, want %d ordered views", data["jobs"], len(records))
		return data
	}
	for i, record := range records {
		view, ok := views[i].(map[string]any)
		if !ok {
			t.Errorf("jobs[%d] is not a public object: %#v", i, views[i])
			continue
		}
		for key, want := range map[string]any{
			"job_id": record.ID, "agent": record.Agent, "command": record.CommandLabel,
			"cwd": record.Cwd, "origin": record.Origin, "started_at": record.StartedAt,
			"launch_state": record.LaunchState,
		} {
			if view[key] != want {
				t.Errorf("jobs[%d].%s = %#v, want %#v", i, key, view[key], want)
			}
		}
		if labels != nil && view["label"] != labels[i] {
			t.Errorf("jobs[%d].label = %#v, want %q", i, view["label"], labels[i])
		}
		for key := range s.jobView(record, nil, false) {
			if _, present := view[key]; !present {
				t.Errorf("jobs[%d] omitted normal public field %q", i, key)
			}
		}
	}
	return data
}

// This private helper permits a bounded number of real runners, then refuses begin.
// It never touches the installed resource controller or changes the test process's scope.
func r844ReceiptHostFixture(t *testing.T, allowedBegins int) (*Service, testResolver) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("host resource claims require Linux")
	}
	s, cfg, resolver := newFixture(t)
	dir := filepath.Join(resolver.workspace.Root, "receipt-host")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	const script = `#!/bin/sh
set -eu
dir=$(dirname "$0")
[ "$1" = external ]
operation=$2
shift 2
case "$operation" in
begin)
  run=
  registration=
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --run-id) run=$2; shift 2 ;;
      --registration-id) registration=$2; shift 2 ;;
      *) shift ;;
    esac
  done
  count=0
  if [ -f "$dir/begins" ]; then read -r count < "$dir/begins"; fi
  count=$((count + 1))
  printf '%s\n' "$count" > "$dir/begins"
  if [ "$count" -gt R844_RECEIPT_BEGINS ]; then exit 125; fi
  printf '{"run_id":"%s","registration_id":"%s","environment":{},"owner":{}}\n' "$run" "$registration"
  ;;
exec)
  [ "$2" = --registration-id ]
  [ "$4" = -- ]
  printf '%s:%s\n' "$1" "$3" >> "$dir/executed"
  shift 4
  exec "$@"
  ;;
cancel)
  printf '{"run_id":"%s","status":"cancelled","quiescent":true,"cancelled":true}\n' "$1"
  ;;
finish)
  printf '{"run_id":"%s","status":"succeeded","quiescent":true,"cancelled":false}\n' "$1"
  ;;
*) exit 125 ;;
esac
`
	helper := filepath.Join(dir, "resources")
	if err := os.WriteFile(helper, []byte(strings.ReplaceAll(script, "R844_RECEIPT_BEGINS", strconv.Itoa(allowedBegins))), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.HostResourcesHelper = helper
	cfg.Set("hostResourceRetryMs", 0)
	r844ReceiptRelease(t, resolver)
	before, err := json.Marshal(process.ErrHostResourcesUnavailable)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		after, err := json.Marshal(process.ErrHostResourcesUnavailable)
		if err != nil || string(before) != string(after) {
			t.Errorf("shared host refusal was mutated: before=%s after=%s err=%v", before, after, err)
		}
	})
	return s, resolver
}

func TestR844ReceiptSinglePreparedRefusalKeepsAuthorizedRetry(t *testing.T) {
	s, resolver := r844ReceiptHostFixture(t, 0)
	scope := r844ReceiptScope()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	const command = "printf refused > receipt-must-not-run"
	_, err := s.Call(ctx, scope, "bash", map[string]any{
		"workspace_id": resolver.workspace.ID, "command": command, "timeout_ms": 1000,
	})
	if len(s.Snapshot()) != 1 {
		t.Fatalf("known refusal lost its retained admission: %d", len(s.Snapshot()))
	}
	record := r844ReceiptRecord(t, s, command)
	if !errors.Is(err, process.ErrHostResourcesUnavailable) {
		t.Errorf("original host refusal cause was lost: %v", err)
	}
	r844ReceiptError(t, s, err, "host_resources_unavailable", []Record{record}, nil, record.ID, true)
	if _, statErr := os.Stat(filepath.Join(resolver.workspace.Root, "receipt-must-not-run")); !os.IsNotExist(statErr) {
		t.Errorf("known unlaunched command executed: %v", statErr)
	}
}

func TestR844ReceiptPartialAdmissionPreservesOrderMultiplicityAndLabels(t *testing.T) {
	s, resolver := r844ReceiptHostFixture(t, 1)
	scope := r844ReceiptScope()
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	const refused = "printf refused > receipt-must-not-run"
	_, err := s.Call(ctx, scope, "start_jobs", map[string]any{
		"workspace_id": resolver.workspace.ID,
		"commands": []any{
			map[string]any{"command": r844ReceiptGate, "label": "first label"},
			map[string]any{"command": r844ReceiptGate, "label": "same admission, second label"},
			map[string]any{"command": refused, "label": "refused label"},
		},
	})
	if len(s.Snapshot()) != 2 {
		t.Fatalf("duplicate command or refused admission changed record census: %d", len(s.Snapshot()))
	}
	accepted := r844ReceiptRecord(t, s, r844ReceiptGate)
	rejected := r844ReceiptRecord(t, s, refused)
	if accepted.ID == rejected.ID {
		t.Fatal("different admitted commands share one ID")
	}
	if !errors.Is(err, process.ErrHostResourcesUnavailable) {
		t.Errorf("original host refusal cause was lost: %v", err)
	}
	var typed *wire.Error
	if errors.As(err, &typed) && typed == process.ErrHostResourcesUnavailable {
		t.Error("handler returned the shared singleton instead of a per-call public receipt")
	}
	data := r844ReceiptError(t, s, err, "host_resources_unavailable",
		[]Record{accepted, accepted, rejected},
		[]string{"first label", "same admission, second label", "refused label"}, rejected.ID, false)
	message, _ := data["error"].(string)
	lower := strings.ToLower(message)
	if strings.Contains(lower, "no command was started") || strings.Contains(lower, "retry the same call shortly") {
		t.Errorf("partial-call message denied earlier execution or advised unsafe replay: %s", message)
	}
	if !strings.Contains(lower, "do not retry the whole call unchanged") {
		t.Errorf("partial-call message omitted whole-call retry prohibition: %s", message)
	}
	if got, readErr := os.ReadFile(filepath.Join(resolver.workspace.Root, "receipt-started")); readErr != nil || string(got) != "once\n" {
		t.Errorf("deduplicated accepted command did not execute exactly once: %q %v", got, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(resolver.workspace.Root, "receipt-must-not-run")); !os.IsNotExist(statErr) {
		t.Errorf("refused command executed: %v", statErr)
	}
	if got, readErr := os.ReadFile(filepath.Join(resolver.workspace.Root, "receipt-host", "begins")); readErr != nil || string(got) != strconv.Itoa(2)+"\n" {
		t.Errorf("duplicate command allocated again or refused command was retried: %q %v", got, readErr)
	}
}

func TestR844ReceiptLaterPreAdmissionFailureKeepsAcceptedIDsAndTypedData(t *testing.T) {
	s, _, resolver := newFixture(t)
	r844ReceiptRelease(t, resolver)
	scope := r844ReceiptScope()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const missing = "job_r844_missing_input"
	const refused = "printf refused > receipt-must-not-run"
	_, err := s.Call(ctx, scope, "start_jobs", map[string]any{
		"workspace_id": resolver.workspace.ID,
		"commands": []any{
			map[string]any{"command": r844ReceiptGate, "label": "retained first"},
			map[string]any{"command": refused, "input_job_ids": []string{missing}, "label": "never admitted"},
		},
	})
	if len(s.Snapshot()) != 1 {
		t.Fatalf("pre-admission failure created or discarded a record: %d", len(s.Snapshot()))
	}
	accepted := r844ReceiptRecord(t, s, r844ReceiptGate)
	data := r844ReceiptError(t, s, err, "job_not_found", []Record{accepted}, []string{"retained first"}, "", false)
	if !reflect.DeepEqual(data["known_job_ids"], []any{accepted.ID}) {
		t.Errorf("unrelated typed error metadata was lost: %#v", data["known_job_ids"])
	}
	if message, _ := data["error"].(string); !strings.Contains(message, "Unknown job_id: "+missing) {
		t.Errorf("original refusal explanation was lost: %s", message)
	}
	if _, statErr := os.Stat(filepath.Join(resolver.workspace.Root, "receipt-must-not-run")); !os.IsNotExist(statErr) {
		t.Errorf("unadmitted command executed: %v", statErr)
	}
}

func TestR844ReceiptOutputReadFailureKeepsAcceptedJob(t *testing.T) {
	for _, tool := range []string{"bash", "start_jobs"} {
		t.Run(tool, func(t *testing.T) {
			s, _, resolver := newFixture(t)
			r844ReceiptRelease(t, resolver)
			scope := r844ReceiptScope()
			base, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var accepted Record
			ctx := &waitEntryContext{Context: base, onWait: func() {
				accepted = r844ReceiptRecord(t, s, r844ReceiptGate)
				waitStartupFile(t, filepath.Join(resolver.workspace.Root, "receipt-started"))
				rendered := filepath.Join(s.outputDir(accepted), "stdout.log")
				if err := os.WriteFile(rendered, []byte{0xff, 0xff, 0xff, 0xff, 0xff}, 0600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.WriteFile(rendered, nil, 0600) })
			}}
			args := map[string]any{"workspace_id": resolver.workspace.ID}
			var labels []string
			if tool == "bash" {
				args["command"], args["timeout_ms"] = r844ReceiptGate, 1000
			} else {
				args["commands"] = []any{map[string]any{"command": r844ReceiptGate, "label": "output owner"}}
				labels = []string{"output owner"}
			}
			_, err := s.Call(ctx, scope, tool, args)
			if accepted.ID == "" {
				t.Fatal("controlled output failure never reached an accepted wait")
			}
			accepted = r844ReceiptRecord(t, s, r844ReceiptGate)
			data := r844ReceiptError(t, s, err, "job_output_invalid", []Record{accepted}, labels, accepted.ID, false)
			if message, _ := data["error"].(string); !strings.Contains(message, "Invalid rendered output") {
				t.Errorf("output error cause was replaced: %s", message)
			}
		})
	}
}

func TestR844ReceiptWaitCancellationKeepsAcceptedJobWithoutReplay(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "launched", true: "uncertain-launching"}[uncertain], func(t *testing.T) {
			s, _, resolver := newFixture(t)
			r844ReceiptRelease(t, resolver)
			scope := r844ReceiptScope()
			base, cancel := context.WithCancel(t.Context())
			defer cancel()
			var accepted Record
			ctx := &waitEntryContext{Context: base, onWait: func() {
				accepted = r844ReceiptRecord(t, s, r844ReceiptGate)
				waitStartupFile(t, filepath.Join(resolver.workspace.Root, "receipt-started"))
				if uncertain {
					var err error
					accepted, err = s.update(accepted.ID, func(next *Record) bool {
						next.PID, next.ProcessIdentity, next.LaunchState = 0, "", "launching"
						return true
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				cancel()
			}}
			_, err := s.Call(ctx, scope, "bash", map[string]any{
				"workspace_id": resolver.workspace.ID, "command": r844ReceiptGate, "timeout_ms": 1000,
			})
			if accepted.ID == "" {
				t.Fatal("cancellation did not occur after admission")
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("original cancellation cause was lost: %v", err)
			}
			accepted = r844ReceiptRecord(t, s, r844ReceiptGate)
			r844ReceiptError(t, s, err, "job_call_failed", []Record{accepted}, nil, accepted.ID, false)
			if uncertain && accepted.LaunchState != "launching" {
				t.Errorf("uncertain launch state was rewritten: %q", accepted.LaunchState)
			}
		})
	}
}

func TestR844ReceiptSuccessfulBashAndJobsExposePublicLaunchState(t *testing.T) {
	s, _, resolver := newFixture(t)
	scope := r844ReceiptScope()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := s.Call(ctx, scope, "bash", map[string]any{
		"workspace_id": resolver.workspace.ID, "command": "printf receipt", "timeout_ms": 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	bash := resultData(t, result)
	id, _ := bash["job_id"].(string)
	record, ok := s.Get(id, resolver.workspace.ID)
	if !ok {
		t.Fatal("successful Bash result lost its accepted record")
	}
	if bash["launch_state"] != record.LaunchState || record.LaunchState != "launched" {
		t.Errorf("Bash omitted or misstated launch_state: %#v, retained %q", bash["launch_state"], record.LaunchState)
	}
	r844ReceiptPublic(t, bash)
	result, err = s.Call(ctx, scope, "jobs", map[string]any{
		"workspace_id": resolver.workspace.ID, "job_ids": []string{id}, "wait_for": "all", "wait_ms": 3000,
	})
	if err != nil {
		t.Fatal(err)
	}
	collected := resultData(t, result)
	views, ok := collected["jobs"].([]any)
	if !ok || len(views) != 1 {
		t.Fatalf("normal collection lost the accepted job: %#v", collected)
	}
	view, ok := views[0].(map[string]any)
	if !ok {
		t.Fatalf("normal collection is not a public job view: %#v", views[0])
	}
	if view["job_id"] != id || view["launch_state"] != "launched" || view["status"] != "succeeded" {
		t.Errorf("normal collection lost launch or execution identity: %#v", view)
	}
	r844ReceiptPublic(t, collected)
}

func TestR844ReceiptPreAdmissionRefusalDoesNotInventReceipts(t *testing.T) {
	s, _, resolver := newFixture(t)
	_, err := s.Call(t.Context(), r844ReceiptScope(), "bash", map[string]any{
		"workspace_id": resolver.workspace.ID, "command": "printf never",
		"input_job_ids": []string{"job_r844_missing_input"}, "timeout_ms": 1000,
	})
	var typed *wire.Error
	if !errors.As(err, &typed) || typed.Code != "job_not_found" {
		t.Fatalf("pre-admission error changed: %v", err)
	}
	data := resultData(t, wire.ErrorResult(err))
	for _, key := range []string{"job_id", "job_ids", "jobs"} {
		if value, present := data[key]; present {
			t.Errorf("unaccepted call invented %s: %#v", key, value)
		}
	}
	if len(s.Snapshot()) != 0 || !reflect.DeepEqual(data["known_job_ids"], []any{}) {
		t.Errorf("pre-admission refusal created work or changed original metadata: %#v", data)
	}
}
