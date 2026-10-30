import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { AuditJournal } from "../dist/audit.js";
import { loadConfig } from "../dist/config.js";
import { collectActivityDashboard } from "../dist/activityDashboard/collect.js";
import { collectToolCallMetrics, TOOL_CALL_METRICS_WINDOW_MS } from "../dist/activityDashboard/metrics.js";

const now = Date.parse("2026-10-30T04:00:00Z");
const hour = 3_600_000;

function action(overrides = {}) {
  return {
    toolName: "read",
    finishedAt: new Date(now - 1000).toISOString(),
    status: "succeeded",
    durationMs: 100,
    projectId: "alpha",
    ...overrides
  };
}

test("tool-call metrics report last-hour retained calls, mean and odd median", () => {
  const metrics = collectToolCallMetrics([action({ durationMs: 300 }), action({ durationMs: 100 }), action({ durationMs: 200 })], now);
  assert.equal(TOOL_CALL_METRICS_WINDOW_MS, hour);
  assert.equal(metrics.source, "retained journal");
  assert.equal(metrics.windowStartedAt, "2026-10-30T03:00:00.000Z");
  assert.equal(metrics.windowEndedAt, "2026-10-30T04:00:00.000Z");
  assert.equal(metrics.windowMs, hour);
  assert.equal(metrics.count, 3);
  assert.equal(metrics.callsPerHour, 3);
  assert.equal(metrics.failedCount, 0);
  assert.equal(metrics.latencySampleCount, 3);
  assert.equal(metrics.meanLatencyMs, 200);
  assert.equal(metrics.medianLatencyMs, 200);
});

test("tool-call metrics average the middle pair for even n, preserving source order", () => {
  const actions = [action({ durationMs: 900 }), action({ durationMs: 100 }), action({ durationMs: 300 }), action({ durationMs: 200 })];
  const metrics = collectToolCallMetrics(actions, now);
  assert.equal(metrics.latencySampleCount, 4);
  assert.equal(metrics.meanLatencyMs, 375);
  assert.equal(metrics.medianLatencyMs, 250);
  assert.deepEqual(actions.map(item => item.durationMs), [900, 100, 300, 200]);
});

test("tool-call metrics return zero rate and absent latency for an empty window", () => {
  const metrics = collectToolCallMetrics([action({ finishedAt: new Date(now - hour - 1).toISOString() })], now);
  assert.equal(metrics.count, 0);
  assert.equal(metrics.callsPerHour, 0);
  assert.equal(metrics.failedCount, 0);
  assert.equal(metrics.latencySampleCount, 0);
  assert.equal(metrics.meanLatencyMs, null);
  assert.equal(metrics.medianLatencyMs, null);
  assert.doesNotThrow(() => JSON.stringify(metrics));
});

test("tool-call metrics use completion time, include both window boundaries and ignore future or invalid timestamps", () => {
  const metrics = collectToolCallMetrics([
    action({ finishedAt: new Date(now - hour).toISOString() }),
    action({ finishedAt: new Date(now).toISOString() }),
    action({ finishedAt: new Date(now - hour - 1).toISOString() }),
    action({ finishedAt: new Date(now + 1).toISOString() }),
    action({ finishedAt: "invalid" }),
    action({ startedAt: new Date(now - 2 * hour).toISOString() })
  ], now);
  assert.equal(metrics.count, 3);
  assert.equal(metrics.callsPerHour, 3);
});

test("tool-call metrics include all completed outcomes and count every unsuccessful outcome consistently", () => {
  const metrics = collectToolCallMetrics([
    action({ status: "succeeded", durationMs: 0 }),
    action({ status: "failed", durationMs: 100 }),
    action({ status: "timed_out", durationMs: 200 }),
    action({ status: "cancelled", durationMs: 300 }),
    action({ status: "blocked", durationMs: 400 })
  ], now);
  assert.equal(metrics.count, 5);
  assert.equal(metrics.failedCount, 4);
  assert.equal(metrics.latencySampleCount, 5);
  assert.equal(metrics.meanLatencyMs, 200);
  assert.equal(metrics.medianLatencyMs, 200);
});

test("tool-call metrics exclude synthetic completions and in-flight calls, and separate invalid latency samples", () => {
  const metrics = collectToolCallMetrics([
    action({ toolName: "bash_job", durationMs: 999_999 }),
    action({ status: "running", durationMs: 999_999 }),
    action({ status: "queued", durationMs: 999_999 }),
    action({ finishedAt: undefined, durationMs: 999_999 }),
    action({ durationMs: -1 }),
    action({ durationMs: Number.NaN }),
    action({ durationMs: Number.POSITIVE_INFINITY }),
    action({ durationMs: undefined }),
    action({ durationMs: 0 }),
    action({ durationMs: 100 })
  ], now);
  assert.equal(metrics.count, 6);
  assert.equal(metrics.callsPerHour, 6);
  assert.equal(metrics.latencySampleCount, 2);
  assert.equal(metrics.meanLatencyMs, 50);
  assert.equal(metrics.medianLatencyMs, 50);
  const invalidOnly = collectToolCallMetrics([action({ durationMs: -1 })], now);
  assert.equal(invalidOnly.count, 1);
  assert.equal(invalidOnly.latencySampleCount, 0);
  assert.equal(invalidOnly.meanLatencyMs, null);
  assert.equal(invalidOnly.medianLatencyMs, null);
});

test("tool-call metrics restrict the whole sample to the selected project", () => {
  const metrics = collectToolCallMetrics([
    action({ projectId: "alpha", durationMs: 100 }),
    action({ projectId: "beta", durationMs: 900, status: "failed" }),
    action({ projectId: undefined, durationMs: 200 })
  ], now, "alpha");
  assert.equal(metrics.projectId, "alpha");
  assert.equal(metrics.count, 1);
  assert.equal(metrics.failedCount, 0);
  assert.equal(metrics.meanLatencyMs, 100);
  assert.equal(metrics.medianLatencyMs, 100);
  assert.equal(collectToolCallMetrics([action()], now, "missing").count, 0);
});

test("dashboard metrics reuse journal durations and resolved project attribution regardless of history pagination", async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "codexpro-metrics-"));
  try {
    const config = loadConfig(["--root", root, "--audit", "metadata", "--audit-log", path.join(root, "audit.jsonl")]);
    const journal = new AuditJournal(config);
    const record = (args, durationMs, toolName = "read") => journal.record({
      toolName,
      args: { path: "a.txt", ...args },
      result: { structuredContent: { bytes: 1 } },
      startedAtMs: now - 1000 - durationMs,
      finishedAtMs: now - 1000,
      mutating: false
    });
    const first = record({ project_id: "default", workspace_id: "ws_metrics" }, 100);
    record({ workspace_id: "ws_metrics" }, 300);
    record({ project_id: "other", workspace_id: "ws_other" }, 900);
    record({ project_id: "default", workspace_id: "ws_metrics" }, 999_999, "bash_job");
    const inFlightId = journal.begin({ toolName: "read", args: { project_id: "default" }, startedAtMs: now - 1000, mutating: false });

    const snapshot = collectActivityDashboard(config, journal, now, { projectId: "default", beforeSequence: first.sequence });
    assert.equal(snapshot.recentActions.length, 0);
    assert.equal(snapshot.metrics.count, 2);
    assert.equal(snapshot.metrics.projectId, "default");
    assert.equal(snapshot.metrics.latencySampleCount, 2);
    assert.equal(snapshot.metrics.meanLatencyMs, 200);
    assert.equal(snapshot.metrics.medianLatencyMs, 200);
    assert.equal(collectActivityDashboard(config, journal, now).metrics.count, 3);
    journal.end(inFlightId);
  } finally {
    await fs.rm(root, { recursive: true, force: true });
  }
});
