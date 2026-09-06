import { z } from "zod";
import { runBash } from "../bashOps.js";
import { CodexProError } from "../guard.js";
import { elapsedLabel, type JobRecord } from "../jobs.js";
import type { ToolContext } from "./context.js";
import {
  BASH_ANNOTATIONS,
  LOCAL_WRITE_ANNOTATIONS,
  READ_ONLY_ANNOTATIONS,
  bashTextResult,
  limitInt,
  parseBool,
  textResult,
  toolMeta,
  workspaceIdSchema
} from "./shared.js";

const JOB_WAIT_MAX_MS = 60_000;
const JOB_TAIL_DEFAULT_BYTES = 4_096;
const JOB_TAIL_MAX_BYTES = 64 * 1024;
const JOB_LIST_LIMIT = 20;

function jobView(job: JobRecord, tail?: { stdout: string; stderr: string; stdout_bytes: number; stderr_bytes: number; truncated: boolean }): Record<string, unknown> {
  const now = Date.now();
  return {
    job_id: job.id,
    status: job.status,
    origin: job.origin,
    command: job.command_label,
    cwd: job.cwd,
    started_at: job.started_at,
    finished_at: job.finished_at ?? null,
    elapsed_ms: (job.finished_at_ms ?? now) - job.started_at_ms,
    exit_code: job.exit_code,
    signal: job.signal,
    stop_reason: job.stop_reason ?? null,
    deadline_in_ms: job.status === "running" ? Math.max(0, job.deadline_ms - now) : null,
    ...(tail ? { stdout_tail: tail.stdout, stderr_tail: tail.stderr, stdout_bytes: tail.stdout_bytes, stderr_bytes: tail.stderr_bytes, output_truncated: tail.truncated } : {})
  };
}

function jobLine(job: JobRecord): string {
  const elapsed = elapsedLabel((job.finished_at_ms ?? Date.now()) - job.started_at_ms);
  const outcome = job.status === "running"
    ? `running ${elapsed}`
    : `${job.status}${job.exit_code !== null ? ` exit ${job.exit_code}` : ""} after ${elapsed}`;
  return `- ${job.id} · ${job.command_label} · ${outcome}`;
}

export function registerBashTools(ctx: ToolContext): void {
  const { config, workspaces, guard, jobs } = ctx;

  ctx.register(

    "bash",
    {
      title: "Bash",
      description: (config.bashMode === "full"
        ? "Run one shell command in the workspace (full mode: no allowlist; chaining with &&, pipes and redirects is allowed). Use it for tests, builds, lint, typecheck, project scripts and git operations without a dedicated tool. Prefer read/search/tree/show_changes for reading files or reviewing diffs: they are cheaper and return edit tags. Blocked-path rules apply to file tools only, so bash can reach secrets and build outputs; never print credentials. The text result shows exit code and a bounded stdout/stderr tail; full output is in structured content."
        : "Run one allowlisted verification command in the workspace, such as tests, build, lint, typecheck, or a project script (safe mode). Do not use for git status/diff or file inspection; use show_changes, tree, search, and read instead. Do not chain commands with &&, pipes, redirects, or shell file readers. The text result shows exit code and a bounded stdout/stderr tail; full output is in structured content.")
        + ` A command that outruns timeout_ms keeps running as a background job (up to ${Math.round(config.jobTimeoutMs / 60_000)} min) and the result carries its job_id; collect it later with jobs(job_id, wait_ms). Pass background=true for anything long-running (builds, test suites, servers) to return immediately.`,
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        command: z.string().describe("Command to run."),
        session_id: z.string().optional().describe(config.requireBashSession && config.bashSessionId ? `Required bash session id for this server: ${config.bashSessionId}.` : "Optional bash session id. If configured on the server, a provided value must match it."),
        cwd: z.string().optional().describe("Working directory relative to workspace root. Default: ."),
        timeout_ms: z
          .number()
          .int()
          .min(1000)
          .max(config.maxBashTimeoutMs)
          .optional()
          .describe(`How long to wait for the command in this call. Default: ${config.bashTimeoutMs}. Max: ${config.maxBashTimeoutMs}.`),
        background: z.boolean().optional().describe("Start as a background job and return at once with job_id (quick commands still return complete). Default: false."),
        on_timeout: z.enum(["background", "kill"]).optional().describe("When the command outruns timeout_ms: background keeps it running as a job (default); kill stops it.")
      },
      annotations: BASH_ANNOTATIONS,
      _meta: toolMeta("bash")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const result = await runBash(config, guard, workspace, String(args.command ?? ""), {
        cwd: args.cwd,
        timeoutMs: args.timeout_ms,
        sessionId: args.session_id,
        background: parseBool(args.background, false),
        onTimeout: args.on_timeout === "kill" ? "kill" : "background"
      });
      const text = bashTextResult(config, result);
      return textResult(text, {
        workspace_id: workspace.id,
        root: workspace.root,
        command: result.command,
        cwd: result.cwd,
        exit_code: result.exitCode,
        signal: result.signal,
        duration_ms: result.durationMs,
        stdout: result.stdout,
        stderr: result.stderr,
        truncated: result.truncated,
        timed_out: result.timedOut,
        job_id: result.jobId,
        job_status: result.jobStatus,
        job_origin: result.jobOrigin,
        bash_session_id: result.bashSessionId ?? null
      });
    }
  );

  ctx.register(
    "jobs",
    {
      title: "Background Jobs",
      description:
        "List this workspace's background jobs, or collect one: with job_id and wait_ms it waits until the job finishes or wait_ms elapses, then returns status, exit code and a bounded stdout/stderr tail. Use it after bash returned a running job_id, or when a tool result mentions background jobs. Finished jobs stay listed until collected.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        job_id: z.string().optional().describe("Collect one job. Omit to list the workspace's jobs."),
        wait_ms: z.number().int().min(0).max(JOB_WAIT_MAX_MS).optional().describe(`With job_id: wait up to this long for the job to finish. Default: 0. Max: ${JOB_WAIT_MAX_MS}.`),
        tail_bytes: z.number().int().min(256).max(JOB_TAIL_MAX_BYTES).optional().describe(`Bytes of stdout/stderr tail to return per stream. Default: ${JOB_TAIL_DEFAULT_BYTES}.`),
        include_finished: z.boolean().optional().describe("When listing, include finished jobs. Default: true.")
      },
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("jobs")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const tailBytes = limitInt(args.tail_bytes, JOB_TAIL_DEFAULT_BYTES, 256, JOB_TAIL_MAX_BYTES);
      if (typeof args.job_id === "string" && args.job_id.trim()) {
        const jobId = args.job_id.trim();
        jobs.require(jobId, workspace.id);
        const job = await jobs.wait(jobId, limitInt(args.wait_ms, 0, 0, JOB_WAIT_MAX_MS));
        const tail = jobs.readTail(job, tailBytes);
        jobs.acknowledge([job.id]);
        const view = jobView(job, tail);
        const text = [
          `# Job ${job.id}`,
          "",
          jobLine(job),
          job.status === "running" ? `Deadline in ${elapsedLabel(Math.max(0, job.deadline_ms - Date.now()))}. Call jobs(job_id, wait_ms) again to keep waiting, or stop_job to end it.` : "",
          tail.stdout ? `\n## stdout${tail.truncated ? " (tail)" : ""}\n\n\`\`\`text\n${tail.stdout}\n\`\`\`` : "",
          tail.stderr ? `\n## stderr${tail.truncated ? " (tail)" : ""}\n\n\`\`\`text\n${tail.stderr}\n\`\`\`` : ""
        ].filter((line) => line !== "").join("\n");
        return textResult(text, { workspace_id: workspace.id, root: workspace.root, job: view, waited_ms: limitInt(args.wait_ms, 0, 0, JOB_WAIT_MAX_MS) });
      }
      const includeFinished = parseBool(args.include_finished, true);
      const listed = jobs.list(workspace.id)
        .filter((job) => job.origin !== "foreground")
        .filter((job) => includeFinished || job.status === "running")
        .slice(0, JOB_LIST_LIMIT);
      const views = listed.map((job) => jobView(job, job.status === "running" ? jobs.readTail(job, 512) : undefined));
      jobs.acknowledge(listed.map((job) => job.id));
      const running = listed.filter((job) => job.status === "running").length;
      const text = listed.length
        ? `# Background jobs\n\n${listed.map(jobLine).join("\n")}\n\n${running} running · limit ${config.maxJobs} · collect with jobs(job_id, wait_ms).`
        : "# Background jobs\n\nNone for this workspace.";
      return textResult(text, { workspace_id: workspace.id, root: workspace.root, jobs: views, count: views.length, running_count: running, max_jobs: config.maxJobs });
    }
  );

  ctx.register(
    "stop_job",
    {
      title: "Stop Job",
      description: "Stop a running background job (SIGTERM, then SIGKILL after 1.5 s) and return its final status and output tail.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        job_id: z.string().describe("Job to stop, from bash or jobs.")
      },
      annotations: LOCAL_WRITE_ANNOTATIONS,
      _meta: toolMeta("stop_job")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const jobId = String(args.job_id ?? "").trim();
      const existing = jobs.require(jobId, workspace.id);
      if (existing.status !== "running") {
        throw new CodexProError(`Job ${jobId} is not running (status ${existing.status}).`, { code: "job_not_running", retryUnchanged: false });
      }
      jobs.stop(jobId, "stopped");
      const job = await jobs.wait(jobId, 3_000);
      const tail = jobs.readTail(job, JOB_TAIL_DEFAULT_BYTES);
      jobs.acknowledge([job.id]);
      return textResult(`# Stopped ${job.id}\n\n${jobLine(job)}`, { workspace_id: workspace.id, root: workspace.root, job: jobView(job, tail) });
    }
  );
}
