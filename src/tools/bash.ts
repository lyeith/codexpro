import { z } from "zod";
import { runBash } from "../bashOps.js";
import { CodexProError } from "../guard.js";
import { elapsedLabel, type JobOutput, type JobRecord } from "../jobs.js";
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

const JOB_WAIT_MAX_MS = 300_000;
// Collecting waits by default: one long wait is cheaper than repeated status probes.
const JOB_WAIT_DEFAULT_MS = 30_000;
const JOB_TAIL_DEFAULT_BYTES = 4_096;
const JOB_TAIL_MAX_BYTES = 64 * 1024;
const JOB_LIST_LIMIT = 20;
const JOB_IDS_MAX = 32;

function jobView(job: JobRecord, output?: JobOutput, full = false): Record<string, unknown> {
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
    ...(output
      ? full
        ? { stdout: output.stdout, stderr: output.stderr, stdout_bytes: output.stdout_bytes, stderr_bytes: output.stderr_bytes, output_truncated: output.truncated }
        : { stdout_tail: output.stdout, stderr_tail: output.stderr, stdout_bytes: output.stdout_bytes, stderr_bytes: output.stderr_bytes, output_truncated: output.truncated }
      : {})
  };
}

function jobLine(job: JobRecord): string {
  const elapsed = elapsedLabel((job.finished_at_ms ?? Date.now()) - job.started_at_ms);
  const outcome = job.status === "running"
    ? `running ${elapsed}`
    : `${job.status}${job.exit_code !== null ? ` exit ${job.exit_code}` : ""} after ${elapsed}`;
  return `- ${job.id} · ${job.command_label} · ${outcome}`;
}

function outputBlocks(output: JobOutput, label: string): string {
  return [
    output.stdout ? `\n## stdout${label}\n\n\`\`\`text\n${output.stdout}\n\`\`\`` : "",
    output.stderr ? `\n## stderr${label}\n\n\`\`\`text\n${output.stderr}\n\`\`\`` : ""
  ].filter(Boolean).join("\n");
}

function idsArg(value: unknown): string[] {
  const ids = Array.isArray(value) ? value.map((item) => String(item).trim()).filter(Boolean) : [];
  return [...new Set(ids)];
}

export function registerBashTools(ctx: ToolContext): void {
  const { config, workspaces, guard, jobs } = ctx;

  ctx.register(

    "bash",
    {
      title: "Bash",
      description: (config.bashMode === "full"
        ? "Run one shell command in the workspace and wait for its result (full mode: no allowlist; chaining with &&, pipes and redirects is allowed). Use it for tests, builds, lint, typecheck, project scripts and git operations without a dedicated tool. Prefer read/search/tree/show_changes for reading files or reviewing diffs: they are cheaper and return edit tags. Blocked-path rules apply to file tools only, so bash can reach secrets and build outputs; never print credentials. The text result shows exit code and a bounded stdout/stderr tail; full output is in structured content."
        : "Run one allowlisted verification command in the workspace and wait for its result, such as tests, build, lint, typecheck, or a project script (safe mode). Do not use for git status/diff or file inspection; use show_changes, tree, search, and read instead. Do not chain commands with &&, pipes, redirects, or shell file readers. The text result shows exit code and a bounded stdout/stderr tail; full output is in structured content.")
        + ` Set timeout_ms to how long you are willing to wait (default ${Math.round(config.bashTimeoutMs / 1000)} s); most commands should finish inside it. Only if a command outruns timeout_ms does it continue as a background job (up to ${Math.round(config.jobTimeoutMs / 60_000)} min) and the result carries a job_id to collect with jobs(job_ids). For work you do not want to wait for, use start_jobs instead.`,
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
          .describe(`How long to wait for the result in this call, in ms. Choose it to cover the command; do not use a small value to push work into the background (use start_jobs for that). Default: ${config.bashTimeoutMs}. Max: ${config.maxBashTimeoutMs}.`),
        on_timeout: z.enum(["background", "kill"]).optional().describe("When the command outruns timeout_ms: background keeps it running as a job (default); kill stops it.")
      },
      // Compatibility: background=true still works (same as start_jobs with one command) but is no longer advertised.
      hiddenInputSchema: {
        background: z.boolean().optional()
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
    "start_jobs",
    {
      title: "Start Background Jobs",
      description:
        `Start one or more shell commands as background jobs and return their job_ids at once (commands that finish within a second come back complete). Use it for long work you will keep working during: full builds, big test suites, servers, parallel test lanes. Jobs run up to ${Math.round(config.jobTimeoutMs / 60_000)} min, at most ${config.maxJobsPerWorkspace} per workspace; a command identical to a running job returns that job instead of starting a second one. Collect results with jobs(job_ids, wait_ms).`,
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        commands: z.array(z.object({
          command: z.string().min(1).describe("Command to run."),
          label: z.string().max(80).optional().describe("Short label echoed back with the job id."),
          cwd: z.string().optional().describe("Working directory relative to workspace root. Default: .")
        }).strict()).min(1).max(config.maxJobsPerWorkspace).describe(`One to ${config.maxJobsPerWorkspace} commands, started in order.`),
        session_id: z.string().optional().describe("Optional bash session id, as for bash.")
      },
      annotations: BASH_ANNOTATIONS,
      _meta: toolMeta("start_jobs")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const commands: Array<{ command: string; label?: string; cwd?: string }> = args.commands;
      const capacity = jobs.capacity(workspace.id);
      if (commands.length > capacity) {
        throw new CodexProError(
          `Cannot start ${commands.length} jobs: only ${capacity} more background job${capacity === 1 ? "" : "s"} may run in this workspace right now (${config.maxJobsPerWorkspace} per workspace, ${config.maxJobs} per server). Collect or stop running jobs first, or start fewer.`,
          { code: "job_limit_reached", retryUnchanged: false, details: { capacity } }
        );
      }
      const started: Array<Record<string, unknown>> = [];
      for (const item of commands) {
        const result = await runBash(config, guard, workspace, item.command, { cwd: item.cwd, sessionId: args.session_id, background: true });
        const job = jobs.require(result.jobId);
        started.push({ ...jobView(job, job.status === "running" ? undefined : jobs.readTail(job, 1024)), label: item.label ?? job.command_label });
      }
      const running = started.filter((item) => item.status === "running").length;
      const ids = started.map((item) => item.job_id as string);
      const text = [
        `# Started ${started.length} job${started.length === 1 ? "" : "s"}`,
        "",
        ...started.map((item) => `- ${item.job_id} · ${item.label} · ${item.status}${item.exit_code !== null && item.exit_code !== undefined ? ` exit ${item.exit_code}` : ""}`),
        "",
        running
          ? `Collect with one call: jobs(job_ids=${JSON.stringify(ids)}, wait_for="all", wait_ms=${JOB_WAIT_MAX_MS}) — or wait_for="any" to act on the first finisher. Do other work meanwhile.`
          : "All finished already."
      ].join("\n");
      return textResult(text, { workspace_id: workspace.id, root: workspace.root, jobs: started, job_ids: ids, running_count: running });
    }
  );

  ctx.register(
    "jobs",
    {
      title: "Background Jobs",
      description:
        `Collect or list background jobs. With job_ids it waits up to wait_ms (default ${JOB_WAIT_DEFAULT_MS / 1000} s, max ${JOB_WAIT_MAX_MS / 1000} s) until all of them (wait_for="all", default) or the first one (wait_for="any") has finished, then returns each job's status, exit code and a bounded output tail (full_output=true returns the whole output within the server limit). Waiting is cheaper than polling: call once with a long wait_ms instead of repeated short calls; wait_ms=0 is an instant status check. Without job_ids it lists this workspace's jobs. Finished jobs stay listed until collected.`,
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        job_ids: z.array(z.string().min(1)).min(1).max(JOB_IDS_MAX).optional().describe("Jobs to collect. Omit to list the workspace's jobs."),
        wait_for: z.enum(["all", "any"]).optional().describe("With job_ids: return when all have finished (default) or when any one has."),
        wait_ms: z.number().int().min(0).max(JOB_WAIT_MAX_MS).optional().describe(`With job_ids: how long to wait. Default: ${JOB_WAIT_DEFAULT_MS}. Max: ${JOB_WAIT_MAX_MS}. Prefer one long wait over repeated short ones.`),
        tail_bytes: z.number().int().min(256).max(JOB_TAIL_MAX_BYTES).optional().describe(`Bytes of stdout/stderr tail per stream. Default: ${JOB_TAIL_DEFAULT_BYTES}.`),
        full_output: z.boolean().optional().describe(`Return each finished job's complete stdout/stderr (bounded by the server output limit, ${config.maxOutputBytes} bytes per stream) instead of a tail. Default: false.`),
        include_finished: z.boolean().optional().describe("When listing, include finished jobs. Default: true.")
      },
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("jobs")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const tailBytes = limitInt(args.tail_bytes, JOB_TAIL_DEFAULT_BYTES, 256, JOB_TAIL_MAX_BYTES);
      const full = parseBool(args.full_output, false);
      const ids = idsArg(args.job_ids);
      if (ids.length) {
        for (const id of ids) jobs.require(id, workspace.id);
        const waitMs = limitInt(args.wait_ms, JOB_WAIT_DEFAULT_MS, 0, JOB_WAIT_MAX_MS);
        const mode = args.wait_for === "any" ? "any" : "all";
        const waited = await jobs.waitFor(ids, mode, waitMs, { interruptible: true });
        const views = waited.jobs.map((job) => {
          const output = job.status === "running" ? jobs.readTail(job, tailBytes) : full ? jobs.readOutput(job, config.maxOutputBytes) : jobs.readTail(job, tailBytes);
          return { job, view: jobView(job, output, full && job.status !== "running"), output };
        });
        jobs.acknowledge(waited.jobs.filter((job) => job.status !== "running").map((job) => job.id));
        const stillRunning = waited.jobs.filter((job) => job.status === "running");
        const sections = views.map(({ job, output }) => {
          const running = job.status === "running";
          const label = running ? " so far" : full ? "" : output.truncated ? " (tail)" : "";
          return `${jobLine(job)}${outputBlocks(output, label)}`;
        });
        const footer = waited.interrupted
          ? "The server is restarting; the jobs keep running. Call jobs again in a moment."
          : stillRunning.length
            ? `${stillRunning.length} still running after waiting ${elapsedLabel(waitMs)} (deadline in ${elapsedLabel(Math.max(0, Math.min(...stillRunning.map((job) => job.deadline_ms)) - Date.now()))}). Call jobs(job_ids, wait_ms=${JOB_WAIT_MAX_MS}) to keep waiting, do other work meanwhile, or stop_jobs to end them.`
            : "";
        const text = [`# Jobs (${waited.jobs.length})`, "", ...sections, "", footer].filter((line, index, all) => line !== "" || (index > 0 && all[index - 1] !== "")).join("\n");
        return textResult(text, {
          workspace_id: workspace.id,
          root: workspace.root,
          jobs: views.map((item) => item.view),
          job_ids: ids,
          wait_for: mode,
          waited_ms: waitMs,
          all_finished: stillRunning.length === 0,
          running_count: stillRunning.length,
          ...(waited.interrupted ? { server_restarting: true } : {})
        });
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
        ? `# Background jobs\n\n${listed.map(jobLine).join("\n")}\n\n${running} running · ${config.maxJobsPerWorkspace} per workspace · collect with jobs(job_ids, wait_ms).`
        : "# Background jobs\n\nNone for this workspace.";
      return textResult(text, { workspace_id: workspace.id, root: workspace.root, jobs: views, count: views.length, running_count: running, max_jobs_per_workspace: config.maxJobsPerWorkspace, max_jobs: config.maxJobs });
    }
  );

  ctx.register(
    "stop_jobs",
    {
      title: "Stop Jobs",
      description: "Stop running background jobs (SIGTERM, then SIGKILL after 1.5 s) and return each one's final status and output tail. Jobs that already finished are reported as-is.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        job_ids: z.array(z.string().min(1)).min(1).max(JOB_IDS_MAX).describe("Jobs to stop, from bash, start_jobs or jobs.")
      },
      annotations: LOCAL_WRITE_ANNOTATIONS,
      _meta: toolMeta("stop_jobs")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const ids = idsArg(args.job_ids);
      for (const id of ids) jobs.require(id, workspace.id);
      const stoppedIds = ids.filter((id) => jobs.require(id).status === "running");
      for (const id of stoppedIds) jobs.stop(id, "stopped");
      const waited = stoppedIds.length ? await jobs.waitFor(stoppedIds, "all", 3_000) : { jobs: [], interrupted: false };
      const records = ids.map((id) => jobs.require(id));
      jobs.acknowledge(records.filter((job) => job.status !== "running").map((job) => job.id));
      const views = records.map((job) => jobView(job, jobs.readTail(job, JOB_TAIL_DEFAULT_BYTES)));
      void waited;
      return textResult(`# Stopped ${stoppedIds.length} of ${ids.length}\n\n${records.map(jobLine).join("\n")}`, {
        workspace_id: workspace.id,
        root: workspace.root,
        jobs: views,
        stopped_ids: stoppedIds,
        already_finished_ids: ids.filter((id) => !stoppedIds.includes(id))
      });
    }
  );
}
