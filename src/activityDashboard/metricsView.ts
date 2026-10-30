import { escapeHtml } from "./format.js";
import type { ActivityDashboardSnapshot } from "./types.js";

export const toolCallMetricsStyle = `
  .tool-metrics { margin: 16px 0; padding: 18px 20px; border: 1px solid var(--rule); border-radius: 14px; background: var(--panel); }
  .tool-metrics-heading { display: flex; align-items: baseline; flex-wrap: wrap; gap: 8px 20px; margin-bottom: 14px; }
  .tool-metrics-heading h2 { margin: 0; font-size: 16px; }
  .tool-metrics-heading p, .tool-metrics-note { margin: 0; color: var(--muted); font-size: 12px; }
  .tool-metrics-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin-bottom: 12px; }
  .tool-metrics-grid .metric { background: var(--paper); }
  @media (max-width: 640px) { .tool-metrics { padding: 14px; } .tool-metrics-grid { grid-template-columns: 1fr; } }
`;

/** Shared by initial HTML and refresh JSON; latency n can differ from call count. */
export function renderToolCallMetrics(snapshot: ActivityDashboardSnapshot): string {
  const metrics = snapshot.metrics;
  const project = metrics?.projectId
    ? snapshot.projects.find(project => project.id === metrics.projectId)?.label ?? metrics.projectId
    : "All projects";
  const number = (value: number | undefined): string => value === undefined ? "—" : value.toLocaleString("en", { maximumFractionDigits: 1 });
  const latency = (value: number | null | undefined): string => value === null || value === undefined ? "—" : number(value) + " ms";
  return `<section class="tool-metrics" data-dashboard-section="metrics" aria-label="Tool call metrics">
    <div class="tool-metrics-heading"><h2>Tool calls</h2><p>Last 60 min · retained journal · all outcomes · ${escapeHtml(project)}</p></div>
    <div class="tool-metrics-grid">
      <div class="metric" data-refresh-key="call-rate"><span>Tool calls / hour</span><strong>${escapeHtml(number(metrics?.callsPerHour))}</strong></div>
      <div class="metric" data-refresh-key="call-mean"><span>Average tool-call latency</span><strong>${escapeHtml(latency(metrics?.meanLatencyMs))}</strong></div>
      <div class="metric" data-refresh-key="call-median"><span>Median tool-call latency</span><strong>${escapeHtml(latency(metrics?.medianLatencyMs))}</strong></div>
    </div>
    <p class="tool-metrics-note">${metrics ? escapeHtml(number(metrics.count)) + " retained completed calls · " + escapeHtml(number(metrics.failedCount)) + " unsuccessful (failed, timed out, cancelled or blocked) · latency n=" + escapeHtml(number(metrics.latencySampleCount)) + ". Includes queue time; excludes background-job completion events. Retention may reduce this sample." : "Tool-call metrics unavailable."}</p>
  </section>`;
}
