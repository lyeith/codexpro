package contracts

import (
	"github.com/google/jsonschema-go/jsonschema"
)

type readInput struct {
	workspaceInput
	Path      string `json:"path" jsonschema:"File path relative to workspace root."`
	StartLine int    `json:"start_line,omitempty" jsonschema:"First line to read. Default: 1."`
	EndLine   int    `json:"end_line,omitempty" jsonschema:"Last requested line. A page may stop earlier at max_bytes; continue with next_start_line."`
	MaxBytes  int    `json:"max_bytes,omitempty" jsonschema:"Maximum returned page bytes including line numbers. Capped by server config; large source files can be paged."`
	toolInput
}

type viewImageInput struct {
	workspaceInput
	Path     string `json:"path" jsonschema:"Image path relative to workspace root."`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"Maximum image bytes. Default: at least 1 MB, capped at 2 MB."`
	toolInput
}

type writeInput struct {
	workspaceInput
	Path           string `json:"path" jsonschema:"File path relative to workspace root."`
	Content        string `json:"content" jsonschema:"Complete file contents to write."`
	CreateDirs     bool   `json:"create_dirs,omitempty" jsonschema:"Create parent directories if missing. Default: true."`
	Overwrite      bool   `json:"overwrite,omitempty" jsonschema:"Allow overwriting existing files. Default: true."`
	ExpectedSHA256 string `json:"expected_sha256,omitempty" jsonschema:"Optional SHA-256 from read. Fails instead of overwriting if another session changed the file."`
	toolInput
}

type editInput struct {
	workspaceInput
	Path    string `json:"path" jsonschema:"File path relative to workspace root."`
	EditTag string `json:"edit_tag" jsonschema:"Four-character edit tag returned by read. The connector resolves it to retained full content and rejects stale or colliding snapshots."`
	Edits   []any  `json:"edits" jsonschema:"Line operations against the original tagged snapshot. Targets must have been displayed by read."`
	toolInput
}

type editReplaceInput struct {
	Op        string `json:"op"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line,omitempty"`
	Content   string `json:"content"`
}

type editDeleteInput struct {
	Op        string `json:"op"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line,omitempty"`
}

type editInsertInput struct {
	Op      string `json:"op"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

type applyPatchInput struct {
	workspaceInput
	Patch string `json:"patch" jsonschema:"Git unified diff or native *** Begin Patch text. Paths must stay inside the workspace and avoid blocked paths."`
	toolInput
}

type importFileInput struct {
	workspaceInput
	File           attachmentInput `json:"file" jsonschema:"ChatGPT Apps SDK file reference from openai/fileParams."`
	Destination    string          `json:"destination" jsonschema:"Destination path relative to the workspace root."`
	Overwrite      bool            `json:"overwrite,omitempty" jsonschema:"Replace an existing destination file. Default: false."`
	ExpectedSHA256 string          `json:"expected_sha256,omitempty" jsonschema:"Optional SHA-256 of the attachment bytes. Import fails on mismatch."`
	toolInput
}

type attachmentInput struct {
	DownloadURL string `json:"download_url" jsonschema:"Temporary HTTPS download URL provided by ChatGPT."`
	FileID      string `json:"file_id" jsonschema:"ChatGPT file id for this attachment."`
	MIMEType    string `json:"mime_type,omitempty" jsonschema:"Optional MIME type declared by ChatGPT."`
	FileName    string `json:"file_name,omitempty" jsonschema:"Optional original file name declared by ChatGPT."`
}

type showChangesInput struct {
	workspaceInput
	Path         string `json:"path,omitempty" jsonschema:"Optional file path relative to workspace root."`
	Staged       bool   `json:"staged,omitempty" jsonschema:"Show staged diff. Default: false."`
	IncludeDiff  bool   `json:"include_diff,omitempty" jsonschema:"Include the unified diff. Default: true."`
	Since        string `json:"since,omitempty" jsonschema:"workspace (default) reports the full current state; last_shown suppresses a diff already shown by the previous review."`
	MarkReviewed bool   `json:"mark_reviewed,omitempty" jsonschema:"Update the last-shown review checkpoint after this call. Default: true."`
	toolInput
}

type commitChangesInput struct {
	workspaceInput
	Message string   `json:"message" jsonschema:"Commit message. First line is the subject."`
	Paths   []string `json:"paths,omitempty" jsonschema:"Files to stage and commit, relative to the workspace root. Default: every changed and untracked file."`
	toolInput
}

func readDefinition() definition {
	return defineTool[readInput]("read", "Read File", readOnlyTool,
		"Read a bounded page of a text file with line numbers and a four-character edit_tag. The whole source "+
			"must fit the configured source-size limit before line selection; start_line, end_line and max_bytes "+
			"do not raise that limit. max_bytes limits returned text. Continue a paginated result with "+
			"next_start_line. Before editing, read every range you plan to change, then send all intended "+
			"same-file changes in one combined multi-hunk edit. Re-read after any mutation before another tagged "+
			"edit; otherwise avoid redundant rereads when the returned diff is sufficient.", func(s *jsonschema.Schema) {
			lineNumber(s.Properties["start_line"])
			lineNumber(s.Properties["end_line"])
			integerRange(s.Properties["max_bytes"], 1000, 2000000)
		})
}

func viewImageDefinition() definition {
	return defineTool[viewImageInput]("view_image", "View Image", readOnlyTool,
		"Inspect a PNG, JPEG, GIF, or WebP image from the active workspace. Returns native MCP image content "+
			"plus dimensions and SHA-256.", func(s *jsonschema.Schema) {
			integerRange(s.Properties["max_bytes"], 4096, 2000000)
		})
}

func writeDefinition() definition {
	return defineTool[writeInput]("write", "Write File", localDestructiveTool,
		"Create a new text file, or replace a whole file when a tagged edit is not practical (prefer edit for "+
			"changes to an existing file). Returns a unified diff. When overwriting a file another session may "+
			"have touched, pass expected_sha256 (the SHA-256 printed by read) so the write fails instead of "+
			"clobbering.", func(s *jsonschema.Schema) {
			s.Properties["expected_sha256"].Pattern = "^[a-f0-9]{64}$"
		})
}

func editDefinition() definition {
	return defineTool[editInput]("edit", "Edit File", localDestructiveTool,
		"Preferred tool for every one-file change, including many non-adjacent hunks. Immediately before "+
			"editing, use the four-character edit_tag from a read or from a complete current-file search context "+
			"that displayed every targeted line. Submit all intended changes for that file in a single tagged "+
			"multi-hunk call, where every operation addresses the original tagged snapshot. Do not reuse the tag "+
			"after any mutation. After an error, do not retry unchanged: follow error_code and recovery.", func(s *jsonschema.Schema) {
			s.Properties["edit_tag"].Pattern = "^[0-9A-F]{4}$"
			configureEdits(s.Properties["edits"])
		})
}

func configureEdits(s *jsonschema.Schema) {
	arrayRange(s, 1, 100)
	s.Items = &jsonschema.Schema{OneOf: []*jsonschema.Schema{
		editOperationSchema[editReplaceInput]("replace"),
		editOperationSchema[editDeleteInput]("delete"),
		editOperationSchema[editInsertInput]("insert_before"),
		editOperationSchema[editInsertInput]("insert_after"),
	}}
}

func editOperationSchema[T any](op string) *jsonschema.Schema {
	s := inferSchema[T]()
	s.Properties["op"].Const = new(any(op))
	for _, field := range []string{"line", "start_line", "end_line"} {
		if p := s.Properties[field]; p != nil {
			lineNumber(p)
		}
	}
	return s
}

func applyPatchDefinition() definition {
	return defineTool[applyPatchInput]("apply_patch", "Apply Patch", localDestructiveTool,
		"Apply a standard unified diff for a deliberate multi-file change or a file that tagged edit cannot "+
			"handle. Use edit for every single-file change. Accepts raw Git unified diffs or native *** Begin "+
			"Patch syntax (add/update/delete/move). Both formats use the same guarded preflight. Never resend a "+
			"failed patch unchanged—read current targets and regenerate it.", nil)
}

func importFileDefinition() definition {
	return defineTool[importFileInput]("import_file", "Import Attachment File", localDestructiveTool,
		"Import a ChatGPT Apps SDK attachment into the workspace. Accepts only a platform file object with "+
			"download_url and file_id. Not a general URL downloader. Overwrite is off by default.", func(s *jsonschema.Schema) {
			s.Properties["expected_sha256"].Pattern = "^[a-f0-9]{64}$"
		})
}

func showChangesDefinition() definition {
	return defineTool[showChangesInput]("show_changes", "Show Changes", readOnlyTool,
		"Review the working tree: git status, diff stats, the unified diff and change-impact analysis in one "+
			"result. Use it instead of bash git status/diff. Every call reports the full current state; pass "+
			"since=last_shown to get only what changed since the previous review.", func(s *jsonschema.Schema) {
			allowValues(s.Properties["since"], "last_shown", "workspace")
		})
}

func commitChangesDefinition() definition {
	return defineTool[commitChangesInput]("commit_changes", "Commit Changes", openWorldMutation,
		"Create a git commit in the workspace. Stages the given paths, or every changed and untracked file "+
			"when paths is omitted, then commits with the message. Paths blocked by safety rules (secrets, build "+
			"artifacts) are never staged. Use after show_changes when the user asks for a commit; it does not "+
			"push. Returns the full commit SHA and a post-commit working-tree status snapshot.", func(s *jsonschema.Schema) {
			stringRange(s.Properties["message"], 1, 8000)
			arrayRange(s.Properties["paths"], 1, 200)
			s.Properties["paths"].Items.MinLength = new(1)
		})
}
