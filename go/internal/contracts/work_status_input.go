package contracts

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
)

type workStatusInput struct {
	Action           string `json:"action,omitempty"`
	RunID            string `json:"run_id,omitempty" jsonschema:"Full run ID, unique case-sensitive run_ prefix (at least 8 characters after run_), or dashboard prefix…suffix form. Ambiguous references fail; replies retain full IDs."`
	ProjectID        string `json:"project_id,omitempty"`
	Section          string `json:"section,omitempty"`
	Claimed          bool   `json:"claimed,omitempty"`
	NeedsAttention   bool   `json:"needs_attention,omitempty"`
	State            string `json:"state,omitempty"`
	DocumentID       string `json:"document_id,omitempty"`
	DocumentRevision int    `json:"document_revision,omitempty"`
	OperationID      string `json:"operation_id,omitempty"`
	Query            string `json:"query,omitempty"`
	Offset           int    `json:"offset,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	MaxBytes         int    `json:"max_bytes,omitempty"`
	AfterSequence    int    `json:"after_sequence,omitempty"`
	callerInput
}

func workStatusDefinition() definition {
	return defineTool[workStatusInput]("work_status", "Inspect work runs", readOnlyTool,
		"Read run state, tasks, documents and operation receipts. Paginate sections and documents for full detail.",
		func(s *jsonschema.Schema) {
			s.Properties["action"].Default = json.RawMessage("\"list\"")
			allowValues(s.Properties["action"], "list", "get", "read_document", "search_memory", "history", "operation")
			stringRange(s.Properties["run_id"], 1, 160)
			stringRange(s.Properties["project_id"], 1, 160)
			s.Properties["section"].Default = json.RawMessage("\"packet\"")
			allowValues(s.Properties["section"], "packet", "summary", "todos", "acceptance", "documents", "iterations", "operations", "jobs", "activity", "source")
			allowValues(s.Properties["state"], "draft", "provisioning", "ready", "active", "closing", "waiting", "recovering", "verifying", "blocked", "paused", "complete", "cancelled")
			stringRange(s.Properties["document_id"], 1, 160)
			integerRange(s.Properties["document_revision"], 1, maxSafeInteger)
			stringRange(s.Properties["operation_id"], 1, 160)
			stringRange(s.Properties["query"], 1, 500)
			s.Properties["offset"].Default = json.RawMessage("0")
			integerRange(s.Properties["offset"], 0, maxSafeInteger)
			s.Properties["limit"].Default = json.RawMessage("10")
			integerRange(s.Properties["limit"], 1, 50)
			integerRange(s.Properties["max_bytes"], 512, 32000)
			integerRange(s.Properties["after_sequence"], 0, maxSafeInteger)
		})
}
