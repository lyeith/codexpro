import fs from "node:fs";
import { AuditJournal, type CodexProDashboardActionV1, type InFlightAction } from "../audit.js";
import type { CodexProConfig } from "../config.js";
import { getJobManager, type JobManager, type JobRecord } from "../jobs.js";
import { redactSensitiveText } from "../redact.js";
import type { WorkRuntime } from "../work/runtime.js";
import type { IterationRecord, RunRecord } from "../work/types.js";
import { resolveRunReference } from "../work/runReference.js";
import { jobProject } from "./jobs.js";

const MAX_INFLIGHT = 10;
const MAX_COMMAND_BYTES = 1024;
const OUTPUT_BUDGET = 32 * 1024;
const TERMINAL_RUNS = new Set(["complete", "cancelled"]);

export interface ActivityJsonOptions {
  projectId?: string;
  runId?: string;
  limit?: number;
  outputBytes?: number;
  quietAfterMs?: number;
}

/** UTF-8-safe byte bound, applied after redaction. Never return an unbounded script. */
function text(value: unknown, max = MAX_COMMAND_BYTES): string | null {
  if (typeof value !== "string") return null;
  const safe = redactSensitiveText(value);
  const bytes = Buffer.from(safe);
  if (bytes.length <= max) return safe;
  let end = max - 3;
  while (end > 0 && (bytes[end] & 0xc0) === 0x80) end--;
  return bytes.subarray(0, end).toString("utf8") + "…";
}

function age(now: number, at: string | null | undefined): number | null {
  const parsed = at ? Date.parse(at) : NaN;
  return Number.isFinite(parsed) ? Math.max(0, now - parsed) : null;
}

function newest(values: Array<string | undefined | null>): string | null {
  return values.filter((v): v is string => !!v && Number.isFinite(Date.parse(v))).sort((a, b) => Date.parse(b) - Date.parse(a))[0] ?? null;
}

function outputProgress(job: JobRecord, now: number) {
  try {
    const stats = [job.stdout_path, job.stderr_path].map(file => fs.statSync(file));
    const last = Math.max(0, ...stats.filter(stat => stat.size > 0).map(stat => stat.mtimeMs));
    return { last_output_at: last ? new Date(last).toISOString() : null,
      quiet_for_ms: Math.max(0, now - (last || job.started_at_ms)),
      captured_bytes: stats.reduce((sum, stat) => sum + stat.size, 0), output_progress_known: true };
  } catch {
    return { last_output_at: null, quiet_for_ms: null, captured_bytes: null, output_progress_known: false };
  }
}

function commandFor(action: CodexProDashboardActionV1): string | null {
  return text(action.dashboard_metadata?.shell_scripts?.map(script => script.script).join("\n") || action.request_metadata.command_label || action.operation);
}

function jobIds(action: CodexProDashboardActionV1): string[] {
  const metadata = action.result_metadata;
  const result = typeof metadata.job_id === "string" ? [metadata.job_id] : [];
  for (const key of ["jobs", "child_results"]) {
    if (Array.isArray(metadata[key])) for (const item of metadata[key]) {
      if (item && typeof item === "object" && typeof item.job_id === "string") result.push(item.job_id);
    }
  }
  return [...new Set(result)];
}

function resultSummary(action: CodexProDashboardActionV1) {
  // Operational evidence only: no file contents, edit bodies, request tokens or raw results.
  const allowed = ["exit_code", "job_status", "jobs_count", "running_count", "all_finished", "all_succeeded", "bytes", "stdout_bytes", "stderr_bytes", "changed", "files_count", "matches_count", "operation_count", "succeeded_count", "failed_count", "error_code", "revision", "state"];
  const summary: Record<string, unknown> = {};
  for (const key of allowed) {
    const value = action.result_metadata[key];
    if (typeof value === "number" || typeof value === "boolean") summary[key] = value;
    else if (typeof value === "string") summary[key] = text(value, 160);
  }
  return summary;
}

/** A bounded decision packet, including terminal runs, without a coordinator sweep. */
function monitorRun(run: RunRecord, unresolvedOperations: number) {
  const pending = run.todos.filter(todo => !["done", "skipped"].includes(todo.status));
  const checkpoint = run.checkpoint;
  return { run_id: run.id, project_id: run.project_id, mode: run.mode, title: text(run.title, 300),
    state: run.state, revision: run.revision, updated_at: run.updated_at, workspace_id: run.workspace?.id ?? null,
    claimed: !!run.iteration_id, iteration_id: run.iteration_id ?? null, unresolved_operations: unresolvedOperations,
    objective: text(run.objective), scope: text(run.scope), recovery_reason: text(run.recovery_reason, 512),
    todos: { total: run.todos.length, ...Object.fromEntries(["pending", "in_progress", "blocked", "done", "skipped"].map(status => [status, run.todos.filter(todo => todo.status === status).length])),
      unfinished: pending.slice(0, 5).map(todo => ({ id: todo.id, title: text(todo.title, 300), status: todo.status, reason: text(todo.reason, 300) })), unfinished_total: pending.length },
    checkpoint: checkpoint ? { checkpoint_id: checkpoint.id, recorded_at: checkpoint.recorded_at,
      summary: text(checkpoint.summary), next_action: text(checkpoint.next_action),
      blockers: checkpoint.blockers.slice(0, 5).map(item => text(item, 300)), blockers_total: checkpoint.blockers.length } : null,
    completion: run.completion ? { completed_at: run.completion.completed_at, spec_revision: run.completion.spec_revision,
      evidence_ids: run.completion.evidence_ids.slice(0, 10), note: "Recorded acceptance result; this GET does not recheck current source." } : null };
}

/** Read-only operator projection. Does not sweep runs, acknowledge jobs or inspect Git. */
export function collectActivityJson(config: CodexProConfig, options: ActivityJsonOptions = {}, dependencies: {
  journal?: AuditJournal; manager?: JobManager; work?: WorkRuntime; nowMs?: number;
} = {}) {
  const { projectId, runId } = options;
  if (projectId && !config.projects.some(project => project.id === projectId)) throw new Error("unknown_project");
  if (runId !== undefined && (!projectId || !runId || runId.length > 200)) throw new Error("invalid_run_id");
  const limit = options.limit ?? 8;
  const outputBytes = options.outputBytes ?? 1024;
  const quietAfterMs = options.quietAfterMs ?? 5 * 60_000;
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 10 || !Number.isSafeInteger(outputBytes) || outputBytes < 0 || outputBytes > 4096 || !Number.isSafeInteger(quietAfterMs) || quietAfterMs < 30_000 || quietAfterMs > 86_400_000) throw new Error("invalid_options");
  const now = dependencies.nowMs ?? Date.now();
  const journal = dependencies.journal ?? new AuditJournal(config);
  const manager = dependencies.manager ?? getJobManager(config);
  const allCalls = journal.listInFlight();
  const allJobs = manager.list();
  const runs = dependencies.work?.coordinator.store.runs(undefined, projectId) ?? [];
  const selectedRun = runId ? resolveRunReference(runs.filter(run => run.project_id === projectId), runId) : undefined;
  if (runId && !selectedRun) throw new Error("unknown_run");
  const auditStatus = journal.status();
  let remainingOutput = OUTPUT_BUDGET;
  const output = (job: JobRecord | undefined) => {
    if (!job) return { available: false, reason: "not_retained_or_not_a_shell_command" };
    if (job.output_expired) return { available: false, reason: "expired" };
    if (!outputBytes || !remainingOutput) return { available: true, included: false, reason: outputBytes ? "response_budget" : "not_requested" };
    try {
      const budget = Math.min(outputBytes, remainingOutput);
      // readTail bounds each stream. Split the total allowance evenly.
      const tail = manager.readTail(job, Math.floor(budget / 2));
      const returned = Buffer.byteLength(tail.stdout) + Buffer.byteLength(tail.stderr);
      remainingOutput -= returned;
      return { available: true, included: true, stdout: tail.stdout, stderr: tail.stderr,
        returned_bytes: returned, truncated: tail.truncated, complete: job.status !== "running" && !tail.truncated };
    } catch { return { available: false, reason: "unavailable" }; }
  };
  const callView = (call: InFlightAction) => ({ action_id: call.actionId, tool: call.toolName, state: call.state,
    workspace_id: call.workspaceId ?? null, started_at: call.startedAt, elapsed_ms: age(now, call.startedAt),
    command: text(call.shellScripts.map(script => script.script).join("\n") || call.requestMetadata.command_label || call.toolName) });

  const summaries = config.projects.filter(project => !projectId || project.id === projectId).map(project => {
    const calls = allCalls.filter(call => call.projectId === project.id);
    const jobs = allJobs.filter(job => jobProject(config, job)?.id === project.id);
    const activeJobs = jobs.filter(job => job.status === "running");
    const projectRuns = runs.filter(run => run.project_id === project.id && !TERMINAL_RUNS.has(run.state));
    const workRuns = projectRuns.map(run => {
      const iteration = run.iteration_id ? dependencies.work?.coordinator.store.get<IterationRecord>("iterations", run.iteration_id) : undefined;
      return { run_id: run.id, mode: run.mode, title: text(run.title, 300), revision: run.revision, state: run.state, claimed: !!run.iteration_id, workspace_id: run.workspace?.id ?? null,
        updated_at: run.updated_at, last_contact_at: iteration?.last_contact_at ?? null,
        last_contact_age_ms: age(now, iteration?.last_contact_at), last_progress_at: iteration?.last_progress_at ?? null,
        last_progress_age_ms: age(now, iteration?.last_progress_at), recovery_reason: text(run.recovery_reason, 256) };
    });
    // Extra receipts let us fold bash_job completion into its original invocation.
    const retained = journal.listForDashboard({ projectId: project.id, limit: limit * 3 }).actions.reverse();
    const lastStarted = newest([...calls.map(call => call.startedAt), ...jobs.map(job => job.started_at), ...retained.map(action => action.occurred_at)]);
    const lastFinished = newest([...jobs.map(job => job.finished_at), ...retained.map(action => action.finished_at)]);
    const progress = new Map(activeJobs.map(job => [job.id, outputProgress(job, now)]));
    const lastActivity = newest([lastStarted, lastFinished, ...workRuns.map(run => run.last_contact_at), ...activeJobs.map(job => progress.get(job.id)?.last_output_at)]);
    const signals: Array<{ kind: string; id: string; age_ms?: number | null }> = [];
    for (const call of calls) if ((age(now, call.startedAt) ?? 0) >= quietAfterMs) signals.push({ kind: "long_running_call", id: call.actionId, age_ms: age(now, call.startedAt) });
    for (const job of activeJobs) {
      const quiet = progress.get(job.id)?.quiet_for_ms;
      if (quiet !== null && quiet !== undefined && quiet >= quietAfterMs) signals.push({ kind: "job_output_quiet", id: job.id, age_ms: quiet });
      if (now > job.deadline_ms) signals.push({ kind: "job_past_deadline", id: job.id, age_ms: now - job.deadline_ms });
    }
    for (const run of workRuns) {
      if (run.claimed && (run.last_contact_age_ms ?? 0) >= quietAfterMs) signals.push({ kind: "claim_contact_quiet", id: run.run_id, age_ms: run.last_contact_age_ms });
      if (["blocked", "recovering"].includes(run.state)) signals.push({ kind: "run_" + run.state, id: run.run_id });
      if (run.state === "ready" && !run.claimed && (age(now, run.updated_at) ?? 0) >= quietAfterMs) signals.push({ kind: "run_ready_unclaimed", id: run.run_id, age_ms: age(now, run.updated_at) });
    }
    const counts = { tool_calls: calls.length, jobs: activeJobs.length, claims: workRuns.filter(run => run.claimed).length };
    const summary = { project_id: project.id, label: text(project.label, 160), url: `/activity/projects/${encodeURIComponent(project.id)}.json`,
      has_inflight_work: counts.tool_calls + counts.jobs + counts.claims > 0, inflight_counts: counts,
      last_command_started_at: lastStarted, last_command_started_age_ms: age(now, lastStarted),
      last_command_finished_at: lastFinished, last_command_finished_age_ms: age(now, lastFinished),
      last_activity_at: lastActivity, last_activity_age_ms: age(now, lastActivity),
      review_recommended: signals.length > 0, signals: signals.slice(0, MAX_INFLIGHT), signals_total: signals.length };
    if (!projectId) return summary;

    const jobById = new Map(jobs.map(job => [job.id, job]));
    const inflightJobs = activeJobs.slice(0, MAX_INFLIGHT).map(job => ({ job_id: job.id, workspace_id: job.workspace_id,
      status: job.status, origin: job.origin, command: text(job.command), started_at: job.started_at,
      elapsed_ms: age(now, job.started_at), deadline_at: new Date(job.deadline_ms).toISOString(),
      ...progress.get(job.id), output: output(job) }));
    const seenJobs = new Set<string>();
    const recent: Array<Record<string, unknown>> = [];
    for (const action of retained) {
      const ids = jobIds(action);
      if (["bash", "bash_job"].includes(action.tool_name) && ids.length) {
        if (ids.every(id => seenJobs.has(id))) continue;
        ids.forEach(id => seenJobs.add(id));
      }
      recent.push({ action_id: action.action_id, tool: action.tool_name, status: action.status,
        workspace_id: action.workspace_id ?? null, started_at: action.occurred_at, finished_at: action.finished_at,
        started_age_ms: age(now, action.occurred_at), finished_age_ms: age(now, action.finished_at), duration_ms: action.duration_ms,
        command: commandFor(action), command_truncated: !!action.dashboard_metadata?.shell_scripts?.some(script => script.truncated) || Buffer.byteLength(redactSensitiveText(action.dashboard_metadata?.shell_scripts?.map(script => script.script).join("\n") ?? "")) > MAX_COMMAND_BYTES,
        result: resultSummary(action), error: text(action.dashboard_metadata?.error_message, 256),
        job_ids: ids.slice(0, MAX_INFLIGHT), job_ids_total: ids.length });
      if (recent.length === limit) break;
    }
    // Job receipts can outlive journal retention, and remain useful when audit
    // logging is off. Merge by job id so one shell execution is shown once.
    const recordedJobIds = new Set(recent.flatMap(item => item.job_ids as string[]));
    for (const job of jobs.filter(job => job.status !== "running" && !recordedJobIds.has(job.id)).sort((a, b) => (b.finished_at_ms ?? 0) - (a.finished_at_ms ?? 0)).slice(0, limit)) {
      recent.push({ action_id: null, source: "job_store", tool: "bash_job", status: job.status,
        workspace_id: job.workspace_id, started_at: job.started_at, finished_at: job.finished_at ?? null,
        started_age_ms: age(now, job.started_at), finished_age_ms: age(now, job.finished_at),
        duration_ms: (job.finished_at_ms ?? now) - job.started_at_ms, command: text(job.command),
        command_truncated: Buffer.byteLength(redactSensitiveText(job.command)) > MAX_COMMAND_BYTES,
        result: { exit_code: job.exit_code, job_status: job.status, stop_reason: job.stop_reason ?? null },
        error: null, job_ids: [job.id], job_ids_total: 1 });
    }
    recent.sort((a, b) => Date.parse(String(b.finished_at)) - Date.parse(String(a.finished_at)));
    return { ...summary, inflight: { calls: calls.slice(0, MAX_INFLIGHT).map(callView), jobs: inflightJobs,
      truncated: calls.length > MAX_INFLIGHT || activeJobs.length > MAX_INFLIGHT },
      work_runs: workRuns.slice(0, MAX_INFLIGHT), work_runs_total: workRuns.length,
      recent_commands: recent.slice(0, limit).map(item => {
        const ids = item.job_ids as string[];
        return { ...item, output: ids.length > 1 ? { included: false, reason: "see_job_outputs" } : output(ids.length === 1 ? jobById.get(ids[0]) : undefined),
          ...(ids.length > 1 ? { job_outputs: ids.slice(0, 3).map(id => ({ job_id: id, output: output(jobById.get(id)) })), job_outputs_truncated: Number(item.job_ids_total) > 3 } : {}) };
      }) };
  });
  return { schema_version: 1, generated_at: new Date(now).toISOString(),
    ...(selectedRun ? { run: monitorRun(selectedRun, dependencies.work!.coordinator.store.operationCount(selectedRun.id, true)) } : {}),
    limits: { recent_commands: limit, output_bytes_per_command: outputBytes, inflight_per_kind: MAX_INFLIGHT, quiet_after_ms: quietAfterMs },
    coverage: { tool_calls: "current_server_process", jobs: "persistent_job_store", audit_enabled: journal.enabled,
      recent_receipts_scanned_per_project: limit * 3,
      history_incomplete: auditStatus.gap_detected || auditStatus.malformed_records > 0,
      note: "Recorded CodexPro activity only. Quiet signals invite review, not automatic cancellation. Output is a bounded retained tail; requests do not acknowledge jobs or heartbeat claims." },
    ...(projectId ? { project: summaries[0] } : { projects: summaries, unattributed_inflight_calls: allCalls.filter(call => !call.projectId).length }) };
}
