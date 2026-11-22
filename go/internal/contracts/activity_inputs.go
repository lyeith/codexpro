package contracts

import (
	"github.com/google/jsonschema-go/jsonschema"
)

type activityListInput struct {
	AfterSequence  int    `json:"after_sequence,omitempty" jsonschema:"Read actions after this durable source sequence. Omit to tail the latest actions."`
	Limit          int    `json:"limit,omitempty" jsonschema:"Maximum matching actions. Default: 100."`
	MutatingOnly   bool   `json:"mutating_only,omitempty" jsonschema:"Return only project/workspace/file/command mutations."`
	ToolName       string `json:"tool_name,omitempty" jsonschema:"Exact effective CodexPro tool-name filter."`
	OperationClass string `json:"operation_class,omitempty" jsonschema:"Operation-class filter."`
	Status         string `json:"status,omitempty" jsonschema:"Outcome filter."`
	ProjectID      string `json:"project_id,omitempty" jsonschema:"Exact project-id filter."`
	WorkspaceID    string `json:"workspace_id,omitempty" jsonschema:"Exact workspace-id filter."`
	toolInput
}

type activityGetInput struct {
	ActionID string `json:"action_id" jsonschema:"Stable CodexPro action id returned by activity_list."`
	toolInput
}

type activityExportInput struct {
	AfterSequence  int    `json:"after_sequence" jsonschema:"Last acknowledged source sequence. The export starts after this sequence."`
	Limit          int    `json:"limit,omitempty" jsonschema:"Maximum matching actions before the byte budget is applied. Default: 100."`
	Format         string `json:"format,omitempty" jsonschema:"Export encoding. Default: jsonl."`
	MutatingOnly   bool   `json:"mutating_only,omitempty" jsonschema:"Export only project/workspace/file/command mutations."`
	ToolName       string `json:"tool_name,omitempty" jsonschema:"Exact effective CodexPro tool-name filter."`
	OperationClass string `json:"operation_class,omitempty" jsonschema:"Operation-class filter."`
	Status         string `json:"status,omitempty" jsonschema:"Outcome filter."`
	ProjectID      string `json:"project_id,omitempty" jsonschema:"Exact project-id filter."`
	WorkspaceID    string `json:"workspace_id,omitempty" jsonschema:"Exact workspace-id filter."`
	toolInput
}

func activityListDefinition() definition {
	return defineTool[activityListInput]("activity_list", "List CodexPro Debug Activity", readOnlyTool,
		"List bounded debug-namespace codexpro.action.v1 engineering records. Omit after_sequence to tail the "+
			"most recent actions, or pass the last acknowledged sequence to consume forward without scanning "+
			"ChatGPT history or Git logs. These are diagnostics, not day-to-day operations. Public action records "+
			"omit payload bodies, command/query text, tokens, and raw output; exact Bash scripts are retained "+
			"only for the authenticated activity dashboard and are stripped from activity_list, activity_get, and "+
			"activity_export.", func(s *jsonschema.Schema) {
			integerRange(s.Properties["after_sequence"], 0, maxSafeInteger)
			integerRange(s.Properties["limit"], 1, 500)
			s.Properties["tool_name"].MaxLength = new(120)
			allowValues(s.Properties["operation_class"], "read", "write", "execute", "git", "lifecycle", "analysis", "handoff", "administrative")
			allowValues(s.Properties["status"], "succeeded", "failed", "timed_out", "cancelled", "blocked")
			s.Properties["project_id"].MaxLength = new(160)
			s.Properties["workspace_id"].MaxLength = new(160)
		})
}

func activityGetDefinition() definition {
	return defineTool[activityGetInput]("activity_get", "Get CodexPro Debug Action", readOnlyTool,
		"Get one debug-namespace codexpro.action.v1 engineering record by its stable source-owned action id.", func(s *jsonschema.Schema) {
			s.Properties["action_id"].Pattern = "^cpa_[a-f0-9]{32}$"
		})
}

func activityStatusDefinition() definition {
	return defineTool[toolInput]("activity_status", "CodexPro Debug Activity Status", readOnlyTool,
		"Read the debug action-journal cursor/status boundary, including retained and latest sequences, "+
			"malformed records, and explicit gap detection.", nil)
}

func activityExportDefinition() definition {
	return defineTool[activityExportInput]("activity_export", "Export CodexPro Debug Activity", readOnlyTool,
		"Export a bounded page of debug-namespace codexpro.action.v1 engineering records as JSONL or JSON for "+
			"a diagnostic consumer such as Ops Inbox. The export uses the same durable sequence cursor and "+
			"metadata-only redaction boundary as activity_list, and never exposes the journal filesystem path.", func(s *jsonschema.Schema) {
			integerRange(s.Properties["after_sequence"], 0, maxSafeInteger)
			integerRange(s.Properties["limit"], 1, 500)
			allowValues(s.Properties["format"], "jsonl", "json")
			s.Properties["tool_name"].MaxLength = new(120)
			allowValues(s.Properties["operation_class"], "read", "write", "execute", "git", "lifecycle", "analysis", "handoff", "administrative")
			allowValues(s.Properties["status"], "succeeded", "failed", "timed_out", "cancelled", "blocked")
			s.Properties["project_id"].MaxLength = new(160)
			s.Properties["workspace_id"].MaxLength = new(160)
		})
}
