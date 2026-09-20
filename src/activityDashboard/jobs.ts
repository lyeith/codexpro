import path from "node:path";
import type { CodexProConfig } from "../config.js";
import { type JobManager, type JobRecord, getJobManager } from "../jobs.js";
import { AuditJournal } from "../audit.js";
import { redactSensitiveText } from "../redact.js";
import type { ActivityDashboardLive } from "./types.js";
import { escapeHtml } from "./format.js";

export function jobProject(config: CodexProConfig, job: JobRecord) {
  // Job project_id is resolved at launch. Worktree roots differ from catalog roots.
  return config.projects.find((project) => job.project_id
    ? project.id === job.project_id
    : path.resolve(project.root) === path.resolve(job.root));
}

export function collectActivityLive(config: CodexProConfig, journal = new AuditJournal(config), nowMs = Date.now(), manager: JobManager = getJobManager(config)): ActivityDashboardLive {
  return {
    generatedAt: new Date(nowMs).toISOString(),
    calls: journal.listInFlight().map((call) => ({ ...call,
      projectLabel: config.projects.find((project) => project.id === call.projectId)?.label ?? "Server / unattributed",
      elapsedMs: Math.max(0, nowMs - Date.parse(call.startedAt))
    })),
    jobs: manager.runningJobs().flatMap((job) => {
      const project = jobProject(config, job);
      return project ? [{ jobId: job.id, workspaceId: job.workspace_id, projectId: project.id, projectLabel: project.label,
        startedAt: job.started_at, elapsedMs: Math.max(0, nowMs - job.started_at_ms), origin: job.origin,
        command: redactSensitiveText(job.command), cwd: job.cwd, deadlineAt: new Date(job.deadline_ms).toISOString() }] : [];
    })
  };
}

/** Authenticated HTTP callers only. Does not acknowledge, stop or start jobs. */
export function renderActivityJobFragment(config: CodexProConfig, jobId: string, workspaceId: string, manager: JobManager = getJobManager(config)): string {
  if (!/^job_[a-f0-9]{8}$/.test(jobId) || !workspaceId || workspaceId.length > 160) throw new Error("Invalid job reference");
  const job = manager.require(jobId, workspaceId);
  if (!jobProject(config, job)) throw new Error("Job project is no longer in the catalog");
  const output = manager.readTail(job, 16_384);
  const metadata = manager.output.metadata(job);
  return `<section data-job-status="${escapeHtml(job.status)}"><h4>Current job state · ${escapeHtml(new Date().toISOString())}</h4>
    <p>${escapeHtml(job.id)} · ${escapeHtml(job.status)}${job.exit_code !== null ? ` · exit ${escapeHtml(job.exit_code)}` : ""}${job.signal ? ` · ${escapeHtml(job.signal)}` : ""}${job.stop_reason ? ` · ${escapeHtml(job.stop_reason)}` : ""}</p>
    <p>${metadata.output_available ? "Retained output available" : "Output expired under retention"} · ${metadata.available_stdout_bytes + metadata.available_stderr_bytes} rendered bytes.</p>
    <p>Output tail: ${output.stdout_bytes} stdout bytes · ${output.stderr_bytes} stderr bytes${output.truncated ? " · truncated to the last 16,384 bytes per stream" : ""}. Reading does not acknowledge completion.</p>
    <h4>stdout</h4><pre>${escapeHtml(output.stdout || "(no stdout)")}</pre><h4>stderr</h4><pre>${escapeHtml(output.stderr || "(no stderr)")}</pre>
    </section>`;
}
