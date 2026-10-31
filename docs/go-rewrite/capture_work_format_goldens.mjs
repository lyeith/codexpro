#!/usr/bin/env node
// Pure formatter golden capture from the pinned source snippets. No coordinator,
// WorkStore, SQLite database, process launcher, or HTTP server is constructed.
import fs from "node:fs";
import path from "node:path";
import crypto from "node:crypto";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = path.dirname(fileURLToPath(import.meta.url));
const read = name => fs.readFileSync(path.join(root, name), "utf8");
const hashes = {};
function source(name) {
  const value = read(name);
  hashes[name] = crypto.createHash("sha256").update(value).digest("hex");
  return value;
}
const shared = source("tools-shared.ts");
const tools = source("tools-work-source.ts");
const briefing = source("workBriefing-source.ts");
const coordinator = source("coordinator-tail.ts");
const bounded = shared.slice(shared.indexOf("export function compactStructuredContent"), shared.indexOf("export function countTextLines"));
const result = tools.slice(tools.indexOf("  function result("), tools.indexOf('  ctx.register("work_status"'));
const methods = coordinator.slice(coordinator.indexOf("  publicIteration("), coordinator.lastIndexOf("\n}"));
if (!bounded.includes("left.localeCompare(right)") || !result.includes("packet: undefined") ||
    methods.split("    this.sweep();").length !== 2) throw new Error("unrecognized pinned pure formatter source");
const pureMethods = methods.replace("    this.sweep();\n", "");
const prelude = `
const STRUCTURED_STRING_MAX_CHARS = 30000;
const redactSensitiveText = (value: string) => value;
const redactStructured = (value: unknown) => value;
const terminal = new Set(["complete", "cancelled"]);
const ctx = { config: { work: { packetBytes: 16384 } } };
`;
const fixture = `
class FixtureReadService {
  constructor(runs: any[]) {
    this.values = runs;
    this.store = {
      get: () => undefined, runs: () => runs, children: () => [], operationPage: () => [], operationCount: () => 0,
      documentManifest: () => [{ id: "doc_fixture", revision: 1, kind: "spec" }],
    };
    this.host = {
      contextDir: ".ai-bridge", busy: () => false,
      source: () => ({ observed_at: this.now(), dirty_paths: [], complete: false, error: "Archived fixture source is not observed." }),
      activity: () => ({ enabled: false, actions: [], latest_sequence: 0 }),
      jobs: () => Array.from({ length: 4 }, (_, n) => ({ id: "j_fixture_"+n, status: "succeeded", quiescent: true,
        started_at: "2026-10-31T18:00:00.000Z", finished_at: "2026-10-31T18:00:01.000Z", deadline_ms: 1793469660000 })),
    };
  }
  now() { return "2026-10-31T18:00:01.000Z"; }
  require(_principal: string, id: string) { const run = this.values.find(value => value.id === id); if (!run) throw new Error("fixture run missing"); return run; }
  ${pureMethods}
}
function runFixture(n: number) {
  return { id: "run_fixture_abcdefgh_"+n, project_id: "fixture", title: "Safe fixture "+"title ".repeat(24), mode: "manual", state: "blocked",
    revision: 1, plan_revision: 1, spec_revision: 1, workspace: { id: "ws_fixture_"+n, branch: "codexpro/mcp/fixture_branch_long_"+n },
    generation: 0, attempt_count: 0, no_progress_count: 0, updated_at: "2026-10-31T18:00:00.000Z",
    recovery_reason: "Fixture history requires review. "+"reason ".repeat(14),
    objective: "Preserve exact formatter decisions. "+"objective ".repeat(100), scope: "Safe isolated formatting fixture.",
    todos: Array.from({ length: 6 }, (_, index) => ({ id: "todo_"+index, text: "Controlled task "+index, status: "pending" })),
    acceptance: Array.from({ length: 5 }, (_, index) => ({ id: "check_"+index, description: "Controlled acceptance "+index, state: "pending" })),
  };
}
export function goldenCases() {
  const runs = Array.from({ length: 8 }, (_, n) => runFixture(n));
  const service = new FixtureReadService(runs);
  const cases: any[] = [];
  const add = (name: string, value: any, maxBytes: number, offset = 0, limit = 10) => {
    ctx.config.work.packetBytes = maxBytes;
    cases.push({ name, value, max_bytes: maxBytes, offset, limit, expected: result(value, offset, limit) });
  };
  add("summary_undefined_is_omitted", service.summary(runs[0]), 16384);
  add("packet_keeps_undefined_key_slots", service.status("owner", runs[0].id), 16384);
  add("packet_next_keeps_undefined_key_slots", service.status("owner", runs[0].id), 16384, 2, 3);
  add("list_keeps_optional_summary_key_slots", service.status("owner"), 16384);
  const html = runFixture(9);
  html.objective = "<value>&control</value> ".repeat(300);
  const htmlService = new FixtureReadService([html]);
  add("html_packet_uses_JSON_stringify_bytes", htmlService.status("owner", html.id), 4096);
  return cases;
}
`;
const temporary = path.join(root, "work-format-source-fixture.mts");
try {
  fs.writeFileSync(temporary, prelude + bounded + briefing.replace(/^import[^\n]+\n/, "") + result + fixture, { flag: "wx", mode: 0o600 });
  const reference = await import(pathToFileURL(temporary).href);
  const cases = reference.goldenCases();
  const output = path.join(root, "workhistory-format-goldens.json");
  fs.writeFileSync(output, JSON.stringify({ source_commit: "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a", source_hashes: hashes,
    scope: "Pure captured formatter/read-projection source with synthetic non-sensitive values and identity redaction; no lifecycle methods or stores.", cases }) + "\n");
  console.log(JSON.stringify({ output, cases: cases.length, bytes: fs.statSync(output).size,
    truncation: cases.map(value => ({ name: value.name, truncated: value.expected.structuredContent.return_size.truncated,
      returned_bytes: value.expected.structuredContent.return_size.returned_bytes,
      retained_keys: Object.keys(value.expected.structuredContent) })) }));
} finally {
  if (fs.existsSync(temporary)) fs.unlinkSync(temporary);
}
