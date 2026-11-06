package audit

import "github.com/lyeith/codexpro/go/internal/wire"

// UnsuccessfulCall shares the existing terminal classification with lightweight
// metrics. It inspects the returned result only for this call; it neither records
// nor retains any result, command, argument, or audit event.
func UnsuccessfulCall(tool string, result wire.Result, err error) bool {
	status, _ := classify(RecordInput{ToolName: tool, Result: result, Error: err})
	return status != "succeeded"
}
