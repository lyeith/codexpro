package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestUnsuccessfulCallTerminalScalars(t *testing.T) {
	tests := []struct {
		name, tool string
		data       map[string]any
		isError    bool
		err        error
		want       bool
	}{
		{name: "successful tool", tool: "read"},
		{name: "reported failure", tool: "edit", isError: true, want: true},
		{name: "policy refusal", tool: "write", err: wire.Errorf("policy_blocked", "refused"), want: true},
		{name: "cancelled", tool: "read", err: context.Canceled, want: true},
		{name: "wrapped cancellation", tool: "read", err: fmt.Errorf("interrupted: %w", context.Canceled), want: true},
		{name: "deadline", tool: "read", err: context.DeadlineExceeded, want: true},
		{name: "successful bash", tool: "bash", data: map[string]any{"job_status": "succeeded", "exit_code": 0, "signal": nil, "timed_out": false}},
		{name: "nonzero exit", tool: "bash", data: map[string]any{"exit_code": 7}, want: true},
		{name: "int32 exit", tool: "bash", data: map[string]any{"exit_code": int32(2)}, want: true},
		{name: "int64 exit", tool: "bash", data: map[string]any{"exit_code": int64(2)}, want: true},
		{name: "decoded numeric exit", tool: "bash", data: map[string]any{"exit_code": float64(2)}, want: true},
		{name: "JSON numeric exit", tool: "bash", data: map[string]any{"exit_code": json.Number("2")}, want: true},
		{name: "zero JSON exit", tool: "bash", data: map[string]any{"exit_code": json.Number("0")}},
		{name: "compatibility exit", tool: "bash", data: map[string]any{"exitCode": 7}, want: true},
		{name: "canonical exit wins", tool: "bash", data: map[string]any{"exit_code": 0, "exitCode": 7}},
		{name: "null terminal exit", tool: "bash", data: map[string]any{"exit_code": nil}, want: true},
		{name: "signal", tool: "bash", data: map[string]any{"exit_code": 0, "signal": "SIGTERM"}, want: true},
		{name: "timeout flag", tool: "bash", data: map[string]any{"timed_out": true}, want: true},
		{name: "compatibility timeout flag", tool: "bash", data: map[string]any{"timedOut": true}, want: true},
		{name: "failed job", tool: "bash", data: map[string]any{"job_status": "failed"}, want: true},
		{name: "timed out job", tool: "bash", data: map[string]any{"job_status": "timed_out"}, want: true},
		{name: "stopped job", tool: "bash", data: map[string]any{"job_status": "stopped"}, want: true},
		{name: "background accepted", tool: "bash", data: map[string]any{"job_status": "running", "exit_code": nil, "signal": nil, "timed_out": false}},
		{name: "timeout overrides running", tool: "bash", data: map[string]any{"job_status": "running", "exit_code": nil, "timed_out": true}, want: true},
		{name: "error flag overrides running", tool: "bash", data: map[string]any{"job_status": "running"}, isError: true, want: true},
		{name: "background receipt scalar", tool: "bash_job", data: map[string]any{"job_status": "failed", "exit_code": 3}, want: true},
		{name: "no terminal fields", tool: "bash"},
		{name: "historical status read", tool: "jobs", data: map[string]any{"job_status": "failed", "exit_code": 2, "signal": "SIGTERM", "timed_out": true}},
		{name: "historical jobs in launch", tool: "start_jobs", data: map[string]any{"jobs": []any{map[string]any{"status": "failed", "exit_code": 2}}}},
		{name: "error text is data", tool: "read", data: map[string]any{"error": "blocked", "error_code": "policy_blocked"}},
		{name: "numeric text is not an exit scalar", tool: "bash", data: map[string]any{"exit_code": "7"}},
		{name: "malformed JSON number", tool: "bash", data: map[string]any{"exit_code": json.Number("not-a-number")}},
		{name: "nonfinite exit", tool: "bash", data: map[string]any{"exit_code": math.Inf(1)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := wire.Result{StructuredContent: test.data, IsError: test.isError}
			if got := unsuccessfulCall(test.tool, result, test.err); got != test.want {
				t.Fatalf("unsuccessfulCall(%q) = %v, want %v", test.tool, got, test.want)
			}
		})
	}
}

type terminalUnreadError struct{}

func (terminalUnreadError) Error() string {
	panic("metrics inspected an error message")
}

type terminalUnreadValue struct{}

func (terminalUnreadValue) String() string {
	panic("metrics formatted a payload")
}

func TestUnsuccessfulCallDoesNotInspectPayloads(t *testing.T) {
	unread := terminalUnreadValue{}
	result := wire.Result{
		Meta:              map[string]any{"private": unread},
		DashboardMetadata: map[string]any{"private": unread},
		Content:           []wire.Content{{"type": "text", "text": unread}},
		StructuredContent: map[string]any{
			"job_status": "succeeded", "exit_code": 0,
			"stdout": unread, "stderr": unread, "error": unread, "error_code": unread,
			"jobs": unread, "command": unread,
		},
	}
	if unsuccessfulCall("bash", result, nil) {
		t.Fatal("unrelated payload affected a successful terminal outcome")
	}
	if !unsuccessfulCall("read", result, terminalUnreadError{}) {
		t.Fatal("an error flag must count without reading its message")
	}
	result.IsError = true
	if !unsuccessfulCall("bash", result, nil) {
		t.Fatal("IsError must count without inspecting payloads")
	}
}

func TestUnsuccessfulCallLargeOutputHasNoClassificationAllocations(t *testing.T) {
	// Uppercase output forces a payload-lowercasing implementation to allocate;
	// the timeout marker also catches treating successful output as an outcome.
	output := strings.Repeat("[CODEXPRO] COMMAND TIMED OUT AFTER 1 MS; BLOCKED; FAILED\n", 1<<16)
	result := wire.Result{
		Meta:              map[string]any{"output": output},
		DashboardMetadata: map[string]any{"output": output},
		Content:           []wire.Content{{"type": "text", "text": output}, {"type": "text", "text": output}},
		StructuredContent: map[string]any{
			"job_status": "succeeded", "exit_code": 0, "signal": nil, "timed_out": false,
			"stdout": output, "stderr": output, "error": output,
		},
	}
	for _, tool := range []string{"read", "bash"} {
		t.Run(tool, func(t *testing.T) {
			var failed bool
			allocations := testing.AllocsPerRun(20, func() {
				failed = unsuccessfulCall(tool, result, nil)
			})
			if failed {
				t.Fatal("successful output text was mistaken for a terminal failure")
			}
			if allocations != 0 {
				t.Fatalf("terminal classification allocated for output: %g allocations/call, want zero", allocations)
			}
		})
	}
}

func TestUnsuccessfulCallBoundsNumericCompatibilityScalar(t *testing.T) {
	// No real exit code needs an unbounded decimal token. The terminal owner
	// still supplies an error flag/status when a tool or command failed.
	result := wire.Result{StructuredContent: map[string]any{"exit_code": json.Number(strings.Repeat("0", 1<<20))}}
	var failed bool
	if allocations := testing.AllocsPerRun(20, func() {
		failed = unsuccessfulCall("bash", result, nil)
	}); allocations != 0 {
		t.Fatalf("oversized scalar allocated: %g allocations/call, want zero", allocations)
	}
	if failed {
		t.Fatal("malformed compatibility metadata manufactured a failure")
	}
}
