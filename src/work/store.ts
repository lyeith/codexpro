import Database from "better-sqlite3";
import fs from "node:fs";
import path from "node:path";
import { randomUUID } from "node:crypto";
import { CodexProError } from "../guard.js";
import type { IterationRecord, OperationRecord, RunRecord, WorkDocument, WorkSession } from "./types.js";

type Entity = RunRecord | IterationRecord | OperationRecord | WorkSession;
type Table = "runs" | "iterations" | "operations" | "sessions";

/** One durable control plane, shared by all HTTP transports in a server process. */
export class WorkStore {
  readonly db: Database.Database;
  readonly id: string;
  constructor(readonly directory: string) {
    fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
    const file = path.join(directory, "work.sqlite");
    this.db = new Database(file, { timeout: 5000 });
    try {
      fs.chmodSync(file, 0o600);
      this.db.pragma("journal_mode = WAL");
      this.db.pragma("synchronous = FULL");
      this.db.pragma("foreign_keys = ON");
      this.db.exec(`
        CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS runs (id TEXT PRIMARY KEY, principal TEXT NOT NULL, project TEXT NOT NULL, state TEXT NOT NULL, revision INTEGER NOT NULL, body TEXT NOT NULL);
        CREATE INDEX IF NOT EXISTS run_scope ON runs(principal, project, state);
        CREATE TABLE IF NOT EXISTS iterations (id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), body TEXT NOT NULL);
        CREATE INDEX IF NOT EXISTS iteration_run ON iterations(run_id);
        CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), operation_key TEXT NOT NULL, body TEXT NOT NULL, UNIQUE(run_id, operation_key));
        CREATE INDEX IF NOT EXISTS operation_states ON operations(run_id, json_extract(body,'$.state'));
        CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), body TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS documents (id TEXT NOT NULL, run_id TEXT NOT NULL REFERENCES runs(id), revision INTEGER NOT NULL, kind TEXT NOT NULL, title TEXT NOT NULL, content TEXT NOT NULL, body TEXT NOT NULL, PRIMARY KEY(id, revision));
        CREATE INDEX IF NOT EXISTS document_run ON documents(run_id, id, revision);
        CREATE TABLE IF NOT EXISTS events (sequence INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL REFERENCES runs(id), kind TEXT NOT NULL, at TEXT NOT NULL, body TEXT NOT NULL);
        CREATE INDEX IF NOT EXISTS event_run ON events(run_id, sequence);
        CREATE TABLE IF NOT EXISTS requests (principal TEXT NOT NULL, request_key TEXT NOT NULL, fingerprint TEXT NOT NULL, result TEXT NOT NULL, PRIMARY KEY(principal, request_key));
        CREATE TABLE IF NOT EXISTS work_jobs (id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), body TEXT NOT NULL);
      `);
      this.id = this.transaction(() => {
        const schema = this.meta("schema");
        if (schema && schema !== "1" && schema !== "2") throw new Error(`Unsupported work database schema: ${schema}`);
        if (!schema) this.setMeta("schema", "1");
        const id = this.meta("store_id") ?? randomUUID();
        this.setMeta("store_id", id);
        return id;
      });
    } catch (error) {
      this.db.close();
      if (["SQLITE_BUSY", "SQLITE_LOCKED"].includes((error as { code?: string }).code ?? "")) {
        throw new CodexProError("Work storage is busy during coordinator startup. Retry or connect to the existing MCP endpoint.", { code: "work_store_busy", retryUnchanged: true });
      }
      throw error;
    }
  }

  transaction<T>(operation: () => T): T { return this.db.transaction(operation).immediate(); }
  meta(key: string): string | undefined { return (this.db.prepare("SELECT value FROM metadata WHERE key=?").get(key) as { value: string } | undefined)?.value; }
  setMeta(key: string, value: string): void { this.db.prepare("INSERT OR REPLACE INTO metadata VALUES (?, ?)").run(key, value); }

  get<T extends Entity>(table: Table, id: string): T | undefined {
    const row = this.db.prepare(`SELECT body FROM ${table} WHERE id=?`).get(id) as { body: string } | undefined;
    return row ? JSON.parse(row.body) as T : undefined;
  }
  runs(principal?: string, project?: string): RunRecord[] {
    const rows = this.db.prepare("SELECT body FROM runs WHERE (? IS NULL OR principal=?) AND (? IS NULL OR project=?) ORDER BY rowid DESC")
      .all(principal ?? null, principal ?? null, project ?? null, project ?? null) as { body: string }[];
    return rows.map(row => JSON.parse(row.body));
  }
  children<T extends Entity>(table: Exclude<Table, "runs">, runId: string): T[] {
    return (this.db.prepare(`SELECT body FROM ${table} WHERE run_id=? ORDER BY rowid`).all(runId) as { body: string }[]).map(row => JSON.parse(row.body));
  }
  operation(runId: string, key: string): OperationRecord | undefined {
    const row = this.db.prepare("SELECT body FROM operations WHERE run_id=? AND operation_key=?").get(runId, key) as { body: string } | undefined;
    return row ? JSON.parse(row.body) : undefined;
  }
  operationCount(runId: string, unresolved = false): number {
    return (this.db.prepare(`SELECT COUNT(*) AS n FROM operations WHERE run_id=? ${unresolved ? "AND json_extract(body,'$.state') IN ('prepared','running','unknown')" : ""}`).get(runId) as { n: number }).n;
  }
  operationPage(runId: string, offset = 0, limit = 20, reverse = false, unresolved = false): OperationRecord[] {
    return (this.db.prepare(`SELECT json_remove(body,'$.result','$.fingerprint') AS body FROM operations WHERE run_id=? ${unresolved ? "AND json_extract(body,'$.state') IN ('prepared','running','unknown')" : ""} ORDER BY rowid ${reverse ? "DESC" : "ASC"} LIMIT ? OFFSET ?`)
      .all(runId, limit, offset) as { body: string }[]).map(row => JSON.parse(row.body));
  }
  saveRun(run: RunRecord): void {
    this.db.prepare("INSERT INTO runs VALUES (@id,@principal,@project,@state,@revision,@body) ON CONFLICT(id) DO UPDATE SET state=excluded.state,revision=excluded.revision,body=excluded.body")
      .run({ id: run.id, principal: run.principal_id, project: run.project_id, state: run.state, revision: run.revision, body: JSON.stringify(run) });
  }
  save<T extends Entity>(table: Exclude<Table, "runs">, value: T & { run_id: string }): void {
    if (table === "operations") {
      this.db.prepare("INSERT INTO operations VALUES (?,?,?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body")
        .run(value.id, value.run_id, (value as OperationRecord).operation_key, JSON.stringify(value));
    } else {
      this.db.prepare(`INSERT INTO ${table} VALUES (?,?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body`).run(value.id, value.run_id, JSON.stringify(value));
    }
  }
  event(run: RunRecord, kind: string, at: string, body: unknown): number {
    return Number(this.db.prepare("INSERT INTO events(run_id,kind,at,body) VALUES (?,?,?,?)").run(run.id, kind, at, JSON.stringify(body)).lastInsertRowid);
  }
  events(runId: string, after = 0, limit = 30): unknown[] {
    return (this.db.prepare("SELECT sequence,kind,at,body FROM events WHERE run_id=? AND sequence>? ORDER BY sequence LIMIT ?")
      .all(runId, after, limit) as { sequence: number; kind: string; at: string; body: string }[]).map(({ body, ...row }) => ({ ...row, ...JSON.parse(body) }));
  }
  document(runId: string, id: string, revision?: number): WorkDocument | undefined {
    const row = this.db.prepare("SELECT body FROM documents WHERE run_id=? AND id=? AND (? IS NULL OR revision=?) ORDER BY revision DESC LIMIT 1")
      .get(runId, id, revision ?? null, revision ?? null) as { body: string } | undefined;
    return row ? JSON.parse(row.body) : undefined;
  }
  documents(runId: string): WorkDocument[] {
    return (this.db.prepare("SELECT d.body FROM documents d WHERE run_id=? AND revision=(SELECT MAX(revision) FROM documents v WHERE v.id=d.id) ORDER BY d.id")
      .all(runId) as { body: string }[]).map(row => JSON.parse(row.body));
  }
  documentManifest(runId: string): Omit<WorkDocument, "content">[] {
    return (this.db.prepare("SELECT json_remove(d.body,'$.content') AS body FROM documents d WHERE run_id=? AND revision=(SELECT MAX(revision) FROM documents v WHERE v.id=d.id) ORDER BY d.id")
      .all(runId) as { body: string }[]).map(row => JSON.parse(row.body));
  }
  putDocument(document: WorkDocument): void {
    this.db.prepare("INSERT INTO documents VALUES (?,?,?,?,?,?,?)").run(document.id, document.run_id, document.revision, document.kind, document.title, document.content, JSON.stringify(document));
  }
  saveJob<T extends { id: string; work?: { run_id: string } }>(job: T): void {
    if (!job.work || !this.get("runs", job.work.run_id)) return;
    this.db.prepare("INSERT INTO work_jobs VALUES (?,?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body").run(job.id, job.work.run_id, JSON.stringify(job));
  }
  jobs<T>(runId: string): T[] { return (this.db.prepare("SELECT body FROM work_jobs WHERE run_id=? ORDER BY rowid").all(runId) as { body: string }[]).map(row => JSON.parse(row.body)); }
  replay(principal: string, key: string, fingerprint: string): unknown | undefined {
    const row = this.db.prepare("SELECT fingerprint,result FROM requests WHERE principal=? AND request_key=?").get(principal, key) as { fingerprint: string; result: string } | undefined;
    if (!row) return undefined;
    if (row.fingerprint !== fingerprint) throw new CodexProError("Idempotency key was already used with different arguments.", { code: "work_idempotency_conflict", retryUnchanged: false });
    return JSON.parse(row.result);
  }
  remember(principal: string, key: string, fingerprint: string, result: unknown): void {
    this.db.prepare("INSERT INTO requests VALUES (?,?,?,?)").run(principal, key, fingerprint, JSON.stringify(result));
  }
  close(): void { this.db.close(); }
}
