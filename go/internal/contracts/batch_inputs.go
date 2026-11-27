package contracts

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type batchInput struct {
	workspaceInput
	Operations      []batchOperationInput `json:"operations,omitempty" jsonschema:"One to 24 inline operations. Cannot be combined with path. Use direct tools for 1-2 simple read-only calls. Serial write/edit children must target distinct files; combine same-file changes into one edit."`
	Path            string                `json:"path,omitempty" jsonschema:"Workspace-relative JSON batch file to execute. Cannot be combined with operations; mode and continue_on_error come from the file."`
	From            string                `json:"from,omitempty" jsonschema:"Stored batches only: start at this operation id, inclusive."`
	FromIndex       int                   `json:"from_index,omitempty" jsonschema:"Stored batches only: start at this zero-based operation index, inclusive. Cannot be combined with from."`
	Mode            string                `json:"mode,omitempty" jsonschema:"serial by default. parallel is for 3+ independent read-only operations and is limited to parallel-safe tools."`
	ContinueOnError bool                  `json:"continue_on_error,omitempty" jsonschema:"Continue after child failures. Read-only batches only; default false."`
	Persist         bool                  `json:"persist,omitempty" jsonschema:"Inline batches only. Defaults true when the batch contains Bash verification and false for 1-2 read-only operations or other non-verification workflows."`
	SessionID       string                `json:"session_id,omitempty" jsonschema:"Bash session authorization, when required by server_config. Inherited by Bash children and never saved in batch definitions."`
	Checkpoint      batchCheckpointInput  `json:"checkpoint,omitempty" jsonschema:"Retired managed checkpoint argument."`
	toolInput
}

type batchOperationInput struct {
	ID   string         `json:"id,omitempty"`
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
}

type batchCheckpointInput struct {
	ExpectedRevision int                  `json:"expected_revision"`
	Summary          string               `json:"summary"`
	NextAction       string               `json:"next_action"`
	Blockers         []string             `json:"blockers,omitempty"`
	Decisions        []string             `json:"decisions,omitempty"`
	FailedApproaches []string             `json:"failed_approaches,omitempty"`
	EvidenceIDs      []string             `json:"evidence_ids,omitempty"`
	Todos            []batchTodoInput     `json:"todos,omitempty"`
	TodoUpdates      []batchTodoInput     `json:"todo_updates,omitempty" jsonschema:"Upsert this page of todos by id, preserving all others. Use instead of todos for larger plans; there is no total-plan count limit."`
	Documents        []batchDocumentInput `json:"documents,omitempty" jsonschema:"Save up to twelve memory documents atomically with todos and handoff; existing documents require document_revision."`
}

type batchTodoInput struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Acceptance  string   `json:"acceptance,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
}

type batchDocumentInput struct {
	DocumentID       string   `json:"document_id,omitempty"`
	DocumentRevision int      `json:"document_revision,omitempty"`
	Kind             string   `json:"kind,omitempty"`
	Title            string   `json:"title"`
	Content          string   `json:"content"`
	ReferencePath    string   `json:"reference_path,omitempty" jsonschema:"Existing workspace file to register with its source observation."`
	TodoIDs          []string `json:"todo_ids,omitempty"`
}

func batchDefinition() definition {
	return defineTool[batchInput]("batch", "Batch Operations", openWorldMutation,
		"Run a meaningful multi-step workflow, not a wrapper around ordinary calls. Use direct tools for one "+
			"or two simple read-only calls and for a one-file mutation followed only by read/show_changes. A "+
			"serial batch may contain several write/edit children only when each targets a distinct file; combine "+
			"all same-file hunks into one edit. apply_patch remains exclusive because one patch may already span "+
			"files. Use batch for three or more related reads, coordinated distinct-file mutations, short Bash "+
			"checks, launching background verification with start_jobs after edits (returning job_ids immediately "+
			"to collect later with jobs), or a sequence deliberately retained for resume. Avoid long synchronous "+
			"bash commands inside batch so a single request does not exceed per-request HTTP timeouts. Inline "+
			"Bash verification workflows persist by default; other inline batches are one-shot unless persist=true.", func(s *jsonschema.Schema) {
			arrayRange(s.Properties["operations"], 1, wire.MaxBatchOperations)
			s.Properties["operations"].Items.AdditionalProperties = closedObject()
			s.Properties["operations"].Items.Properties["id"].Pattern = "^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$"
			allowValues(s.Properties["operations"].Items.Properties["tool"], "tree", "search", "ast_grep", "read", "inspect_workspace", "show_changes", "write", "edit", "apply_patch", "bash", "start_jobs")
			s.Properties["operations"].Items.Properties["args"].AdditionalProperties = &jsonschema.Schema{}
			s.Properties["operations"].Items.Properties["args"].PropertyNames = &jsonschema.Schema{Type: "string"}
			s.Properties["from"].Pattern = "^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$"
			integerRange(s.Properties["from_index"], 0, wire.MaxBatchOperations-1)
			allowValues(s.Properties["mode"], "serial", "parallel")
			configureBatchCheckpoint(s.Properties["checkpoint"])
		})
}

func configureBatchCheckpoint(s *jsonschema.Schema) {
	s.AdditionalProperties = closedObject()
	integerRange(s.Properties["expected_revision"], 1, maxSafeInteger)
	stringRange(s.Properties["summary"], 1, 2000)
	stringRange(s.Properties["next_action"], 1, 2000)
	s.Properties["blockers"].MaxItems = new(30)
	stringRange(s.Properties["blockers"].Items, 1, 2000)
	s.Properties["decisions"].MaxItems = new(30)
	stringRange(s.Properties["decisions"].Items, 1, 2000)
	s.Properties["failed_approaches"].MaxItems = new(30)
	stringRange(s.Properties["failed_approaches"].Items, 1, 2000)
	s.Properties["evidence_ids"].MaxItems = new(50)
	stringRange(s.Properties["evidence_ids"].Items, 1, 160)
	configureBatchTodos(s.Properties["todos"])
	configureBatchTodos(s.Properties["todo_updates"])
	s.Properties["documents"].MaxItems = new(12)
	s.Properties["documents"].Items.AdditionalProperties = closedObject()
	stringRange(s.Properties["documents"].Items.Properties["document_id"], 1, 160)
	integerRange(s.Properties["documents"].Items.Properties["document_revision"], 1, maxSafeInteger)
	allowValues(s.Properties["documents"].Items.Properties["kind"], "note", "decision", "question", "project_memory")
	stringRange(s.Properties["documents"].Items.Properties["title"], 1, 300)
	s.Properties["documents"].Items.Properties["content"].MaxLength = new(131072)
	stringRange(s.Properties["documents"].Items.Properties["reference_path"], 1, 2000)
	s.Properties["documents"].Items.Properties["todo_ids"].MaxItems = new(100)
	stringRange(s.Properties["documents"].Items.Properties["todo_ids"].Items, 1, 160)
}

func configureBatchTodos(s *jsonschema.Schema) {
	s.MaxItems = new(200)
	stringRange(s.Items.Properties["id"], 1, 160)
	stringRange(s.Items.Properties["title"], 1, 500)
	allowValues(s.Items.Properties["status"], "pending", "in_progress", "blocked", "done", "skipped")
	stringRange(s.Items.Properties["acceptance"], 1, 2000)
	stringRange(s.Items.Properties["reason"], 1, 2000)
	s.Items.Properties["evidence_ids"].MaxItems = new(30)
	s.Items.Properties["evidence_ids"].Default = json.RawMessage("[]")
	stringRange(s.Items.Properties["evidence_ids"].Items, 1, 160)
}
