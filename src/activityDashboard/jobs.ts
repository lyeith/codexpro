import path from "node:path";
import type { CodexProConfig } from "../config.js";
import { type JobManager, getJobManager } from "../jobs.js";
import { escapeHtml } from "./format.js";

/** Authenticated HTTP callers only. Does not acknowledge, stop or start jobs. */
export function renderActivityJobFragment(config: CodexProConfig, jobId: string, workspaceId: string, manager: JobManager = getJobManager(config)): string {
  if (!/^job_[a-f0-9]{8}$/.test(jobId) || !workspaceId || workspaceId.length > 160) throw new Error("Invalid job reference");
  const job = manager.require(jobId, workspaceId);
  if (!config.projects.some((project) => path.resolve(project.root) === path.resolve(job.root))) throw new Error("Job project is no longer in the catalog");
  const output = manager.readTail(job, 16_384);
  return `<section data-job-status="${escapeHtml(job.status)}"><h4>Current job state · ${escapeHtml(new Date().toISOString())}</h4>
    <p>${escapeHtml(job.id)} · ${escapeHtml(job.status)}${job.exit_code !== null ? ` · exit ${escapeHtml(job.exit_code)}` : ""}${job.signal ? ` · ${escapeHtml(job.signal)}` : ""}${job.stop_reason ? ` · ${escapeHtml(job.stop_reason)}` : ""}</p>
    <p>Output tail: ${output.stdout_bytes} stdout bytes · ${output.stderr_bytes} stderr bytes${output.truncated ? " · truncated to the last 16,384 bytes per stream" : ""}. Reading does not acknowledge completion.</p>
    <h4>stdout</h4><pre>${escapeHtml(output.stdout || "(no stdout)")}</pre><h4>stderr</h4><pre>${escapeHtml(output.stderr || "(no stderr)")}</pre>
    </section>`;
}
