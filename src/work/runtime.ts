import fs from "node:fs";
import path from "node:path";
import os from "node:os";
import type { CodexProConfig } from "../config.js";
import { canonicalPlannedPath } from "../config.js";
import { isSubpath, PathGuard, type Workspace } from "../guard.js";
import type { WorkspaceAccess } from "../workspaceAccess.js";
import { WorktreeManager } from "../worktrees/manager.js";
import { currentToolContext, runWithToolContext, type ToolCallContext } from "../toolContext.js";
import { AuditJournal } from "../audit.js";
import { getJobManager, processIdentity, type JobRecord } from "../jobs.js";
import { assertVerificationCommand, makeRestrictedBashEnv } from "../bashOps.js";
import { MUTATING_WORKSPACE_TOOLS, SUPERTOOL_NAME } from "../tools/registry.js";
import { redactSensitiveText } from "../redact.js";
import { WorkStore } from "./store.js";
import { WorkCoordinator, digest, workError, workId } from "./coordinator.js";
import { observeSource } from "./source.js";
import { recentActivity } from "./activity.js";
import type { AcceptanceCheck, ExecutionEnvelope, IterationRecord, OperationRecord, RunRecord, WorkClock } from "./types.js";

function context(): ToolCallContext { return currentToolContext() ?? workError("Work operation requires authenticated request context."); }

/** One process owns a state directory. HTTP sessions share this instance. */
export class WorkRuntime {
  readonly coordinator: WorkCoordinator;
  readonly ready: Promise<void>;
  private readonly managers = new Map<string, Promise<WorktreeManager>>();
  private readonly loadedManagers = new Map<string, WorktreeManager>();
  private readonly busyCounts = new Map<string, number>();
  private readonly queues = new Map<string, Promise<unknown>>();
  private timer?: NodeJS.Timeout;
  private readonly owner: string;
  private stopped = false;
  private unobserve?: () => void;
  constructor(readonly config: CodexProConfig, clock?: WorkClock) {
    const work = config.work!;
    fs.mkdirSync(work.directory, { recursive: true, mode: 0o700 });
    const directory = fs.realpathSync(work.directory);
    for (const project of config.projects) if (isSubpath(directory, project.root) || isSubpath(project.root, directory)) workError("Work control storage must not overlap any project.");
    for (const other of [config.jobsDir, config.worktreeRoot].map(canonicalPlannedPath)) if (isSubpath(directory, other) || isSubpath(other, directory)) workError("Work, job and legacy worktree stores must be separate directories.");
    let bootId: string | undefined;
    try { bootId = fs.readFileSync("/proc/sys/kernel/random/boot_id", "utf8").trim(); } catch { /* Non-Linux: use process start identity. */ }
    this.owner = JSON.stringify({ pid: process.pid, identity: processIdentity(process.pid), host: os.hostname(), boot_id: bootId, nonce: workId("owner") });
    const store = new WorkStore(directory);
    // SQLite serializes competing owners and rolls back an interrupted handover.
    // No recovery lock file can survive a crash and strand the next coordinator.
    try { store.transaction(() => {
      const previous = store.meta("owner");
      if (previous) {
        let old: { pid: number; identity?: string; host: string; boot_id?: string };
        try { old = JSON.parse(previous); } catch { workError("Work owner record is unreadable. Inspect it before starting another coordinator."); }
        if (!old! || old!.host !== os.hostname() || !Number.isInteger(old!.pid) || old!.pid < 1) workError("Work directory is owned by an unverified process or another host.");
        let live = true; try { process.kill(old!.pid, 0); } catch (error) { live = (error as NodeJS.ErrnoException).code !== "ESRCH"; }
        const identity = live ? processIdentity(old!.pid) : undefined;
        const differentBoot = !!(bootId && old!.boot_id && bootId !== old!.boot_id);
        if (live && !differentBoot && (!old!.identity || !identity || old!.identity === identity)) workError("Another coordinator owns this work directory. Connect to its MCP endpoint instead.");
      }
      store.setMeta("owner", this.owner);
    }); } catch (error) { store.close(); throw error; }
    const jobs = getJobManager(config); const journal = new AuditJournal(config);
    this.coordinator = new WorkCoordinator(store, work, {
      contextDir: config.contextDir,
      provision: async run => {
        const workspace = (await (await this.manager(run.project_id)).createWorkspace(this.internalContext(run), { projectId: run.project_id, baseRef: run.base_ref, label: run.title, idempotencyKey: run.id })).workspace;
        const marker = new PathGuard(config).resolve(workspace, `${config.contextDir}/managed-run.json`, { forWrite: true }).absPath;
        fs.mkdirSync(path.dirname(marker), { recursive: true, mode: 0o700 }); fs.writeFileSync(marker, JSON.stringify({ run_id: run.id, instruction: "Use work_claim; do not start an unmanaged writer in this worktree." }), { mode: 0o600 });
        return workspace;
      },
      source: run => {
        try {
          if (!run.workspace) workError("Workspace is not provisioned.");
          const manager = this.loadedManagers.get(run.project_id); if (!manager) workError("Workspace catalog is unavailable.");
          const workspace = manager.getWorkspace(this.internalContext(run), run.workspace.id);
          return observeSource(workspace.root, work.sourceMaxBytes, config.contextDir, this.coordinator.now());
        } catch (error) { return { observed_at: this.coordinator.now(), complete: false, dirty_paths: [], error: redactSensitiveText(String(error)).slice(0, 1000) }; }
      },
      activity: (run, after) => recentActivity(journal, run.project_id, run.workspace?.id, after),
      jobs: run => {
        const retained = new Map(store.jobs<JobRecord>(run.id).map(j => [j.id, j]));
        for (const j of jobs.list(run.workspace?.id).filter(j => j.work?.run_id === run.id)) retained.set(j.id, j);
        return [...retained.values()];
      },
      stop: job => { jobs.stop(job.id); },
      startCheck: (run, check) => this.startCheck(run, check),
      busy: id => (this.busyCounts.get(id) ?? 0) > 0
    }, clock);
    this.unobserve = jobs.observe(job => store.transaction(() => {
      store.saveJob({ ...job, command: job.command_label });
      const op = job.work ? store.get<OperationRecord>("operations", job.work.operation_id) : undefined;
      if (op && op.run_id === job.work?.run_id && !op.job_ids.includes(job.id)) { op.job_ids.push(job.id); store.save("operations", op); }
    }));
    this.coordinator.recoverStartup();
    this.ready = this.initialize();
    this.ready.catch(error => console.error(`[CodexPro work] Initialization failed: ${String(error)}`));
  }
  private async initialize(): Promise<void> {
    for (const run of this.coordinator.store.runs()) {
      try {
        if (run.workspace) await this.manager(run.project_id);
        if (run.state === "provisioning") await this.coordinator.provision(run);
      } catch (error) {
        run.recovery_reason = `Workspace initialization unavailable: ${redactSensitiveText(String(error)).slice(0, 800)}`;
        if (!run.iteration_id && run.state !== "provisioning") run.state = "blocked";
        this.coordinator.changed(run, "workspace_unavailable");
      }
    }
    if (this.stopped) return;
    this.coordinator.sweep();
    this.timer = setInterval(() => { try { this.coordinator.sweep(); } catch (error) { console.error(`[CodexPro work] Reconciliation failed: ${String(error)}`); } }, this.config.work!.sweepMs);
    this.timer.unref();
  }
  private internalContext(run: RunRecord): ToolCallContext { return { principalId: run.principal_id, requestId: workId("internal"), signal: new AbortController().signal }; }
  private manager(projectId: string): Promise<WorktreeManager> {
    let promise = this.managers.get(projectId);
    if (!promise) {
      const project = this.config.projects.find(p => p.id === projectId); if (!project) workError("Project is not in the catalog.", "work_project_missing");
      const manager = new WorktreeManager({ ...this.config, projects: [project], defaultProjectId: projectId, worktreeRoot: path.join(this.config.work!.directory, "checkouts", digest(projectId).slice(0, 24)) });
      promise = manager.initialize().then(() => { this.loadedManagers.set(projectId, manager); return manager; }).catch(error => { this.managers.delete(projectId); throw error; }); this.managers.set(projectId, promise);
    }
    return promise;
  }
  private startCheck(run: RunRecord, check: AcceptanceCheck): JobRecord {
    assertVerificationCommand(this.config, check.command!);
    const limit = Math.max(1, Math.min(this.config.jobTimeoutMs, this.config.work!.attemptMs, run.limits.active_ms - run.measured_active_ms));
    const identity = { run_id: run.id, iteration_id: "verification", generation: run.generation, operation_id: `verify:${run.spec_revision}:${check.id}`, deadline_ms: Date.now() + limit, deadline_monotonic_ms: Number(process.hrtime.bigint() / 1_000_000n) + limit };
    return runWithToolContext({ ...this.internalContext(run), workExecution: identity, workJobPrepared: id => {
      const fresh = this.coordinator.store.get<RunRecord>("runs", run.id)!;
      if (fresh.state !== "verifying" || !fresh.verification) workError("Verification was revoked before launch.");
      fresh.verification.job_ids.push(id); this.coordinator.store.saveRun(fresh);
    } }, () => getJobManager(this.config).start({ workspaceId: run.workspace!.id, projectId: run.project_id, root: run.workspace!.root, cwdAbs: run.workspace!.root,
      cwdLabel: ".", command: check.command!, env: makeRestrictedBashEnv(this.config), origin: "background", timeoutMs: limit, outputLimitBytes: this.config.maxJobOutputBytes }));
  }
  workspace(run: RunRecord): Workspace {
    if (run.principal_id !== context().principalId) workError("Unknown or inaccessible workspace.", "work_not_found");
    const manager = this.loadedManagers.get(run.project_id); if (!manager) workError("Work coordinator is initializing; retry status shortly.");
    return manager.getWorkspace(context(), run.workspace!.id);
  }
  wrap(base: WorkspaceAccess): WorkspaceAccess {
    const runtime = this;
    function unmanaged(ws: Workspace): Workspace {
      const root = fs.realpathSync(ws.root), store = fs.realpathSync(runtime.config.work!.directory);
      if (isSubpath(root, store) || isSubpath(store, root)) workError("Use work_status and work_claim to access managed workspaces."); return ws;
    }
    return new Proxy(base, { get(target, key) {
      if (key === "getWorkspace") return (id?: string) => { const run = runtime.coordinator.forWorkspace(id); return run ? runtime.workspace(run) : unmanaged(target.getWorkspace(id)); };
      if (key === "execute") return async (id: string | undefined, mutating: boolean, fn: () => Promise<unknown>) => { await runtime.ready; const run = runtime.coordinator.forWorkspace(id); if (run) { runtime.workspace(run); return fn(); } return target.execute(id, mutating, fn); };
      if (key === "listWorkspaces") return () => [...target.listWorkspaces(), ...runtime.coordinator.store.runs(context().principalId).filter(r => r.workspace).map(r => runtime.workspace(r))];
      if (["removeWorkspace", "releaseWorkspace"].includes(String(key))) return (id: string) => { if (runtime.coordinator.forWorkspace(id)) workError("Managed run workspaces are retained by the coordinator. Finish or cancel the run first."); return (target[key as "removeWorkspace"] as any).call(target, id); };
      if (["openWorkspace", "openProject", "defaultWorkspace", "selectDefaultWorkspace"].includes(String(key))) return (...args: unknown[]) => unmanaged((target[key as "openWorkspace"] as any).apply(target, args));
      const value = Reflect.get(target, key); return typeof value === "function" ? value.bind(target) : value;
    } });
  }
  async invoke(name: string, args: any, handler: (args: any) => any): Promise<any> {
    await this.ready; if (this.stopped) workError("Work coordinator is closed."); this.coordinator.sweep();
    const ctx = context(); const envelope: ExecutionEnvelope | undefined = args.execution ?? ctx.workEnvelope;
    if (name.startsWith("work_")) return handler(args);
    if (name === SUPERTOOL_NAME) return runWithToolContext({ ...ctx, workEnvelope: envelope }, () => handler(args));
    const run = this.coordinator.forWorkspace(args.workspace_id);
    if (!run) {
      if (envelope) workError("An execution envelope requires the exact managed workspace_id returned by work_claim.");
      return handler(args);
    }
    this.workspace(run);
    const mutation = MUTATING_WORKSPACE_TOOLS.has(name) || name === "batch";
    // Unclaimed agents may read retained work to plan recovery. Reads with a token renew only its own current claim.
    if (!mutation) {
      if (envelope) this.coordinator.authorize(ctx.principalId, run.id, envelope.attempt_token);
      return handler(args);
    }
    const authorization = this.coordinator.authorize(ctx.principalId, run.id, envelope?.attempt_token);
    if (authorization.iteration.phase !== "execute") workError("A planning claim may read source and revise run documents, but cannot mutate the workspace.");
    if (!envelope?.operation_key) workError("Managed mutations require execution.operation_key for durable receipts.");
    const action = async () => {
      const auth = this.coordinator.authorize(ctx.principalId, run.id, envelope.attempt_token);
      const key = envelope.operation_key!; const { execution, ...effectArgs } = args;
      const fingerprint = digest({ name, args: effectArgs });
      const prior = this.coordinator.store.operation(run.id, key);
      if (prior) {
        if (prior.fingerprint !== fingerprint) workError("Operation key already describes different arguments.", "work_idempotency_conflict");
        if (["prepared", "running", "unknown"].includes(prior.state)) workError(`Operation ${prior.id} is ${prior.state}. Inspect its receipt and jobs; do not retry its effects.`, "work_operation_uncertain");
        if (prior.result) return prior.result;
        workError(`Operation ${prior.id} has a terminal receipt (${prior.state}); its full return was too large to retain. Read work_status(operation_id).`, "work_receipt_only");
      }
      if (this.coordinator.store.operationCount(run.id) >= 10_000) workError("Run operation capacity reached.", "work_capacity");
      const op: OperationRecord = { id: workId("op"), run_id: run.id, iteration_id: auth.iteration.id, generation: auth.iteration.generation,
        operation_key: key, fingerprint, tool: name, state: "prepared", started_at: this.coordinator.now(), job_ids: [], before: this.coordinator.host.source(run) };
      this.coordinator.store.save("operations", op);
      this.busyCounts.set(run.id, (this.busyCounts.get(run.id) ?? 0) + 1);
      const identity = { run_id: run.id, iteration_id: auth.iteration.id, generation: auth.iteration.generation, operation_id: op.id,
        deadline_ms: 0, deadline_monotonic_ms: 0 };
      try {
        const current = this.coordinator.authorize(ctx.principalId, run.id, envelope.attempt_token, false);
        const remaining = Math.max(1, Math.min(current.run.limits.attempt_ms - current.iteration.measured_ms, current.run.limits.active_ms - current.run.measured_active_ms));
        identity.deadline_ms = Date.now() + remaining; identity.deadline_monotonic_ms = Number(process.hrtime.bigint() / 1_000_000n) + remaining;
        op.state = "running"; this.coordinator.store.save("operations", op);
        const result = await runWithToolContext({ ...ctx, workEnvelope: envelope, workExecution: identity, workJobPrepared: jobId => {
          this.coordinator.authorize(ctx.principalId, run.id, envelope.attempt_token, false);
          const fresh = this.coordinator.store.get<OperationRecord>("operations", op.id)!; if (!fresh.job_ids.includes(jobId)) fresh.job_ids.push(jobId); this.coordinator.store.save("operations", fresh);
        } }, () => handler(args));
        const fresh = this.coordinator.store.get<OperationRecord>("operations", op.id)!;
        fresh.state = result?.isError ? "failed" : "succeeded"; fresh.finished_at = this.coordinator.now(); fresh.after = this.coordinator.host.source(run);
        const receipt = { ...result, structuredContent: { ...result?.structuredContent, work_receipt: { operation_id: op.id, run_id: run.id, iteration_id: op.iteration_id, generation: op.generation, job_ids: fresh.job_ids,
          effect_status: fresh.job_ids.length ? "inspect_jobs" : fresh.state } } };
        if (Buffer.byteLength(JSON.stringify(receipt)) <= 24_000) fresh.result = receipt;
        else if (name === "batch") {
          // Large child output must not hide whether the final checkpoint was
          // committed after a lost MCP return. Keep a compact durable replay.
          const data = receipt.structuredContent;
          const summary = Object.fromEntries(["workspace_id", "batch_path", "succeeded", "operation_count", "succeeded_count", "failed_count", "skipped_count", "failed_operation_id", "checkpoint", "work_receipt"].filter(key => data[key] !== undefined).map(key => [key, data[key]]));
          const compact = { ...summary, results_omitted: true, handling: "Replayed durable batch receipt; large child output was omitted. Inspect work_status operations and jobs for retained output. Successful effects and checkpoint were not rerun." };
          fresh.result = { ...(receipt.isError ? { isError: true } : {}), content: [{ type: "text", text: JSON.stringify(compact) }], structuredContent: compact };
        }
        this.coordinator.store.save("operations", fresh);
        const latest = this.coordinator.store.get<RunRecord>("runs", run.id)!;
        if (fresh.before?.fingerprint !== fresh.after?.fingerprint) {
          latest.last_change = fresh.after; this.coordinator.store.saveRun(latest);
          const it = this.coordinator.store.get<IterationRecord>("iterations", op.iteration_id)!; it.last_progress_at = this.coordinator.now(); it.progress_measured_ms = it.measured_ms; this.coordinator.store.save("iterations", it);
        }
        this.coordinator.store.event(latest, "operation_finished", this.coordinator.now(), { operation_id: op.id, tool: name, state: fresh.state, job_ids: fresh.job_ids });
        return receipt;
      } catch (error) {
        const fresh = this.coordinator.store.get<OperationRecord>("operations", op.id)!;
        fresh.state = "unknown"; fresh.error = redactSensitiveText(String(error)).slice(0, 2000); fresh.finished_at = this.coordinator.now(); fresh.after = this.coordinator.host.source(run);
        this.coordinator.store.save("operations", fresh); throw error;
      } finally { this.busyCounts.set(run.id, Math.max(0, (this.busyCounts.get(run.id) ?? 1) - 1)); }
    };
    // Batch children use this same admission path. The container must not hold a
    // non-reentrant lock while awaiting children; each actual effect is serialized.
    if (name === "batch") return action();
    const prior = this.queues.get(run.id) ?? Promise.resolve(); const pending = prior.catch(() => {}).then(action);
    this.queues.set(run.id, pending); try { return await pending; } finally { if (this.queues.get(run.id) === pending) this.queues.delete(run.id); }
  }
  close(): void {
    if (this.stopped) return;
    if (this.queues.size || [...this.busyCounts.values()].some(count => count > 0)) workError("Cannot release coordinator ownership while admitted calls are in flight.");
    this.stopped = true; if (this.timer) clearInterval(this.timer);
    this.unobserve?.();
    this.coordinator.store.db.prepare("DELETE FROM metadata WHERE key='owner' AND value=?").run(this.owner);
    this.coordinator.store.close();
  }
}
const runtimes = new Map<string, WorkRuntime>();
export function getWorkRuntime(config: CodexProConfig): WorkRuntime | undefined {
  if (!config.work?.enabled) return;
  const key = path.resolve(config.work.directory); let runtime = runtimes.get(key);
  if (!runtime) { runtime = new WorkRuntime(config); runtimes.set(key, runtime); } return runtime;
}
