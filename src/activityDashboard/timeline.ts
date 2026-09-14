import { escapeHtml, plural } from "./format.js";
import type { ActivityDashboardAction } from "./types.js";

const TIMELINE_TARGET_BINS = 140;
const TIMELINE_BIN_STEPS_MS = [5, 10, 15, 30, 60, 120, 180, 360, 720, 1_440, 2_880, 10_080].map((minutes) => minutes * 60_000);
const TIMELINE_TICK_STEPS_MS = [30, 60, 180, 360, 720, 1_440, 2_880, 10_080].map((minutes) => minutes * 60_000);
// Cell fill by action count: 1 → faint, 2-3 → light, 4-8 → medium, 9+ → solid.
const TIMELINE_FILL_STEPS: Array<[number, number]> = [[9, 1], [4, 0.78], [2, 0.58], [1, 0.4]];

export const UNATTRIBUTED_LANE_KEY = "__unattributed__";
export const UNKNOWN_LANE_KEY = "__unknown__";
export const UNATTRIBUTED_LABEL = "Unattributed";
export const SERVER_LABEL = "Server";
export const UNKNOWN_LABEL = "Unknown project id (not in catalog)";

export interface TimelineBin {
  index: number;
  startMs: number;
  endMs: number;
  count: number;
  mutating: number;
  failed: number;
  blocked: number;
  /** Tool name → count, most frequent first. */
  tools: Array<[string, number]>;
}

export interface TimelineLane {
  key: string;
  projectId?: string;
  label: string;
  /** Raw ids folded into this lane (only for the unknown-id lane). */
  ids: string[];
  latestMs: number;
  total: number;
  bins: TimelineBin[];
}

export interface TimelineModel {
  startMs: number;
  endMs: number;
  binMs: number;
  binCount: number;
  actionCount: number;
  /** Tick timestamps strictly inside (startMs, endMs). */
  ticks: number[];
  lanes: TimelineLane[];
}

function timelineBinMs(duration: number): number {
  const target = duration / TIMELINE_TARGET_BINS;
  return TIMELINE_BIN_STEPS_MS.find((step) => step >= target) ?? TIMELINE_BIN_STEPS_MS[TIMELINE_BIN_STEPS_MS.length - 1];
}

function timelineTickMs(duration: number): number {
  return TIMELINE_TICK_STEPS_MS.find((step) => duration / step <= 8) ?? TIMELINE_TICK_STEPS_MS[TIMELINE_TICK_STEPS_MS.length - 1];
}

export function timelineBinLabel(binMs: number): string {
  const minutes = Math.round(binMs / 60_000);
  if (minutes < 60) return `${minutes} min`;
  if (minutes < 1_440) return `${Math.round(minutes / 60)} h`;
  return `${Math.round(minutes / 1_440)} d`;
}

function laneFor(action: ActivityDashboardAction): { key: string; projectId?: string; label: string } {
  if (action.attribution === "server") return { key: "__server__", label: SERVER_LABEL };
  if (action.attribution === "unattributed") return { key: UNATTRIBUTED_LANE_KEY, label: UNATTRIBUTED_LABEL };
  if (action.attribution === "unknown") return { key: UNKNOWN_LANE_KEY, label: UNKNOWN_LABEL };
  return { key: action.projectId ?? UNATTRIBUTED_LANE_KEY, projectId: action.projectId, label: action.projectLabel };
}

/**
 * Pure view-model builder: bins the given actions per project lane over
 * [floor(earliest), ceil(now)] using a bin width chosen for ~140 columns.
 * Returns undefined when there is nothing to draw.
 */
export function buildTimeline(actions: ActivityDashboardAction[], nowMs = Date.now()): TimelineModel | undefined {
  const timed = actions
    .map((action) => ({ action, time: Date.parse(action.finishedAt) }))
    .filter((item) => Number.isFinite(item.time) && item.time <= nowMs);
  if (!timed.length) return undefined;

  const earliest = Math.min(...timed.map((item) => item.time));
  const binMs = timelineBinMs(Math.max(60 * 60_000, nowMs - earliest));
  const startMs = Math.floor(earliest / binMs) * binMs;
  const endMs = Math.ceil((nowMs + 1) / binMs) * binMs;
  const binCount = Math.max(1, Math.round((endMs - startMs) / binMs));

  const lanes = new Map<string, TimelineLane & { binMap: Map<number, TimelineBin & { toolMap: Map<string, number> }>; idSet: Set<string> }>();
  for (const { action, time } of timed) {
    const target = laneFor(action);
    const lane = lanes.get(target.key) ?? {
      key: target.key,
      projectId: target.projectId,
      label: target.label,
      ids: [],
      latestMs: time,
      total: 0,
      bins: [],
      binMap: new Map(),
      idSet: new Set<string>()
    };
    if (action.attribution === "unknown" && action.projectId) lane.idSet.add(action.projectId);
    const index = Math.min(binCount - 1, Math.max(0, Math.floor((time - startMs) / binMs)));
    const bin = lane.binMap.get(index) ?? {
      index,
      startMs: startMs + index * binMs,
      endMs: startMs + (index + 1) * binMs,
      count: 0,
      mutating: 0,
      failed: 0,
      blocked: 0,
      tools: [],
      toolMap: new Map<string, number>()
    };
    bin.count += 1;
    if (action.mutating) bin.mutating += 1;
    if (action.status === "failed" || action.status === "timed_out") bin.failed += 1;
    if (action.status === "blocked" || action.status === "cancelled") bin.blocked += 1;
    bin.toolMap.set(action.toolName, (bin.toolMap.get(action.toolName) ?? 0) + 1);
    lane.binMap.set(index, bin);
    lane.latestMs = Math.max(lane.latestMs, time);
    lane.total += 1;
    lanes.set(target.key, lane);
  }

  const tickMs = timelineTickMs(endMs - startMs);
  const ticks: number[] = [];
  for (let tick = Math.ceil(startMs / tickMs) * tickMs; tick < endMs; tick += tickMs) {
    if (tick > startMs) ticks.push(tick);
  }

  const finishedLanes: TimelineLane[] = [...lanes.values()]
    .sort((left, right) => right.latestMs - left.latestMs || left.label.localeCompare(right.label))
    .map((lane) => ({
      key: lane.key,
      projectId: lane.projectId,
      label: lane.label,
      ids: [...lane.idSet].sort(),
      latestMs: lane.latestMs,
      total: lane.total,
      bins: [...lane.binMap.values()]
        .sort((left, right) => left.index - right.index)
        .map(({ toolMap, ...bin }) => ({ ...bin, tools: [...toolMap.entries()].sort((left, right) => right[1] - left[1]) }))
    }));

  return {
    startMs,
    endMs,
    binMs,
    binCount,
    actionCount: timed.length,
    ticks,
    lanes: finishedLanes
  };
}

function binTone(bin: TimelineBin): string {
  if (bin.failed) return "bad";
  if (bin.blocked) return "warn";
  return "good";
}

function binFill(count: number): number {
  return TIMELINE_FILL_STEPS.find(([threshold]) => count >= threshold)?.[1] ?? 0.4;
}

function binSummary(bin: TimelineBin): string {
  const tools = bin.tools
    .slice(0, 6)
    .map(([tool, count]) => (count > 1 ? `${tool}×${count}` : tool))
    .join(", ");
  const parts = [plural(bin.count, "action"), tools];
  if (bin.mutating) parts.push(`${bin.mutating} mutating`);
  if (bin.failed) parts.push(`${bin.failed} failed`);
  if (bin.blocked) parts.push(`${bin.blocked} blocked/cancelled`);
  return parts.filter(Boolean).join(" · ");
}

function percent(value: number, model: TimelineModel): string {
  return (((value - model.startMs) / (model.endMs - model.startMs)) * 100).toFixed(3);
}

export function renderTimeline(model: TimelineModel | undefined, headingNote: string): string {
  const heading = `<div class="section-heading"><div><span class="eyebrow">Cross-project activity</span><h2>Activity timeline</h2></div><span>${escapeHtml(headingNote)}</span></div>`;
  if (!model) {
    return `<section class="dashboard-section timeline-panel">${heading}<p class="empty">Commands will appear here once debug activity is recorded.</p></section>`;
  }

  const binWidth = 100 / model.binCount;
  const ticks = model.ticks.map((tick) => {
    const value = new Date(tick).toISOString();
    return `<span class="timeline-tick" style="left:${percent(tick, model)}%"><time datetime="${escapeHtml(value)}" data-local-axis>${escapeHtml(value)}</time><i></i></span>`;
  }).join("");
  const grid = model.ticks.map((tick) => `<i class="timeline-grid" style="left:${percent(tick, model)}%"></i>`).join("");

  const laneRows = model.lanes.map((lane) => {
    const cells = lane.bins.map((bin) => {
      const summary = binSummary(bin);
      const startIso = new Date(bin.startMs).toISOString();
      const endIso = new Date(bin.endMs).toISOString();
      return `<span class="timeline-cell ${binTone(bin)}${bin.mutating ? " has-mutation" : ""}" style="left:${(bin.index * binWidth).toFixed(3)}%;width:${binWidth.toFixed(3)}%;--fill:${binFill(bin.count)}" data-bin-start="${escapeHtml(startIso)}" data-bin-end="${escapeHtml(endIso)}" data-bin-summary="${escapeHtml(summary)}" title="${escapeHtml(`${startIso} – ${endIso} · ${summary}`)}"><span class="visually-hidden">${escapeHtml(`${lane.label}: ${startIso} – ${endIso} · ${summary}`)}</span></span>`;
    }).join("");
    const code = lane.projectId ?? (lane.ids.length ? lane.ids.join(", ") : "global");
    return `<div class="timeline-lane"><div class="timeline-label"><strong title="${escapeHtml(lane.label)}">${escapeHtml(lane.label)}</strong><code>${escapeHtml(code)} · ${escapeHtml(plural(lane.total, "action"))}</code></div><div class="timeline-track">${grid}${cells}</div></div>`;
  }).join("");

  const startIso = new Date(model.startMs).toISOString();
  const endIso = new Date(model.endMs).toISOString();
  return `<section class="dashboard-section timeline-panel">
    ${heading}
    <div class="timeline-scroll"><div class="timeline-chart">
      <div class="timeline-axis"><div></div><div class="timeline-axis-track">${ticks}</div></div>
      ${laneRows}
    </div></div>
    <p class="timeline-range"><span class="timeline-legend"><i class="good"></i>ok <i class="warn"></i>blocked <i class="bad"></i>failed · darker = more actions · underline = wrote files</span><time datetime="${escapeHtml(startIso)}" data-local-time>${escapeHtml(startIso)}</time><span>to</span><time datetime="${escapeHtml(endIso)}" data-local-time>${escapeHtml(endIso)}</time></p>
  </section>`;
}
