import test from "node:test";
import assert from "node:assert/strict";
import { archivedStatus, assertStatusProjection, compactSource, makeReaderDatabase, makeReadFacades } from "./historical_work_reference.mjs";

function releaseStatusFixture() {
  const source = archivedStatus.toString()
    .replace("function archivedStatus", "function status")
    .replace(") {\n", ") {\n  this.sweep();\n");
  return Function("return (" + source + ")")();
}

test("read projection attestation permits only the leading sweep omission", () => {
  assert.doesNotThrow(() => assertStatusProjection(releaseStatusFixture()));
  assert.throws(() => assertStatusProjection(archivedStatus), /expected reconciliation/);
  const changed = Function("return (" + releaseStatusFixture().toString()
    .replace("total_runs: runs.length", "total_runs: 0") + ")")();
  assert.throws(() => assertStatusProjection(changed), /read projection changed/);
  assert.notEqual(compactSource('return "a b";'), compactSource('return "ab";'));
});

test("database facade exposes only native SELECT readers and no mutation methods", () => {
  const calls = [];
  const database = { prepare(sql) {
    calls.push(sql);
    return { reader: !sql.includes("not_reader"), get: value => ({ value }), all: () => [{ value: 1 }],
             run() { throw new Error("must not be exposed"); } };
  } };
  const reader = makeReaderDatabase(database);
  assert.equal(Object.isFrozen(reader), true);
  for (const sql of ["UPDATE runs SET state='active'", "PRAGMA journal_mode=WAL", "INSERT INTO runs VALUES (1)"]) {
    assert.throws(() => reader.prepare(sql), /only SELECT/);
  }
  assert.equal(calls.length, 0);
  const statement = reader.prepare("SELECT body FROM runs WHERE id=?");
  assert.deepEqual(statement.get("owned"), { value: "owned" });
  assert.deepEqual(statement.all(), [{ value: 1 }]);
  assert.equal("run" in statement, false);
  assert.equal("exec" in reader, false);
  assert.equal("transaction" in reader, false);
  assert.throws(() => reader.prepare("SELECT not_reader"), /not a read query/);
});

test("frozen plain facades do not construct or inherit orchestration components", () => {
  class ForbiddenStore { constructor() { throw new Error("store constructor executed"); } }
  class ForbiddenCoordinator { constructor() { throw new Error("coordinator constructor executed"); } }
  ForbiddenCoordinator.prototype.status = releaseStatusFixture();
  for (const name of ["meta", "get", "runs", "children", "operationCount", "operationPage",
                     "events", "document", "documents", "documentManifest", "jobs"]) {
    ForbiddenStore.prototype[name] = function () { return this.db.prepare("SELECT body FROM runs").all(); };
  }
  for (const name of ["require", "readDocument", "search", "publicIteration", "packet",
                     "health", "publicLimits", "summary"]) {
    ForbiddenCoordinator.prototype[name] = function () { return { clock: this.now() }; };
  }
  ForbiddenCoordinator.prototype.sweep = () => { throw new Error("sweeper executed"); };
  ForbiddenStore.prototype.saveRun = () => { throw new Error("store mutation executed"); };
  const database = { prepare() { return { reader: true, all: () => [], get: () => undefined }; } };
  const { store, service } = makeReadFacades(ForbiddenStore, ForbiddenCoordinator,
    database, "2026-10-31T00:00:00.000Z", 16384, ".ai-bridge",
    () => ({ latest_sequence: 0 }));
  assert.equal(Object.getPrototypeOf(store), null);
  assert.equal(Object.getPrototypeOf(service), null);
  assert.equal(service instanceof ForbiddenCoordinator, false);
  assert.equal(store instanceof ForbiddenStore, false);
  assert.equal(Object.isFrozen(service), true);
  assert.equal(Object.isFrozen(store), true);
  assert.equal("sweep" in service, false);
  assert.equal("saveRun" in store, false);
  assert.equal(service.host.busy("historical-ready"), false);
  assert.deepEqual(service.host.source({ state: "ready" }), {
    observed_at: "2026-10-31T00:00:00.000Z", dirty_paths: [], complete: false,
    error: "Read-only archived run; current source has not been observed.",
  });
  assert.deepEqual(service.status("owner"), {
    server_time: "2026-10-31T00:00:00.000Z", runs: [], total_runs: 0, next_offset: null,
  });
});
