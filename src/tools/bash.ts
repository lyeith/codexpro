import path from "node:path";
import { OUTPUT_PAGE_DEFAULT, OUTPUT_PAGE_MAX, OUTPUT_RESPONSE_MAX } from "../jobOutput.js";
import type { JobManager } from "../jobs.js";
import { inspectionGuidance } from "./guidance.js";
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
  workspaceIdSchema
} from "./shared.js";

const JOB_WAIT_MAX_MS = 300_000;
// Collecting waits by default: one long wait is cheaper than repeated status probes.
const JOB_WAIT_DEFAULT_MS = 30_000;
const JOB_TAIL_DEFAULT_BYTES = 4_096;
const JOB_TAIL_MAX_BYTES = 64 * 1024;
const JOB_LIST_LIMIT = 20;
const JOB_IDS_MAX = 32;

function jobView(job: JobRecord, output?: JobOutput, full = false, manager?: JobManager): Record<string, unknown> {
  const now = Date.now();
  return {
    ...(manager ? manager.output.metadata(job) : {}),
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
    returned_bytes: output ? Buffer.byteLength(output.stdout) + Buffer.byteLength(output.stderr) : 0,
    ...(output ? { output_mode: full ? "head" : "tail" } : {}),
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

function outputReceipt(job: JobRecord, meta: any, returned: number, bash: boolean): string {
  const available = (meta.available_stdout_bytes ?? 0) + (meta.available_stderr_bytes ?? 0);
  const captured = (meta.stdout_bytes ?? 0) + (meta.stderr_bytes ?? 0);
  const summary = `Output: ${captured} captured bytes; ${available} retained rendered bytes; ${returned} returned bytes${meta.output_growing ? " (growing)" : ""}.`;
  if (!meta.output_available) return `${summary} Output expired under retention.`;
  if (available <= returned) return summary;
  return summary + ` Read pages with jobs(job_ids=["${job.id}"], output="incremental"). Omit cursor on the first page, then pass next_cursor unchanged.` +
    (bash ? ` Or inspect output_files with Bash (input_job_ids=["${job.id}"] pins these logs): sed -n '1,80p' "$CODEXPRO_JOB_OUTPUT_DIR/${job.id}/stderr.log". Use grep/rg if installed to locate matches; bound context/matches.` : "");
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
        ? "Run one shell command in the workspace and wait for its result (full mode: no allowlist; chaining with &&, pipes and redirects is allowed). Use it for tests, builds, lint, typecheck, project scripts and git operations without a dedicated tool.  Blocked-path rules apply to file tools only, so bash can reach secrets and build outputs; never print credentials. The text result shows exit code and a bounded stdout/stderr tail; structured content also contains a bounded excerpt."
        : "Run one allowlisted verification command in the workspace and wait for its result, such as tests, build, lint, typecheck, or a project script (safe mode). Do not chain commands with &&, pipes, redirects, or shell file readers. The text result shows exit code and a bounded stdout/stderr tail; structured content also contains a bounded excerpt.")
        + " " + inspectionGuidance(config) + ` Set timeout_ms to how long you are willing to wait (default ${Math.round(config.bashTimeoutMs / 1000)} s); most commands should finish inside it. Only if a command outruns timeout_ms does it continue as a background job (up to ${Math.round(config.jobTimeoutMs / 60_000)} min) and the result carries a job_id to collect with jobs(job_ids). For work you do not want to wait for, use start_jobs instead.`,
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        command: z.string().describe("Command to run."),
        input_job_ids: z.array(z.string()).max(16).optional().describe("Jobs whose retained output this command reads. Pins their logs for this command; IDs must belong to this workspace."),
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
      annotations: BASH_ANNOTATIONS
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const result = await runBash(config, guard, workspace, String(args.command ?? ""), {
        cwd: args.cwd,
        inputJobIds: args.input_job_ids,
        timeoutMs: args.timeout_ms,
        sessionId: args.session_id,
        background: parseBool(args.background, false),
        onTimeout: args.on_timeout === "kill" ? "kill" : "background"
      });
      const job = jobs.require(result.jobId);
      const metadata = jobs.output.metadata(job);
      const text = outputReceipt(job, metadata, Buffer.byteLength(result.stdout) + Buffer.byteLength(result.stderr), config.bashMode === "full") + "\n\n" + bashTextResult(config, result);
      return textResult(text, {
        workspace_id: workspace.id,
        root: workspace.root,
        ...metadata,
        returned_bytes: Buffer.byteLength(result.stdout) + Buffer.byteLength(result.stderr),
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
          cwd: z.string().optional().describe("Working directory relative to workspace root. Default: ."),
          input_job_ids: z.array(z.string()).max(16).optional().describe("Pin retained output of these workspace jobs while this command runs.")
        }).strict()).min(1).max(config.maxJobsPerWorkspace).describe(`One to ${config.maxJobsPerWorkspace} commands, started in order.`),
        session_id: z.string().optional().describe("Optional bash session id, as for bash.")
      },
      annotations: BASH_ANNOTATIONS
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const commands: Array<{ command: string; label?: string; cwd?: string; input_job_ids?: string[] }> = args.commands;
      const capacity = jobs.capacity(workspace.id);
      const key = (command: string, cwd: string, inputs: string[] = []) => JSON.stringify([command, cwd, [...new Set(inputs)].sort()]);
      const existing = new Set(jobs.runningJobs(workspace.id).filter(j => j.origin !== "foreground").map(j => key(j.command, j.cwd, j.input_job_ids)));
      const newKeys = new Set(commands.map(item => key(item.command, path.relative(workspace.root, guard.resolve(workspace, item.cwd ?? ".").absPath) || ".", item.input_job_ids)).filter(k => !existing.has(k)));
      if (newKeys.size > capacity) {
        throw new CodexProError(
          `Cannot start ${newKeys.size} new jobs: only ${capacity} more background job${capacity === 1 ? "" : "s"} may run in this workspace right now (${config.maxJobsPerWorkspace} per workspace, ${config.maxJobs} per server). Collect or stop running jobs first, or start fewer.`,
          { code: "job_limit_reached", retryUnchanged: false, details: { capacity } }
        );
      }
      const started: Array<Record<string, unknown>> = [];
      for (const item of commands) {
        const result = await runBash(config, guard, workspace, item.command, { cwd: item.cwd, sessionId: args.session_id, inputJobIds: item.input_job_ids, background: true });
        const job = jobs.require(result.jobId);
        started.push({ ...jobView(job, job.status === "running" ? undefined : jobs.readTail(job, 1024), false, jobs), label: item.label ?? job.command_label });
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
      description: "Collect or list jobs with output sizes, retention availability and bounded excerpts. With job_ids, waits for all/any completion (default 30 s). output=none returns status and sizes only. output=incremental reads one job from its retained beginning or opaque cursor; its wait_ms defaults to 0 and can wait for new output. Keep next_cursor, use it unchanged for that job. full_output=true is a legacy bounded-head alias, not a complete log. For large output in full Bash mode, inspect returned output_files using Bash with input_job_ids to pin them. all_finished means completion, not success. Incremental/metadata reads do not acknowledge completion. Finished logs can expire under retention.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        job_ids: z.array(z.string().min(1)).min(1).max(JOB_IDS_MAX).optional().describe("Jobs to collect; omit to list this workspace."),
        output: z.enum(["none", "tail", "head", "incremental"]).optional().describe("Default tail. incremental requires exactly one job and returns a continuation cursor; none returns metadata without text."),
        cursor: z.string().max(2048).optional().describe("Opaque next_cursor for the same single job in incremental mode. Omit to start at the retained beginning."),
        max_bytes: z.number().int().min(256).max(OUTPUT_PAGE_MAX).optional().describe(`Combined incremental text budget; default ${OUTPUT_PAGE_DEFAULT}.`),
        wait_for: z.enum(["all", "any"]).optional().describe("Completion collection only. Default all; incremental mode waits for output instead."),
        wait_ms: z.number().int().min(0).max(JOB_WAIT_MAX_MS).optional().describe("Completion wait defaults to 30000 ms. Incremental wait defaults to 0 and returns on output/completion/drain."),
        tail_bytes: z.number().int().min(256).max(JOB_TAIL_MAX_BYTES).optional().describe("Requested tail per stream; further bounded by the aggregate response budget."),
        full_output: z.boolean().optional().describe("Legacy alias: bounded head for finished jobs. Does not return the complete retained log."),
        include_finished: z.boolean().optional().describe("When listing, include finished jobs. Default true.")
      },
      annotations: READ_ONLY_ANNOTATIONS
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const ids = idsArg(args.job_ids);
      const mode = args.output ?? (args.full_output ? "head" : "tail");
      const invalid = (message: string): never => { throw new CodexProError(message, { code: "args_invalid", retryUnchanged: false }); };
      if (args.output && args.full_output !== undefined) invalid("Choose output or legacy full_output, not both.");
      if (mode !== "incremental" && (args.cursor !== undefined || args.max_bytes !== undefined)) invalid("cursor/max_bytes require output=incremental.");
      if (mode === "incremental" && (ids.length !== 1 || args.wait_for !== undefined || args.tail_bytes !== undefined || args.full_output !== undefined)) invalid("Incremental output requires one job; omit completion-wait and head/tail options.");
      if (!ids.length && mode !== "tail" && mode !== "none") invalid("Specify job_ids to read output.");
      const records = ids.length ? ids.map(id => jobs.require(id, workspace.id)) : jobs.list(workspace.id)
        .filter(j => j.origin !== "foreground" && (args.include_finished !== false || j.status === "running")).slice(0, JOB_LIST_LIMIT);
      const started = Date.now();
      let interrupted = false;
      if (mode === "incremental") interrupted = await jobs.waitOutput(records[0], args.cursor, args.wait_ms ?? 0);
      else if (ids.length) interrupted = (await jobs.waitFor(ids, args.wait_for ?? "all", args.wait_ms ?? JOB_WAIT_DEFAULT_MS, { interruptible: true })).interrupted;
      const requested = args.wait_ms ?? (mode === "incremental" ? 0 : JOB_WAIT_DEFAULT_MS);
      let budget = mode === "incremental" ? args.max_bytes ?? OUTPUT_PAGE_DEFAULT : OUTPUT_PAGE_MAX;
      // Retry rendering from the same cursor if escaping/metadata exceeds the envelope budget.
      for (;;) {
        const perStream = Math.max(0, Math.floor(budget / Math.max(1, records.length * 2)));
        const views = records.map(job => {
          if (mode === "incremental") return { ...jobView(job, undefined, false, jobs), ...jobs.output.page(job, args.cursor, budget) };
          const head = mode === "head" && job.status !== "running";
          const output = mode === "none" || (!ids.length && job.status !== "running") ? undefined
            : head ? jobs.readOutput(job, Math.min(config.maxOutputBytes, perStream))
            : jobs.readTail(job, Math.min(args.tail_bytes ?? (ids.length ? JOB_TAIL_DEFAULT_BYTES : 512), perStream));
          return { ...jobView(job, output, head, jobs), ...(mode === "none" ? { output_mode: "none" } : {}) };
        });
        const text = [`# Jobs (${records.length})`, ...records.map((job, i) => {
          const v: any = views[i];
          const output: JobOutput = { stdout: v.stdout ?? v.stdout_tail ?? "", stderr: v.stderr ?? v.stderr_tail ?? "", stdout_bytes: v.stdout_bytes, stderr_bytes: v.stderr_bytes, truncated: v.output_truncated };
          return `${jobLine(job)}\n${outputReceipt(job, v, v.returned_bytes, config.bashMode === "full")}\n${outputBlocks(output, v.output_mode === "tail" ? " (tail)" : "")}`;
        }), interrupted ? "Server restarting; resume with the same job IDs/cursor." : ""].filter(Boolean).join("\n\n");
        const result = textResult(text, { workspace_id: workspace.id, root: workspace.root, jobs: views,
          job_ids: ids, count: views.length, wait_for: mode === "incremental" ? "output" : args.wait_for ?? "all",
          waited_ms: Date.now() - started, requested_wait_ms: ids.length ? requested : 0,
          all_finished: records.every(j => j.status !== "running"), all_succeeded: records.every(j => j.status === "succeeded"),
          running_count: records.filter(j => j.status === "running").length,
          returned_output_bytes: views.reduce((sum, v: any) => sum + v.returned_bytes, 0),
          max_response_bytes: OUTPUT_RESPONSE_MAX, ...(interrupted ? { server_restarting: true } : {}) });
        if (Buffer.byteLength(JSON.stringify(result)) <= OUTPUT_RESPONSE_MAX - 4096) {
          if (ids.length && mode !== "incremental" && mode !== "none") jobs.acknowledge(records.filter(j => j.status !== "running").map(j => j.id));
          return result;
        }
        if (budget <= 256) invalid("Job metadata exceeds the response budget; collect fewer job IDs.");
        budget = Math.max(256, Math.floor(budget / 2));
      }
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
      annotations: LOCAL_WRITE_ANNOTATIONS
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
      const views = records.map((job) => jobView(job, jobs.readTail(job, Math.min(JOB_TAIL_DEFAULT_BYTES, Math.floor(OUTPUT_PAGE_MAX / (records.length * 2)))), false, jobs));
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
