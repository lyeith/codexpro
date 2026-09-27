import { z } from "zod";
import type { ToolContext } from "./context.js";
import { currentToolContext } from "../toolContext.js";
import { workError } from "../work/coordinator.js";
import type { IterationRecord, OperationRecord } from "../work/types.js";
import { READ_ONLY_ANNOTATIONS, BASH_ANNOTATIONS, boundedBatchStructuredContent, textResult } from "./shared.js";
import { acceptance, checkpointFields, documentFields, id, revision, runReference, short, todo } from "./workSchemas.js";
import { validateWorkDocumentReference } from "./workDocuments.js";
import { workBriefing } from "./workBriefing.js";

const optionalRun = { run_id: runReference.optional(), project_id: id.optional() };
const mutation = { run_id: runReference, request_key: id.describe("Stable unique key for this exact update; reuse after a lost return."), expected_revision: revision };

export function registerWorkTools(ctx: ToolContext): void {
  const runtime = ctx.work; if (!runtime) return;
  const service = runtime.coordinator;
  const principal = () => currentToolContext()?.principalId ?? workError("Missing authenticated work context.");
  function result(value: any, offset = 0, limit = 10): any {
    const original = Buffer.byteLength(JSON.stringify(value));
    const max = ctx.config.work!.packetBytes;
    const essential: Record<string, unknown> = {};
    // A shortened startup packet must not hide missing history from its new writer.
    const activityWarning = value.packet?.activity?.warning ?? value.warning;
    if (activityWarning) essential.activity_warning = activityWarning;
    for (const key of ["run_id", "workspace_id", "context_dir", "project_id", "iteration_id", "state", "revision", "plan_revision", "spec_revision", "mode", "generation", "claimed", "inflight", "timing", "limits", "attempt_count", "next_offset", "next_sequence", "earliest_sequence", "latest_sequence", "has_more", "gap_detected", "total_items", "total_runs", "document_id", "document_revision", "checkpoint_id", "recovery_reason", "completion_matches_current_source"]) if (value[key] !== undefined) essential[key] = value[key];
    const remaining = { ...value, packet: undefined };
    for (const key of Object.keys(essential)) delete remaining[key];
    let packetBudget = Math.max(768, Math.floor((max - 1800) / 2) - Buffer.byteLength(JSON.stringify(essential)));
    let rendered;
    do {
      if (value.packet) essential.packet = workBriefing(value.packet, packetBudget, offset, limit);
      const compact = boundedBatchStructuredContent(remaining, value.packet ? 256
        : Math.max(256, Math.floor((max - 1800) / 2) - Buffer.byteLength(JSON.stringify(essential))));
      const body: any = { ...compact.value as object, ...essential };
      const truncated = compact.truncated || !!value.packet;
      const payload = { ...body, return_size: { total_bytes: original, returned_bytes: Buffer.byteLength(JSON.stringify(body)), truncated },
        ...(truncated ? { handling: value.packet
          ? "Briefing only. Read full section pages with offset/limit; read_document with the referenced IDs/revisions and offset/max_bytes returns the complete spec/handoff."
          : "Repeat this offset/sequence with a smaller limit before advancing. Read full documents with read_document and offset/max_bytes." } : {}) };
      rendered = textResult(JSON.stringify(payload), payload);
      if (!value.packet || Buffer.byteLength(JSON.stringify(rendered)) <= max) break;
      packetBudget = Math.floor(packetBudget * 0.75);
    } while (packetBudget >= 128);
    return rendered;
  }
  ctx.register("work_status", {
    title: "Inspect work runs", description: "Read run state, tasks, documents and operation receipts. Paginate sections and documents for full detail.",
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
    const run = service.require(who, args.run_id, args.project_id);
    if (args.action === "read_document") { if (!args.document_id) workError("read_document requires document_id."); return result(service.readDocument(who, args)); }
    if (args.action === "history") return result({ run_id: run.id, events: service.store.events(run.id, args.after_sequence, args.limit) });
    if (args.action === "operation") {
      const op = service.store.get<OperationRecord>("operations", args.operation_id); if (!op || op.run_id !== run.id) workError("Unknown operation_id.");
      const { fingerprint, ...receipt } = op; return result({ run_id: run.id, receipt });
    }
    if (args.section === "packet") return result(service.status(who, run.id), args.offset, args.limit);
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
  ctx.register("work_manage", { title: "Manage a work run", description: "Create a run and retained worktree, activate, pause, recover or cancel it, or request final verification. Does not start an agent. finish_run verifies the whole run; finish_iteration saves a batch handoff.",
    inputSchema: { action: z.enum(["create", "activate", "resume", "pause", "cancel", "recover", "revise_limits", "finish_run"]), request_key: id,
      run_id: runReference.optional(), expected_revision: revision.optional(), project_id: id.optional(), mode: z.enum(["manual", "ralph"]).optional(), title: z.string().min(1).max(300).optional(), objective: short.optional(), scope: short.optional(),
      acceptance: z.array(acceptance).max(50).optional(), todos: z.array(todo).max(200).optional(), base_ref: id.optional(), ready: z.boolean().optional(), initial_handoff: short.optional(), reason: short.optional(),
      evidence_ids: z.array(id).max(50).optional(), max_attempts: z.number().int().nonnegative().optional().describe("Deprecated compatibility input: iteration count is unlimited. Ignored and explicitly reported in the response."), active_ms: z.number().int().nonnegative().optional().describe("Deprecated compatibility input: cumulative run time is unlimited. Ignored and explicitly reported in the response."), reset_no_progress: z.boolean().optional().describe("Acknowledge and reset the advisory no-progress counter; it never blocks work.") }, annotations: BASH_ANNOTATIONS
  }, async args => {
    if (args.action === "create") { for (const field of ["project_id", "mode", "title", "objective", "scope"]) if (!args[field]) workError(`create requires ${field}.`);
      if (!ctx.config.projects.some(p => p.id === args.project_id)) workError("Unknown project_id.");
      return result(await service.create(principal(), args)); }
    if (!args.run_id || args.expected_revision === undefined) workError("This action requires run_id and expected_revision.");
    const existing = service.require(principal(), args.run_id, args.project_id);
    if (args.action === "recover" && existing.state === "provisioning") {
      if (!ctx.config.work!.management) workError("Run management is disabled.");
      service.revision(existing, args.expected_revision); await service.provision(existing); return result(service.status(principal(), existing.id));
    }
    if (["pause", "cancel", "recover", "revise_limits"].includes(args.action) && !args.reason) workError("This action requires a reason.");
    const value = service.manage(principal(), args); service.sweep(); return result(value);
  });
  ctx.register("work_update", { title: "Checkpoint or finish a work iteration", description: "Save tasks, handoff and documents atomically; revise a plan or reconcile an uncertain operation. Use expected_revision and a stable request_key. finish_iteration records the batch outcome after jobs stop or await_job_ids finish; finish_run_if_ready requests final verification.",
    inputSchema: { action: z.enum(["checkpoint", "revise_plan", "put_document", "resolve_operation", "finish_iteration"]), ...mutation,
      ...checkpointFields, summary: short.optional(), next_action: short.optional(),
      acceptance: z.array(acceptance).max(50).optional(), acceptance_updates: z.array(acceptance).max(50).optional().describe("Upsert this page of acceptance criteria by id, preserving all others. Use instead of acceptance for larger specifications."), objective: short.optional(), scope: short.optional(),
      outcome: z.enum(["completed", "yielded", "blocked", "failed"]).optional(), reason: short.optional(), await_job_ids: z.array(id).max(50).optional(), finish_run_if_ready: z.boolean().optional(),
      ...documentFields, title: documentFields.title.optional(), content: documentFields.content.optional(),
      operation_id: id.optional(), resolution: z.enum(["succeeded", "failed", "cancelled"]).optional() }, annotations: BASH_ANNOTATIONS
  }, args => {
    if (args.documents && !["checkpoint", "revise_plan", "finish_iteration"].includes(args.action)) workError("documents requires checkpoint, revise_plan or finish_iteration.");
    if ((args.todo_updates || args.acceptance_updates) && !["checkpoint", "revise_plan", "finish_iteration"].includes(args.action)) workError("Plan update pages require checkpoint, revise_plan or finish_iteration.");
    if (args.action === "put_document") {
      if (!args.title || args.content === undefined) workError("put_document requires title and content.");
      return result(service.putDocument(principal(), args, validateWorkDocumentReference(ctx)));
    }
    if (args.action === "resolve_operation") { if (!args.operation_id || !args.resolution || !args.reason) workError("resolve_operation requires operation_id, resolution and reason describing inspected evidence."); return result(service.resolve(principal(), args)); }
    if (args.action === "finish_iteration" && !args.outcome) workError("finish_iteration requires outcome.");
    const value = service.checkpoint(principal(), args, validateWorkDocumentReference(ctx)); service.sweep(); return result(value);
  });
}
