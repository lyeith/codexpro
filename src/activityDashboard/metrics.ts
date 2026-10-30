import { ACTION_STATUSES } from "../audit.js";
import type { ActivityDashboardAction, ActivityDashboardMetrics } from "./types.js";

export const TOOL_CALL_METRICS_WINDOW_MS = 60 * 60_000;

type MetricsAction = Pick<ActivityDashboardAction, "toolName" | "finishedAt" | "status" | "durationMs" | "projectId">;
const COMPLETED_STATUSES = new Set<string>(ACTION_STATUSES);

/**
 * Completed tool calls retained in the journal, including every outcome. The
 * journal may have pruned older calls, so this is a retained sample, not a
 * counter of every call received by the server. Job completion events describe
 * work already admitted by a tool call and must not count as another call.
 */
export function collectToolCallMetrics(
  actions: readonly MetricsAction[],
  nowMs: number,
  projectId?: string
): ActivityDashboardMetrics {
  const windowStartedAtMs = nowMs - TOOL_CALL_METRICS_WINDOW_MS;
  let count = 0;
  let failedCount = 0;
  const durations: number[] = [];
  for (const action of actions) {
    if (action.toolName === "bash_job" || !COMPLETED_STATUSES.has(action.status)) continue;
    if (projectId && action.projectId !== projectId) continue;
    const finishedAtMs = Date.parse(action.finishedAt);
    if (!Number.isFinite(finishedAtMs) || finishedAtMs < windowStartedAtMs || finishedAtMs > nowMs) continue;
    count += 1;
    if (action.status !== "succeeded") failedCount += 1;
    if (Number.isFinite(action.durationMs) && action.durationMs >= 0) durations.push(action.durationMs);
  }
  durations.sort((left, right) => left - right);
  const middle = Math.floor(durations.length / 2);
  const median = durations.length === 0
    ? null
    : durations.length % 2 === 0
      ? durations[middle - 1]! / 2 + durations[middle]! / 2
      : durations[middle]!;
  return {
    source: "retained journal",
    windowMs: TOOL_CALL_METRICS_WINDOW_MS,
    windowStartedAt: new Date(windowStartedAtMs).toISOString(),
    windowEndedAt: new Date(nowMs).toISOString(),
    ...(projectId ? { projectId } : {}),
    count,
    failedCount,
    callsPerHour: count / (TOOL_CALL_METRICS_WINDOW_MS / 3_600_000),
    latencySampleCount: durations.length,
    meanLatencyMs: durations.length === 0 ? null : durations.reduce((sum, value) => sum + value / durations.length, 0),
    medianLatencyMs: median
  };
}
