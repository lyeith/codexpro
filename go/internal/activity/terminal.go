package activity

import "github.com/lyeith/codexpro/go/internal/wire"

// unsuccessfulCall reads only terminal flags and the bash owner's scalar
// outcome. It never examines output, error messages, audit data, or nested jobs.
// A successful jobs read or asynchronous launch is not a failed tool call.
func unsuccessfulCall(tool string, result wire.Result, err error) bool {
	if err != nil || result.IsError {
		return true
	}
	if tool != "bash" {
		return false
	}

	data := result.StructuredContent
	if timed, _ := data["timed_out"].(bool); timed {
		return true
	}
	switch data["job_status"] {
	case "running":
		return false
	case "failed", "timed_out", "stopped":
		return true
	}
	if signal, _ := data["signal"].(string); signal != "" {
		return true
	}

	// The jobs constructor emits int or nil. Engine redaction converts
	// numeric scalars to float64 before the top-level caller receives them.
	exit, hasExit := data["exit_code"]
	switch code := exit.(type) {
	case int:
		return code != 0
	case float64:
		return code != 0
	default:
		// A running job was handled above; a null settled exit is unsuccessful.
		return hasExit && exit == nil
	}
}
