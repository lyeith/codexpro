package activity

import (
	"encoding/json"
	"math"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// unsuccessfulCall reads only terminal flags and the bash owner's scalar
// outcome. It never examines output, error messages, audit data, or nested jobs.
// A successful jobs read or asynchronous launch is not a failed tool call.
func unsuccessfulCall(tool string, result wire.Result, err error) bool {
	if err != nil || result.IsError {
		return true
	}
	if tool != "bash" && tool != "bash_job" {
		return false
	}

	data := result.StructuredContent
	timedOut := data["timed_out"]
	if timedOut == nil {
		timedOut = data["timedOut"]
	}
	if timed, _ := timedOut.(bool); timed {
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

	exit, hasExit := data["exit_code"]
	if exit == nil {
		exit = data["exitCode"]
	}
	if nonzeroTerminalExit(exit) {
		return true
	}
	// The bash owner always returns exit_code; nil is unsuccessful once the
	// call has settled. A running job was handled above.
	return hasExit && data["exit_code"] == nil
}

func nonzeroTerminalExit(value any) bool {
	switch exit := value.(type) {
	case int:
		return exit != 0
	case int32:
		return exit != 0
	case int64:
		return exit != 0
	case float64:
		return exit != 0 && !math.IsNaN(exit) && !math.IsInf(exit, 0)
	case json.Number:
		// Exit codes are small scalars. Bound even malformed compatibility
		// values before parsing, independently of any command output size.
		if len(exit) > 32 {
			return false
		}
		number, err := exit.Float64()
		return err == nil && number != 0 && !math.IsNaN(number) && !math.IsInf(number, 0)
	default:
		return false
	}
}
