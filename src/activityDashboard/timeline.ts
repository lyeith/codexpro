import { escapeHtml, plural } from "./format.js";
import type { ActivityDashboardAction } from "./types.js";

const TIMELINE_TARGET_BINS = 140;

const TIMELINE_BIN_STEPS_MS = [5, 10, 15, 30, 60, 120, 180, 360, 720, 1_440].map((minutes) => minutes * 60_000);

const TIMELINE_TICK_STEPS_MS = [30, 60, 180, 360, 720, 1_440, 2_880, 10_080].map((minutes) => minutes * 60_000);

interface TimelineBin {
  count: number;
  mutating: number;
  failed: number;
  blocked: number;
  tools: Map<string, number>;
}

function timelineBinMs(duration: number): number {
  const target = duration / TIMELINE_TARGET_BINS;
  return TIMELINE_BIN_STEPS_MS.find((step) => step >= target) ?? TIMELINE_BIN_STEPS_MS[TIMELINE_BIN_STEPS_MS.length - 1];
}

function timelineBinLabel(binMs: number): string {
  const minutes = Math.round(binMs / 60_000);
  if (minutes < 60) return `${minutes} min`;
  if (minutes < 1_440) return `${Math.round(minutes / 60)} h`;
  return `${Math.round(minutes / 1_440)} d`;
}

function timelineTickMs(duration: number): number {
  return TIMELINE_TICK_STEPS_MS.find((step) => duration / step <= 8) ?? TIMELINE_TICK_STEPS_MS[TIMELINE_TICK_STEPS_MS.length - 1];
}

function binTone(bin: TimelineBin): string {
  if (bin.failed) return "bad";
  if (bin.blocked) return "warn";
  return "good";
}

function binIntensity(count: number): string {
  if (count >= 9) return "1";
  if (count >= 4) return ".78";
  if (count >= 2) return ".58";
  return ".4";
}

function binTitle(bin: TimelineBin, startIso: string, endIso: string): string {
  const tools = [...bin.tools.entries()]
    .sort((left, right) => right[1] - left[1])
    .slice(0, 6)
    .map(([tool, count]) => (count > 1 ? `${tool}×${count}` : tool))
    .join(", ");
  const parts = [`${plural(bin.count, "action")} · ${startIso} – ${endIso}`, tools];
  if (bin.mutating) parts.push(`${bin.mutating} mutating`);
  if (bin.failed) parts.push(`${bin.failed} failed`);
  if (bin.blocked) parts.push(`${bin.blocked} blocked/cancelled`);
  return parts.filter(Boolean).join(" · ");
}

export function renderTimeline(actions: ActivityDashboardAction[], knownProjectIds: Set<string>, generatedAt = new Date().toISOString()): string {
  const timed = actions
    .map((action) => ({ action, time: Date.parse(action.finishedAt) }))
    .filter((item) => Number.isFinite(item.time));
  if (!timed.length) {
    return `<section class="dashboard-section timeline-panel"><div class="section-heading"><div><span class="eyebrow">Cross-project activity</span><h2>Activity timeline</h2></div><span>No retained actions</span></div><p class="empty">Commands will appear here once debug activity is recorded.</p></section>`;
  }

  const now = Date.parse(generatedAt) || Date.now();
  const earliest = Math.min(...timed.map((item) => item.time));
  const rawStart = earliest;
  const binMs = timelineBinMs(Math.max(60 * 60_000, now - rawStart));
  const start = Math.floor(rawStart / binMs) * binMs;
  const end = Math.ceil((now + 1) / binMs) * binMs;
  const duration = Math.max(binMs, end - start);
  const binCount = Math.max(1, Math.round(duration / binMs));
  const binWidth = 100 / binCount;

  const lanes = new Map<string, { id?: string; label: string; latest: number; total: number; bins: Map<number, TimelineBin>; unknownIds: Set<string> }>();
  for (const { action, time } of timed) {
    if (time < start) continue;
    const known = action.projectId !== undefined && knownProjectIds.has(action.projectId);
    const key = action.projectId === undefined ? "__unattributed__" : known ? action.projectId : "__unknown__";
    const lane = lanes.get(key) ?? {
      id: known ? action.projectId : undefined,
      label: action.projectId === undefined ? action.projectLabel : known ? action.projectLabel : "Unknown project id (not in catalog)",
      latest: time,
      total: 0,
      bins: new Map(),
      unknownIds: new Set<string>()
    };
    if (!known && action.projectId !== undefined) lane.unknownIds.add(action.projectId);
    const index = Math.min(binCount - 1, Math.max(0, Math.floor((time - start) / binMs)));
    const bin = lane.bins.get(index) ?? { count: 0, mutating: 0, failed: 0, blocked: 0, tools: new Map() };
    bin.count += 1;
    if (action.mutating) bin.mutating += 1;
    if (action.status === "failed" || action.status === "timed_out") bin.failed += 1;
    if (action.status === "blocked" || action.status === "cancelled") bin.blocked += 1;
    bin.tools.set(action.toolName, (bin.tools.get(action.toolName) ?? 0) + 1);
    lane.bins.set(index, bin);
    lane.latest = Math.max(lane.latest, time);
    lane.total += 1;
    lanes.set(key, lane);
  }

  const tickMs = timelineTickMs(duration);
  const tickTimes: number[] = [];
  for (let tick = Math.ceil(start / tickMs) * tickMs; tick <= end; tick += tickMs) tickTimes.push(tick);
  const tickPositions = tickTimes.map((tick) => ((tick - start) / duration) * 100);
  const ticks = tickTimes.map((tick, index) => {
    const value = new Date(tick).toISOString();
    return `<span class="timeline-tick" style="left:${tickPositions[index].toFixed(3)}%"><time datetime="${escapeHtml(value)}" data-local-axis>${escapeHtml(value)}</time><i></i></span>`;
  }).join("");
  const grid = tickPositions.map((position) => `<i class="timeline-grid" style="left:${position.toFixed(3)}%"></i>`).join("");

  const laneRows = [...lanes.values()]
    .sort((left, right) => right.latest - left.latest || left.label.localeCompare(right.label))
    .map((lane) => {
      const cells = [...lane.bins.entries()]
        .sort((left, right) => left[0] - right[0])
        .map(([index, bin]) => {
          const binStart = new Date(start + index * binMs).toISOString();
          const binEnd = new Date(start + (index + 1) * binMs).toISOString();
          const title = binTitle(bin, binStart, binEnd);
          return `<span class="timeline-cell ${binTone(bin)}${bin.mutating ? " has-mutation" : ""}" style="left:${(index * binWidth).toFixed(3)}%;width:${binWidth.toFixed(3)}%;opacity:${binIntensity(bin.count)}" title="${escapeHtml(title)}"><span class="visually-hidden">${escapeHtml(`${lane.label}: ${title}`)}</span></span>`;
        }).join("");
      return `<div class="timeline-lane"><div class="timeline-label"><strong title="${escapeHtml(lane.label)}">${escapeHtml(lane.label)}</strong><code>${escapeHtml(lane.id ?? (lane.unknownIds.size ? [...lane.unknownIds].sort().join(", ") : "global"))} · ${escapeHtml(plural(lane.total, "action"))}</code></div><div class="timeline-track">${grid}${cells}</div></div>`;
    }).join("");

  const startIso = new Date(start).toISOString();
  const endIso = new Date(end).toISOString();
  const shown = [...lanes.values()].reduce((sum, lane) => sum + lane.total, 0);
  return `<section class="dashboard-section timeline-panel">
    <div class="section-heading"><div><span class="eyebrow">Cross-project activity</span><h2>Activity timeline</h2></div><span>${escapeHtml(`Last ${shown} actions · ${lanes.size} lanes · ${timelineBinLabel(binMs)} per cell`)}</span></div>
    <div class="timeline-scroll"><div class="timeline-chart">
      <div class="timeline-axis"><div></div><div class="timeline-axis-track">${ticks}</div></div>
      ${laneRows}
    </div></div>
    <p class="timeline-range"><span class="timeline-legend"><i class="good"></i>ok <i class="warn"></i>blocked <i class="bad"></i>failed · darker = more actions</span><time datetime="${escapeHtml(startIso)}" data-local-time>${escapeHtml(startIso)}</time><span>to</span><time datetime="${escapeHtml(endIso)}" data-local-time>${escapeHtml(endIso)}</time></p>
  </section>`;
}
