#!/usr/bin/env node
/**
 * Pure release-0.31 work_status reference. This imports read definitions from
 * the pinned release but invokes no WorkCoordinator or WorkStore constructor.
 * Only a disposable normalized SQLite copy is opened, read-only/query-only.
 */
import fs from "node:fs";
import path from "node:path";
import os from "node:os";
import crypto from "node:crypto";
import { createRequire } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";
import assert from "node:assert/strict";

export const SOURCE_COMMIT = "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a";
const MAX_ARCHIVE_BYTES = 1024 * 1024 * 1024;
const MAX_INPUT_BYTES = 8 * 1024 * 1024;
const MAX_OUTPUT_BYTES = 64 * 1024 * 1024;
const MAX_CASES = 1024;
const STORE_READS = Object.freeze([
  "meta", "get", "runs", "children", "operationCount", "operationPage",
  "events", "document", "documents", "documentManifest", "jobs",
]);
const SERVICE_READS = Object.freeze([
  "require", "readDocument", "search", "publicIteration", "packet",
  "health", "publicLimits", "summary",
]);

function check(value, message) {
  if (!value) throw new Error(message);
}
export function within(root, candidate) {
  const relative = path.relative(root, candidate);
  return relative === "" || relative !== ".." && !relative.startsWith(".." + path.sep) && !path.isAbsolute(relative);
}
export function privateExisting(file) {
  const resolved = fs.realpathSync(file);
  const homes = new Set([os.homedir(), os.userInfo().homedir]);
  for (const home of homes) {
    for (const suffix of [".codexpro", ".config/codexpro"]) {
      const blocked = path.resolve(home, suffix);
      let canonical = blocked;
      try { canonical = fs.realpathSync(blocked); } catch (error) {
        if (error.code !== "ENOENT") throw error;
      }
      check(!within(blocked, resolved) && !within(canonical, resolved), "protected live state path refused");
    }
  }
  return resolved;
}
function boundedRead(file, maximum) {
  const stat = fs.statSync(file);
  check(stat.isFile() && stat.size <= maximum, "input must be a bounded regular file");
  return fs.readFileSync(file, "utf8");
}
export function fileDigest(file) {
  const fd = fs.openSync(file, fs.constants.O_RDONLY | (fs.constants.O_NOFOLLOW ?? 0));
  const digest = crypto.createHash("sha256");
  const block = Buffer.alloc(1024 * 1024);
  try {
    const before = fs.fstatSync(fd);
    check(before.isFile() && before.size <= MAX_ARCHIVE_BYTES, "archive must be a bounded regular file");
    while (true) {
      const count = fs.readSync(fd, block, 0, block.length, null);
      if (!count) break;
      digest.update(block.subarray(0, count));
    }
    const after = fs.fstatSync(fd);
    check(before.dev === after.dev && before.ino === after.ino &&
      before.size === after.size && before.mtimeMs === after.mtimeMs &&
      before.ctimeMs === after.ctimeMs, "file changed during read-only verification");
    return { sha256: digest.digest("hex"), bytes: before.size, device: before.dev, inode: before.ino };
  } finally { fs.closeSync(fd); }
}
function writeExclusive(file, value) {
  const fd = fs.openSync(file, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL, 0o600);
  try {
    fs.writeFileSync(fd, value);
    fs.fsyncSync(fd);
  } finally { fs.closeSync(fd); }
}
function sameClock(value) {
  check(typeof value === "string" && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/.test(value),
    "plan clock must use exact UTC milliseconds");
  check(new Date(value).toISOString() === value, "plan clock is not a valid calendar value");
  return value;
}

/**
 * This is the release status() read projection from src/work/coordinator.ts
 * 381-390 with its leading this.sweep() omitted. assertStatusProjection verifies
 * the installed release body still matches before this function can be used.
 */
export function archivedStatus(principal, id, project, offset = 0, limit = 20, filters = {}) {
  if (!id) { const runs = this.store.runs(principal, project).filter(r => (filters.claimed === undefined || !!r.iteration_id === filters.claimed) && (!filters.state || r.state === filters.state) && (filters.needs_attention === undefined || this.health(r).needs_attention === filters.needs_attention)); return { server_time: this.now(), runs: runs.slice(offset, offset + limit).map(r => this.summary(r)), total_runs: runs.length, next_offset: offset + limit < runs.length ? offset + limit : null }; }
  const run = this.require(principal, id, project); const iterations = this.store.children("iterations", run.id); const it = iterations.at(-1);
  const packet = this.packet(run);
  return { ...this.summary(run), mode: run.mode, limits: this.publicLimits(run), server_time: this.now(), current_iteration: run.iteration_id ? this.publicIteration(it) : null,
    recent_iterations: iterations.slice(-10).map(i => this.publicIteration(i)), operations: this.store.operationPage(run.id, 0, 20, true),
    jobs: this.host.jobs(run).map(j => ({ job_id: j.id, status: j.status, quiescent: j.quiescent, operation_id: j.work?.operation_id, started_at: j.started_at, finished_at: j.finished_at, deadline_ms: j.deadline_ms })),
    packet, completion: run.completion, completion_matches_current_source: run.completion ? packet.source.complete && packet.source.fingerprint === run.completion.source.fingerprint : undefined };
}

function bodyOf(fn) {
  const source = Function.prototype.toString.call(fn);
  const start = source.indexOf(") {");
  check(start >= 0 && source.lastIndexOf("}") > start, "unrecognized release read-method format");
  return source.slice(start + 3, source.lastIndexOf("}")).trim();
}
export function compactSource(source) {
  // Remove formatting whitespace only; preserve every byte inside strings.
  let out = "", quoted = "", escaped = false;
  for (const character of source) {
    if (quoted) {
      out += character;
      if (escaped) escaped = false;
      else if (character === "\\") escaped = true;
      else if (character === quoted) quoted = "";
    } else if (character === "'" || character === '"' || character === "`") {
      quoted = character; out += character;
    } else if (!/\s/.test(character)) out += character;
  }
  check(!quoted, "unclosed string in read-method attestation");
  return out;
}
export function assertStatusProjection(original) {
  const body = bodyOf(original);
  check(/^this\.sweep\(\);/.test(body), "release status no longer starts with the expected reconciliation call");
  const readBody = body.replace(/^this\.sweep\(\);\s*/, "");
  check(!readBody.includes("this.sweep("), "unexpected additional lifecycle call in status");
  check(compactSource(readBody) === compactSource(bodyOf(archivedStatus)),
    "release status read projection changed; review the new source before running an archive");
}

export function makeReaderDatabase(database) {
  // No .run(), .exec(), .transaction(), or mutable pragma exists on this facade.
  return Object.freeze({
    prepare(sql) {
      check(typeof sql === "string" && /^\s*SELECT\b/i.test(sql), "only SELECT is available to archived read methods");
      const statement = database.prepare(sql);
      check(statement.reader === true, "archive statement is not a read query");
      return Object.freeze({
        get: (...args) => statement.get(...args),
        all: (...args) => statement.all(...args),
      });
    },
  });
}
export function makeReadFacades(Store, Coordinator, database, clock, packetBytes, contextDir, activity) {
  assertStatusProjection(Coordinator.prototype.status);
  const store = Object.create(null);
  store.db = makeReaderDatabase(database);
  for (const name of STORE_READS) {
    check(typeof Store.prototype[name] === "function", "missing release store read method: " + name);
    store[name] = Store.prototype[name].bind(store);
  }
  Object.freeze(store);
  const host = Object.freeze({
    contextDir,
    source() {
      return { observed_at: clock, dirty_paths: [], complete: false,
        error: "Read-only archived run; current source has not been observed." };
    },
    activity(run, afterSequence) { return activity(run, afterSequence); },
    jobs(run) { return store.jobs(run.id); },
    busy() { return false; },
  });
  const service = Object.create(null);
  service.store = store;
  service.host = host;
  service.config = Object.freeze({ packetBytes });
  service.now = () => clock;
  for (const name of SERVICE_READS) {
    check(typeof Coordinator.prototype[name] === "function", "missing release pure read method: " + name);
    service[name] = Coordinator.prototype[name].bind(service);
  }
  service.status = archivedStatus.bind(service);
  Object.freeze(service);
  check(Object.getPrototypeOf(store) === null && Object.getPrototypeOf(service) === null,
    "archive facades must not inherit a coordinator or writable store");
  for (const name of ["sweep", "startupRecovery", "create", "manage", "checkpoint", "document",
    "atomic", "provision", "beginVerification", "verify", "resolve", "writable", "editable"]) {
    check(!(name in service), "lifecycle method escaped onto archive facade");
  }
  for (const name of ["transaction", "save", "saveRun", "event", "putDocument", "saveJob", "remember", "setMeta"]) {
    check(!(name in store), "mutation method escaped onto archive store");
  }
  return { store, service };
}

function parseArguments(argv) {
  const parsed = Object.create(null);
  for (let i = 0; i < argv.length; i += 2) {
    const name = argv[i];
    check(["--release", "--plan", "--output"].includes(name) && argv[i + 1], "use --release, --plan, and --output");
    check(!(name in parsed), "duplicate CLI option");
    parsed[name] = argv[i + 1];
  }
  check(parsed["--release"] && parsed["--plan"] && parsed["--output"], "missing required CLI option");
  return parsed;
}
export async function runReference(options) {
  const planPath = privateExisting(options.plan);
  const root = path.dirname(planPath);
  const plan = JSON.parse(boundedRead(planPath, 1024 * 1024));
  check(plan.schema_version === 1 && plan.source_commit === SOURCE_COMMIT &&
    plan.disposable_copy === true, "plan is not an approved disposable archive fixture");
  const clock = sameClock(plan.clock);
  check(Number.isInteger(plan.packet_bytes) && plan.packet_bytes >= 4096 && plan.packet_bytes <= 65536,
    "invalid archive packet budget");
  check(typeof plan.context_dir === "string" && plan.context_dir, "missing archive context directory");
  const archive = privateExisting(plan.archive.path);
  check(within(root, archive) && !fs.lstatSync(plan.archive.path).isSymbolicLink(),
    "archive must be a regular disposable file below the plan directory");
  const stat = fs.statSync(archive);
  check(stat.nlink === 1 && stat.isFile(), "archive must not share an inode through a hard link");
  for (const suffix of ["-wal", "-journal"]) {
    check(!fs.existsSync(archive + suffix) || fs.statSync(archive + suffix).size === 0,
      "archive must be normalized without a nonempty WAL/rollback journal");
  }
  const before = fileDigest(archive);
  check(before.sha256 === plan.archive.sha256 && before.bytes === plan.archive.bytes,
    "disposable archive does not match its capture manifest");
  check(!plan.source_archive || archive !== fs.realpathSync(plan.source_archive),
    "reference adapter requires a copy, not the published archive itself");
  const casesPath = privateExisting(plan.cases_path);
  check(within(root, casesPath), "cases must belong to this explicit private fixture");
  const requests = boundedRead(casesPath, MAX_INPUT_BYTES).split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line));
  check(requests.length > 0 && requests.length <= MAX_CASES, "case budget exceeded");
  const identifiers = new Set();
  for (const request of requests) {
    check(typeof request.case_id === "string" && request.case_id.length > 0 &&
      !identifiers.has(request.case_id), "case IDs must be unique");
    identifiers.add(request.case_id);
    check(request.tool === "work_status" && typeof request.principal_id === "string" &&
      request.principal_id.length > 0 && request.principal_id.length <= 200,
      "only explicitly owned work_status read cases are accepted");
  }
  const output = path.resolve(options.output);
  check(within(root, output) && fs.realpathSync(path.dirname(output)) === path.dirname(output),
    "output must be directly below the private fixture's real directory tree");
  check(!fs.existsSync(output) && !fs.existsSync(output + ".manifest.json"), "output already exists");
  const release = fs.realpathSync(options.release);
  const packageFile = path.join(release, "package.json");
  const metadata = JSON.parse(boundedRead(packageFile, 1024 * 1024));
  check(metadata.name === "codexpro" && metadata.version === "0.31.0",
    "reference must use the captured 0.31.0 release");
  const moduleNames = [
    "dist/work/store.js", "dist/work/coordinator.js", "dist/work/activity.js",
    "dist/tools/work.js", "dist/tools/workBriefing.js", "dist/tools/workSchemas.js",
    "dist/tools/shared.js", "dist/toolContext.js", "dist/redact.js",
  ];
  const modules = Object.fromEntries(moduleNames.map(name => {
    const target = fs.realpathSync(path.join(release, name));
    check(within(release, target), "release module escapes its installation");
    return [name, fileDigest(target).sha256];
  }));
  const require = createRequire(packageFile);
  const Database = require("better-sqlite3");
  const { z } = require("zod");
  const load = name => import(pathToFileURL(path.join(release, name)).href);
  const [{ WorkStore }, { WorkCoordinator }, { recentActivity }, { registerWorkTools },
    { runWithToolContext }] = await Promise.all([
      load("dist/work/store.js"), load("dist/work/coordinator.js"), load("dist/work/activity.js"),
      load("dist/tools/work.js"), load("dist/toolContext.js"),
    ]);
  const database = new Database(archive, { readonly: true, fileMustExist: true, timeout: 5000 });
  let fd, totalBytes = 0, count = 0, errors = 0;
  try {
    check(database.readonly === true, "native archive connection is writable");
    database.pragma("query_only = ON");
    check(database.pragma("query_only", { simple: true }) === 1, "archive is not query-only");
    check(database.pragma("journal_mode", { simple: true }) === "delete", "archive normalization is incomplete");
    const emptyJournal = Object.freeze({
      list() {
        return { enabled: false, actions: [], latest_sequence: 0, next_sequence: 0,
          earliest_sequence: 0, has_more: false, gap_detected: false };
      },
    });
    const { store, service } = makeReadFacades(WorkStore, WorkCoordinator, database,
      clock, plan.packet_bytes, plan.context_dir,
      (run, after) => recentActivity(emptyJournal, run.project_id, run.workspace?.id, after));
    const originalRunStates = store.runs().map(run => ({ id: run.id, state: run.state, revision: run.revision }));
    let registered;
    registerWorkTools({
      work: Object.freeze({ coordinator: service }),
      config: Object.freeze({ work: Object.freeze({ packetBytes: plan.packet_bytes }), projects: [] }),
      register(name, spec, handler) {
        if (name === "work_status") {
          check(!registered, "work_status was registered more than once");
          registered = { schema: z.object(spec.inputSchema), handler };
        }
      },
    });
    check(registered, "release did not register work_status");
    fd = fs.openSync(output, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL, 0o600);
    for (const request of requests) {
      const row = { case_id: request.case_id, principal_id: request.principal_id,
        arguments: request.arguments ?? {} };
      try {
        row.parsed_arguments = registered.schema.parse(row.arguments);
        row.result = await runWithToolContext({
          principalId: request.principal_id, requestId: request.case_id,
          signal: new AbortController().signal,
        }, () => registered.handler(row.parsed_arguments));
      } catch (error) {
        errors++;
        row.error = { code: typeof error.code === "string" ? error.code : "reference_validation",
          message: String(error.message) };
      }
      const encoded = Buffer.from(JSON.stringify(row) + "\n");
      totalBytes += encoded.length;
      check(totalBytes <= MAX_OUTPUT_BYTES, "reference evidence exceeded 64 MiB");
      fs.writeFileSync(fd, encoded);
      count++;
    }
    assert.deepEqual(store.runs().map(run => ({ id: run.id, state: run.state, revision: run.revision })),
      originalRunStates, "archived run state changed during reference projection");
    fs.fsyncSync(fd);
    fs.closeSync(fd); fd = undefined;
    database.close();
    const after = fileDigest(archive);
    check(before.sha256 === after.sha256 && before.bytes === after.bytes &&
      before.device === after.device && before.inode === after.inode,
      "read-only reference changed the disposable archive");
    for (const suffix of ["-wal", "-journal", "-shm"]) {
      check(!fs.existsSync(archive + suffix), "reference unexpectedly created an archive sidecar");
    }
    const attestation = {
      schema_version: 1, source_commit: SOURCE_COMMIT, release_version: metadata.version,
      modules, source_adapter: "src/work/coordinator.ts:381-390 read projection; leading sweep omitted",
      orchestration_constructors_invoked: [], work_store_constructor_invoked: false,
      exposed_store_methods: STORE_READS,
      exposed_service_methods: [...SERVICE_READS, "status", "now"],
      database_readonly: true, database_query_only: true, journal_mode: "delete",
      archive_unchanged: true, archive_sha256: before.sha256, archive_bytes: before.bytes,
      cases: count, expected_or_observed_errors: errors, output_bytes: totalBytes,
      work_runs: originalRunStates.length,
      stored_run_states: originalRunStates.reduce((counts, run) => {
        counts[run.state] = (counts[run.state] ?? 0) + 1; return counts;
      }, {}),
      fixed_clock: clock,
      output_sha256: fileDigest(output).sha256,
      scope: "Pure archived work_status DTOs; no transport, audit append, reconciliation, session, or execution.",
    };
    writeExclusive(output + ".manifest.json", JSON.stringify(attestation, null, 2) + "\n");
    return { cases: count, errors, work_runs: originalRunStates.length, output, attestation: output + ".manifest.json" };
  } finally {
    if (fd !== undefined) fs.closeSync(fd);
    if (database.open) database.close();
  }
}

if (process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url) {
  try {
    const options = parseArguments(process.argv.slice(2));
    const result = await runReference({ release: options["--release"], plan: options["--plan"],
      output: options["--output"] });
    process.stdout.write(JSON.stringify(result) + "\n");
  } catch (error) {
    // Do not print document contents, authenticated identities, or raw SQL data.
    process.stderr.write("historical reference failed: " + String(error.message) + "\n");
    process.exitCode = 1;
  }
}
