package contracts

import (
	"github.com/google/jsonschema-go/jsonschema"
)

type bashInput struct {
	workspaceInput
	Command     string   `json:"command" jsonschema:"Command to run."`
	InputJobIDs []string `json:"input_job_ids,omitempty" jsonschema:"Jobs whose retained output this command reads. Pins their logs for this command; IDs must belong to this workspace."`
	SessionID   string   `json:"session_id,omitempty" jsonschema:"Optional bash session id. If configured on the server, a provided value must match it."`
	CWD         string   `json:"cwd,omitempty" jsonschema:"Working directory relative to workspace root. Default: ."`
	TimeoutMS   int      `json:"timeout_ms,omitempty" jsonschema:"How long to wait in ms; server_config reports current bashTimeoutMs and maxBashTimeoutMs. Values above the current maximum are rejected. For intentional background work use start_jobs."`
	OnTimeout   string   `json:"on_timeout,omitempty" jsonschema:"When the command outruns timeout_ms: background keeps it running as a job (default); kill stops it."`
	toolInput
}

type startJobsInput struct {
	workspaceInput
	Commands  []jobCommandInput `json:"commands" jsonschema:"Commands started in order, bounded by the current maxCommandsPerCall in server_config."`
	SessionID string            `json:"session_id,omitempty" jsonschema:"Optional bash session id, as for bash."`
	toolInput
}

type jobCommandInput struct {
	Command     string   `json:"command" jsonschema:"Command to run."`
	Label       string   `json:"label,omitempty" jsonschema:"Short label echoed back with the job id."`
	CWD         string   `json:"cwd,omitempty" jsonschema:"Working directory relative to workspace root. Default: ."`
	InputJobIDs []string `json:"input_job_ids,omitempty" jsonschema:"Pin retained output of these workspace jobs while this command runs."`
}

type queueJobsInput struct {
	startJobsInput
	QueueKey       string `json:"queue_key" jsonschema:"Stable caller/workspace submission key. Never change this key to retry an uncertain submission."`
	QueueTimeoutMS int    `json:"queue_timeout_ms,omitempty" jsonschema:"Maximum capacity wait, default one hour, independent of execution timeout."`
}

type jobsInput struct {
	workspaceInput
	JobIDs          []string `json:"job_ids,omitempty" jsonschema:"Jobs to collect; omit to list this workspace."`
	Output          string   `json:"output,omitempty" jsonschema:"Default tail. incremental requires exactly one job and returns a continuation cursor; none returns metadata without text."`
	Cursor          string   `json:"cursor,omitempty" jsonschema:"Opaque next_cursor for the same single job in incremental mode. Omit to start at the retained beginning."`
	MaxBytes        int      `json:"max_bytes,omitempty" jsonschema:"Combined incremental text budget; default 16384."`
	WaitFor         string   `json:"wait_for,omitempty" jsonschema:"Completion collection only. Default all; incremental mode waits for output instead."`
	WaitMS          int      `json:"wait_ms,omitempty" jsonschema:"Completion wait defaults to 30000 ms. Incremental wait defaults to 0 and returns on output/completion/drain."`
	TailBytes       int      `json:"tail_bytes,omitempty" jsonschema:"Requested tail per stream; further bounded by the aggregate response budget."`
	FullOutput      bool     `json:"full_output,omitempty" jsonschema:"Legacy alias: bounded head for finished jobs. Does not return the complete retained log."`
	IncludeFinished bool     `json:"include_finished,omitempty" jsonschema:"When listing, include finished jobs. Default true."`
	toolInput
}

type stopJobsInput struct {
	workspaceInput
	JobIDs []string `json:"job_ids" jsonschema:"Jobs to stop, from bash, start_jobs or jobs."`
	toolInput
}

func bashDefinition() definition {
	return defineTool[bashInput]("bash", "Bash", openWorldMutation,
		"Run one shell command in the workspace and wait for its result (full mode: no allowlist; chaining "+
			"with &&, pipes and redirects is allowed). Use it for tests, builds, lint, typecheck, project scripts "+
			"and git operations without a dedicated tool.  Blocked-path rules apply to file tools only, so bash "+
			"can reach secrets and build outputs; never print credentials. The text result shows exit code and a "+
			"bounded stdout/stderr tail; structured content also contains a bounded excerpt. Inspect files and "+
			"changes with tree, search, ast_grep, read, show_changes. Read and complete current-file search "+
			"contexts establish edit tags; tree and change review do not. Set timeout_ms to how long you are "+
			"willing to wait; server_config reports the current bashTimeoutMs default and maxBashTimeoutMs wait "+
			"maximum. Only if a command outruns timeout_ms does it continue as a background job (up to the "+
			"configured jobTimeoutMs maximum in server_config) and the result carries a job_id to collect with "+
			"jobs(job_ids). For work you do not want to wait for, use start_jobs instead. With systemd job "+
			"scopes, setsid, nohup, shell backgrounding (&) and output redirection do not extend the job "+
			"lifetime: descendants are killed when the job ends, including normal completion. Do not detach long "+
			"checks to bypass the job maximum.", func(s *jsonschema.Schema) {
			s.AdditionalProperties = &jsonschema.Schema{}
			s.Properties["input_job_ids"].MaxItems = new(16)
			integerRange(s.Properties["timeout_ms"], 1000, 900000)
			allowValues(s.Properties["on_timeout"], "background", "kill")
		})
}

func startJobsDefinition() definition {
	return defineTool[startJobsInput]("start_jobs", "Start Background Jobs", openWorldMutation,
		"Start one or more shell commands as background jobs and return their job_ids at once (commands that "+
			"finish within a second come back complete). Use it for long work you will keep working during: full "+
			"builds, big test suites, servers, parallel test lanes. server_config reports the current "+
			"jobTimeoutMs maximum, maxCommandsPerCall, maxJobsPerWorkspace and maxJobs; a command identical to a "+
			"running job returns that job instead of starting a second one. Collect results with jobs(job_ids, "+
			"wait_ms). With systemd job scopes, setsid, nohup, shell backgrounding (&) and output redirection do "+
			"not extend the job lifetime: descendants are killed when the job ends, including normal completion. "+
			"Do not detach long checks to bypass the job maximum.", func(s *jsonschema.Schema) {
			configureJobCommands(s.Properties["commands"])
		})
}

func configureJobCommands(s *jsonschema.Schema) {
	arrayRange(s, 1, 32)
	s.Items.AdditionalProperties = closedObject()
	s.Items.Properties["command"].MinLength = new(1)
	s.Items.Properties["label"].MaxLength = new(80)
	s.Items.Properties["input_job_ids"].MaxItems = new(16)
}

func queueJobsDefinition() definition {
	return defineTool[queueJobsInput]("queue_jobs", "Queue Background Jobs", openWorldMutation,
		"Durably queue background commands with bounded FIFO capacity waiting. Requires a stable queue_key "+
			"scoped to this named caller and workspace: retry the same key and payload to retrieve the same IDs, "+
			"including finished jobs, until queue_key_expires_at. A different payload is refused. Queued jobs "+
			"consume neither execution slots nor execution timeout; jobs and stop_jobs collect/cancel these IDs. "+
			"A distinct key authorizes a distinct submission. Older servers refuse this tool; never fall back to "+
			"start_jobs after an uncertain response.", func(s *jsonschema.Schema) {
			configureJobCommands(s.Properties["commands"])
			stringRange(s.Properties["queue_key"], 1, 160)
			integerRange(s.Properties["queue_timeout_ms"], 1000, 86400000)
		})
}

func jobsDefinition() definition {
	return defineTool[jobsInput]("jobs", "Background Jobs", readOnlyTool,
		"Collect or list jobs with output sizes, retention availability and bounded excerpts. With job_ids, "+
			"waits for all/any completion (default 30 s). output=none returns status and sizes only. "+
			"output=incremental reads one job from its retained beginning or opaque cursor; its wait_ms defaults "+
			"to 0 and can wait for new output. Keep next_cursor, use it unchanged for that job. full_output=true "+
			"is a legacy bounded-head alias, not a complete log. For large output in full Bash mode, inspect "+
			"returned output_files using Bash with input_job_ids to pin them. all_finished means completion, not "+
			"success. Incremental/metadata reads do not acknowledge completion. Finished logs can expire under "+
			"retention.", func(s *jsonschema.Schema) {
			arrayRange(s.Properties["job_ids"], 1, 32)
			s.Properties["job_ids"].Items.MinLength = new(1)
			allowValues(s.Properties["output"], "none", "tail", "head", "incremental")
			s.Properties["cursor"].MaxLength = new(2048)
			integerRange(s.Properties["max_bytes"], 256, 24576)
			allowValues(s.Properties["wait_for"], "all", "any")
			integerRange(s.Properties["wait_ms"], 0, 300000)
			integerRange(s.Properties["tail_bytes"], 256, 65536)
		})
}

func stopJobsDefinition() definition {
	return defineTool[stopJobsInput]("stop_jobs", "Stop Jobs", localDestructiveTool,
		"Stop running background jobs (SIGTERM, then SIGKILL after 1.5 s) and return each one's final status "+
			"and output tail. Jobs that already finished are reported as-is.", func(s *jsonschema.Schema) {
			arrayRange(s.Properties["job_ids"], 1, 32)
			s.Properties["job_ids"].Items.MinLength = new(1)
		})
}
