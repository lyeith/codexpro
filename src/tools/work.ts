import { z } from "zod";
import fs from "node:fs";
import type { ToolContext } from "./context.js";
import { currentToolContext } from "../toolContext.js";
import { workError } from "../work/coordinator.js";
import type { IterationRecord, OperationRecord } from "../work/types.js";
import { READ_ONLY_ANNOTATIONS, BASH_ANNOTATIONS, boundedBatchStructuredContent, textResult } from "./shared.js";

const id = z.string().min(1).max(160);
const short = z.string().min(1).max(2000);
const revision = z.number().int().min(1);
const optionalRun = { run_id: id.optional(), project_id: id.optional() };
const mutation = { run_id: id, request_key: id.describe("Stable unique key for this exact update; reuse after a lost return."), expected_revision: revision };
const claim = { ...mutation, attempt_token: id };
const todo = z.object({ id, title: z.string().min(1).max(500), status: z.enum(["pending", "in_progress", "blocked", "done", "skipped"]), acceptance: short.optional(), reason: short.optional(), evidence_ids: z.array(id).max(30).default([]) });
const acceptance = z.object({ id, description: short, command: z.string().min(1).max(8000).optional(), required: z.boolean().default(true) });
const notes = z.array(short).max(30);

export function registerWorkTools(ctx: ToolContext): void {
  const runtime = ctx.work; if (!runtime) return;
  const service = runtime.coordinator;
  const principal = () => currentToolContext()?.principalId ?? workError("Missing authenticated work context.");
  function result(value: any): any {
    const original = Buffer.byteLength(JSON.stringify(value));
    const max = ctx.config.work!.packetBytes;
    const essential: Record<string, unknown> = {};
    for (const key of ["run_id", "workspace_id", "context_dir", "project_id", "iteration_id", "state", "revision", "plan_revision", "spec_revision", "mode", "generation", "claimed", "inflight", "timing", "next_offset", "total_items", "total_runs", "document_id", "document_revision", "checkpoint_id"]) if (value[key] !== undefined) essential[key] = value[key];
    const remaining = { ...value }; for (const key of Object.keys(essential)) delete remaining[key];
    const compact = boundedBatchStructuredContent(remaining, Math.max(256, Math.floor((max - 1800) / 2) - Buffer.byteLength(JSON.stringify(essential))));
    const body: any = { ...compact.value as object, ...essential };
    // Credentials are deliberately present only in the claim response and its
    // same-principal idempotent replay. Never put them in history or documents.
    if (value.attempt_token) { body.attempt_token = value.attempt_token; body.session_token = value.session_token; }
    const payload = { ...body, return_size: { total_bytes: original, returned_bytes: Buffer.byteLength(JSON.stringify(body)), truncated: compact.truncated },
      ...(compact.truncated ? { handling: "Repeat the SAME offset/sequence with a smaller limit before advancing past omitted items. Use work_status sections todos, documents, iterations, operations, jobs or activity. Read exact document revisions with read_document and offset/max_bytes; search_memory searches retained memory. For command logs use jobs and retained-output Bash inputs." } : {}) };
    const rendered = textResult(JSON.stringify(payload), payload);
    // Generic text redaction correctly masks token-shaped JSON fields. Restore
    // only these server-issued opaque credentials in this authenticated response.
    if (value.attempt_token) { rendered.structuredContent.attempt_token = value.attempt_token; rendered.structuredContent.session_token = value.session_token;
      rendered.content[0].text += `\nClaim credential: ${value.attempt_token}\nWork-session credential: ${value.session_token}`; }
    return rendered;
  }
  ctx.register("work_status", {
    title: "Inspect work runs", description: "Discover optional manual/Ralph runs, claims, todos, retained documents, in-flight work and recovery evidence. No predecessor token needed for inspection. Results are bounded; paginate sections and documents. Reading does not heartbeat a claim.",
    inputSchema: { action: z.enum(["list", "get", "read_document", "search_memory", "history", "operation"]).default("list"), ...optionalRun,
      section: z.enum(["packet", "summary", "todos", "acceptance", "documents", "iterations", "operations", "jobs", "activity", "source"]).default("packet"),
      claimed: z.boolean().optional(), needs_attention: z.boolean().optional(), state: z.enum(["draft", "provisioning", "ready", "active", "closing", "waiting", "recovering", "verifying", "blocked", "paused", "complete", "cancelled"]).optional(),
      document_id: id.optional(), document_revision: revision.optional(), operation_id: id.optional(), query: z.string().min(1).max(500).optional(),
      offset: z.number().int().min(0).default(0), limit: z.number().int().min(1).max(50).default(10), max_bytes: z.number().int().min(512).max(32000).optional(), after_sequence: z.number().int().min(0).optional() }, annotations: READ_ONLY_ANNOTATIONS
  }, args => {
    const who = principal();
    if (args.action === "list") return result(service.status(who, undefined, args.project_id, args.offset, args.limit, args));
    if (args.action === "search_memory") { if (!args.query || (!args.run_id && !args.project_id)) workError("search_memory requires query and run_id or project_id."); return result(service.search(who, args)); }
    if (!args.run_id) workError("This action requires run_id.");
    const run = service.require(who, args.run_id);
    if (args.action === "read_document") { if (!args.document_id) workError("read_document requires document_id."); return result(service.readDocument(who, args)); }
    if (args.action === "history") return result({ run_id: run.id, events: service.store.events(run.id, args.after_sequence, args.limit) });
    if (args.action === "operation") {
      const op = service.store.get<OperationRecord>("operations", args.operation_id); if (!op || op.run_id !== run.id) workError("Unknown operation_id.");
      const { fingerprint, ...receipt } = op; return result({ run_id: run.id, receipt });
    }
    if (args.section === "packet") return result(service.status(who, run.id));
    if (args.section === "summary") return result(service.summary(run));
    if (args.section === "source") return result({ source: service.host.source(run), last_observed_change: run.last_change });
    if (args.section === "activity") return result(service.host.activity(run, args.after_sequence));
    if (args.section === "operations") {
      const count = service.store.operationCount(run.id); return result({ run_id: run.id, revision: run.revision, items: service.store.operationPage(run.id, args.offset, args.limit), total_items: count, next_offset: args.offset + args.limit < count ? args.offset + args.limit : null });
    }
    const values = args.section === "todos" ? run.todos : args.section === "acceptance" ? run.acceptance : args.section === "documents" ? service.store.documentManifest(run.id)
      : args.section === "iterations" ? service.store.children<IterationRecord>("iterations", run.id).map(i => service.publicIteration(i))
      : service.host.jobs(run).map(j => ({ job_id: j.id, status: j.status, quiescent: j.quiescent, started_at: j.started_at, finished_at: j.finished_at, operation_id: j.work?.operation_id }));
    return result({ run_id: run.id, revision: run.revision, items: values.slice(args.offset, args.offset + args.limit), total_items: values.length, next_offset: args.offset + args.limit < values.length ? args.offset + args.limit : null });
  });
  ctx.register("work_manage", { title: "Manage a work run", description: "Create a durable run and retained worktree, activate its plan, pause/recover/cancel, revise budgets, or request whole-run acceptance verification. Does not start an external agent. mode=ralph enables server-clock continuation guidance; manual does not. finish_run is separate from finish_iteration.",
    inputSchema: { action: z.enum(["create", "activate", "resume", "pause", "cancel", "recover", "revise_limits", "finish_run"]), request_key: id,
      run_id: id.optional(), expected_revision: revision.optional(), project_id: id.optional(), mode: z.enum(["manual", "ralph"]).optional(), title: z.string().min(1).max(300).optional(), objective: short.optional(), scope: short.optional(),
      acceptance: z.array(acceptance).max(50).optional(), todos: z.array(todo).max(200).optional(), base_ref: id.optional(), ready: z.boolean().optional(), initial_handoff: short.optional(), reason: short.optional(),
      evidence_ids: z.array(id).max(50).optional(), max_attempts: z.number().int().min(1).max(200).optional(), active_ms: z.number().int().min(1000).max(86400000).optional(), reset_no_progress: z.boolean().optional() }, annotations: BASH_ANNOTATIONS
  }, async args => {
    if (args.action === "create") { for (const field of ["project_id", "mode", "title", "objective", "scope"]) if (!args[field]) workError(`create requires ${field}.`);
      if (!ctx.config.projects.some(p => p.id === args.project_id)) workError("Unknown project_id.");
      return result(await service.create(principal(), args)); }
    if (!args.run_id || args.expected_revision === undefined) workError("This action requires run_id and expected_revision.");
    const existing = service.require(principal(), args.run_id);
    if (args.action === "recover" && existing.state === "provisioning") {
      if (!ctx.config.work!.management) workError("Run management is disabled.");
      service.revision(existing, args.expected_revision); await service.provision(existing); return result(service.status(principal(), existing.id));
    }
    if (["pause", "cancel", "recover", "revise_limits"].includes(args.action) && !args.reason) workError("This action requires a reason.");
    const value = service.manage(principal(), args); service.sweep(); return result(value);
  });
  ctx.register("work_claim", { title: "Claim a work iteration", description: "Claim one planning or execution packet using current run revision. Exactly one active claim per run. Returns attempt_token and a startup packet with source/activity/handoff evidence. Reuse session_token only for consecutive packets in this same worker session; fresh agents omit it. Claims expire on the server without agent cooperation.",
    inputSchema: { ...mutation, phase: z.enum(["plan", "execute"]).default("execute"), worker_label: z.string().min(1).max(120), objective: short, todo_ids: z.array(id).max(100).default([]), check_plan: short, session_token: id.optional() }, annotations: BASH_ANNOTATIONS
  }, args => result(service.claim(principal(), args)));
  ctx.register("work_update", { title: "Checkpoint or finish a work iteration", description: "Heartbeat; atomically checkpoint/revise todos and handoff; version memory documents; reconcile uncertain effects; or finish_iteration. Finishing closes the claim automatically after jobs stop or selected await_job_ids finish. Only finish_run (or finish_run_if_ready) requests whole-run verification. Use expected_revision for every durable update.",
    inputSchema: { action: z.enum(["heartbeat", "checkpoint", "revise_plan", "put_document", "resolve_operation", "finish_iteration"]), ...claim,
      summary: short.optional(), next_action: short.optional(), blockers: notes.optional(), decisions: notes.optional(), failed_approaches: notes.optional(), evidence_ids: z.array(id).max(50).optional(),
      todos: z.array(todo).max(200).optional(), acceptance: z.array(acceptance).max(50).optional(), objective: short.optional(), scope: short.optional(),
      outcome: z.enum(["completed", "yielded", "blocked", "failed"]).optional(), reason: short.optional(), await_job_ids: z.array(id).max(50).optional(), finish_run_if_ready: z.boolean().optional(),
      document_id: id.optional(), document_revision: revision.optional(), kind: z.enum(["note", "decision", "question", "project_memory"]).optional(), title: z.string().min(1).max(300).optional(), content: z.string().max(131072).optional(),
      reference_path: z.string().min(1).max(2000).optional().describe("Optional existing workspace document to register with its source observation. Read it using normal workspace tools; this manifest is a versioned reference."), todo_ids: z.array(id).max(100).optional(),
      operation_id: id.optional(), resolution: z.enum(["succeeded", "failed", "cancelled"]).optional() }, annotations: BASH_ANNOTATIONS
  }, args => {
    if (args.action === "heartbeat") return result(service.heartbeat(principal(), args));
    if (args.action === "put_document") {
      if (!args.title || args.content === undefined) workError("put_document requires title and content.");
      return result(service.putDocument(principal(), args, (run, input) => {
        const workspace = ctx.workspaces.getWorkspace(run.workspace?.id);
        const ref = ctx.guard.resolve(workspace, input); if (!fs.statSync(ref.absPath).isFile()) workError("Document reference must name an existing file."); return ref.relPath;
      }));
    }
    if (args.action === "resolve_operation") { if (!args.operation_id || !args.resolution || !args.reason) workError("resolve_operation requires operation_id, resolution and reason describing inspected evidence."); return result(service.resolve(principal(), args)); }
    if (args.action === "finish_iteration" && !args.outcome) workError("finish_iteration requires outcome.");
    const value = service.checkpoint(principal(), args); service.sweep(); return result(value);
  });
}
