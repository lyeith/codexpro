import { fileURLToPath } from "node:url";
import { JobOutputStore, outputDir, workspaceOutputDir } from "./jobOutput.js";
import { pathRedactions } from "./pathLabels.js";
import { spawn, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { AuditJournal } from "./audit.js";
import type { CodexProConfig } from "./config.js";
import { CodexProError } from "./guard.js";
import { terminateProcessGroup } from "./processOps.js";
import { currentToolContext } from "./toolContext.js";
import type { ExecutionIdentity } from "./work/types.js";

/**
 * Background job runner for bash commands.
 *
 * Every bash command is started as a job with file-backed stdout/stderr and an
 * exit-code file written by an independent supervisor, so a command can outlive the tool
 * call that started it (promotion on timeout, or explicit background=true) and
 * even the server process. Under systemd the job runs in its own transient
 * scope so a service restart (KillMode=mixed) does not kill it; the job table is
 * persisted and re-attached on startup.
 */

export type JobStatus = "running" | "succeeded" | "failed" | "stopped" | "timed_out";
export type JobOrigin = "foreground" | "background" | "promoted";
export type JobStopReason = "timeout" | "stopped" | "output_limit" | "lost";

export interface JobRecord {
  id: string;
  workspace_id: string;
  project_id?: string;
  root: string;
  /** Working directory relative to the workspace root. */
  cwd: string;
  command: string;
  command_label: string;
  pid: number;
  scope_unit?: string;
  origin: JobOrigin;
  status: JobStatus;
  started_at: string;
  started_at_ms: number;
  finished_at?: string;
  finished_at_ms?: number;
  exit_code: number | null;
  signal: string | null;
  stop_reason?: JobStopReason;
  /** Hard deadline; the runner terminates the process tree past it. */
  deadline_ms: number;
  timeout_ms: number;
  /** Combined stdout+stderr byte budget; exceeding it terminates the job. */
  output_limit_bytes: number;
  stdout_path: string;
  stderr_path: string;
  exit_path: string;
  acknowledged: boolean;
  bash_session_id?: string;
  runner_version?: number;
  result_path?: string;
  control_path?: string;
  process_identity?: string;
  input_job_ids?: string[];
  output_expired?: boolean;
  captured_stdout_bytes?: number;
  captured_stderr_bytes?: number;
  work?: ExecutionIdentity;
  launch_state?: "prepared" | "granted";
  quiescent?: boolean;
  quiescence_scope?: "process_group" | "systemd_scope" | "not_launched";
  quiesced_at?: string;
  grant_path?: string;
}

export interface StartJobOptions {
  workspaceId: string;
  projectId?: string;
  root: string;
  cwdAbs: string;
  cwdLabel: string;
  command: string;
  env: NodeJS.ProcessEnv;
  origin: JobOrigin;
  timeoutMs: number;
  outputLimitBytes: number;
  bashSessionId?: string;
  inputJobIds?: string[];
}

export interface JobOutput {
  stdout: string;
  stderr: string;
  stdout_bytes: number;
  stderr_bytes: number;
  truncated: boolean;
}

interface JobTable {
  version: 1;
  jobs: JobRecord[];
}

const POLL_MS = 500;
const KILL_ESCALATION_MS = 1_500;

function commandLabel(command: string): string {
  const firstLine = command.trim().split(/\r?\n/)[0] ?? "";
  const compact = firstLine.replace(/\s+/g, " ");
  return compact.length > 80 ? `${compact.slice(0, 77)}…` : compact;
}

function pidAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === "EPERM";
  }
}

function fileSize(filePath: string): number {
  try {
    return fs.statSync(filePath).size;
  } catch {
    return 0;
  }
}

export function processIdentity(pid: number): string | undefined {
  try {
    if (process.platform === "linux") {
      const stat = fs.readFileSync(`/proc/${pid}/stat`, "utf8");
      return stat.slice(stat.lastIndexOf(")") + 2).split(" ")[19];
    }
    const result = spawnSync("ps", ["-p", String(pid), "-o", "lstart="], { encoding: "utf8", timeout: 1000 });
    return result.status === 0 ? result.stdout.trim() || undefined : undefined;
  } catch { return undefined; }
}

function detectSystemdScopes(): boolean {
  if (process.platform !== "linux" || !process.env.INVOCATION_ID || process.env.CODEXPRO_JOB_SCOPES === "0") return false;
  const probe = spawnSync("systemd-run", ["--user", "--scope", "--quiet", "--collect", "--unit", `codexpro-probe-${process.pid}`, "--", "true"], {
    stdio: "ignore",
    timeout: 5_000
  });
  return !probe.error && probe.status === 0;
}

export class JobManager {
  readonly output: JobOutputStore;
  private readonly dir: string;
  private readonly tablePath: string;
  private readonly jobs = new Map<string, JobRecord>();
  private readonly waiters = new Map<string, Array<() => void>>();
  private readonly drainWaiters = new Set<() => void>();
  private draining = false;
  private lastPruned = 0;
  private readonly killTimers = new Map<string, NodeJS.Timeout>();
  private readonly stopIntent = new Map<string, JobStopReason>();
  private poller?: NodeJS.Timeout;
  private journal?: AuditJournal;
  private readonly useScopes: boolean;

  constructor(private readonly config: CodexProConfig) {
    this.dir = config.jobsDir;
    this.output = new JobOutputStore(config);
    this.tablePath = path.join(this.dir, "jobs.json");
    fs.mkdirSync(this.dir, { recursive: true, mode: 0o700 });
    this.useScopes = detectSystemdScopes();
    this.load();
    this.reattach();
  }

  get scopesEnabled(): boolean {
    return this.useScopes;
  }
  private readonly observers = new Set<(job: JobRecord) => void>();
  observe(observer: (job: JobRecord) => void): () => void {
    this.observers.add(observer);
    for (const job of this.jobs.values()) observer(job);
    return () => { this.observers.delete(observer); };
  }
  private notify(job: JobRecord): void { for (const observer of this.observers) observer(job); }

  // ---- persistence ------------------------------------------------------

  private load(): void {
    try {
      const parsed = JSON.parse(fs.readFileSync(this.tablePath, "utf8")) as JobTable;
      if (parsed?.version === 1 && Array.isArray(parsed.jobs)) {
        for (const job of parsed.jobs) if (job && typeof job.id === "string") this.jobs.set(job.id, job);
      }
    } catch {
      // first run or unreadable table: start empty
    }
  }

  private persist(): void {
    const now = Date.now();
    const protectedIds = new Set(this.runningJobs().flatMap(j => j.input_job_ids ?? []));
    const finished = [...this.jobs.values()].filter(j => j.status !== "running")
      .sort((a, b) => (b.finished_at_ms ?? 0) - (a.finished_at_ms ?? 0));
    const counts = new Map<string, number>();
    const logBytes = (job: JobRecord) => fileSize(job.stdout_path) + fileSize(job.stderr_path) +
      fileSize(path.join(outputDir(this.config, job), "stdout.log")) + fileSize(path.join(outputDir(this.config, job), "stderr.log"));
    const pinnedBytes = finished.filter(j => protectedIds.has(j.id) && !j.output_expired).reduce((sum, j) => sum + logBytes(j), 0);
    const unpinnedBudget = Math.max(0, this.config.maxRetainedJobBytes - pinnedBytes);
    const used = { foreground: 0, background: 0 };
    for (const job of finished) {
      if (job.output_expired) continue;
      const category = job.origin === "foreground" ? "foreground" : "background";
      const key = `${job.workspace_id}:${category}`;
      const count = (counts.get(key) ?? 0) + 1; counts.set(key, count);
      const bytes = logBytes(job);
      if (protectedIds.has(job.id)) continue;
      used[category] += bytes;
      const cap = unpinnedBudget * (category === "foreground" ? 0.25 : 0.75);
      if (now - (job.finished_at_ms ?? now) <= this.config.jobRetentionMs &&
          count <= (category === "foreground" ? Math.min(20, this.config.maxJobHistoryPerWorkspace) : this.config.maxJobHistoryPerWorkspace) && used[category] <= cap) continue;
      job.captured_stdout_bytes = fileSize(job.stdout_path); job.captured_stderr_bytes = fileSize(job.stderr_path);
      job.output_expired = true; used[category] -= bytes;
      for (const file of [job.stdout_path, job.stderr_path, job.exit_path, job.result_path, job.control_path, job.grant_path,
        path.join(this.dir, `${job.id}.spec.json`), path.join(this.dir, `${job.id}.spec.json.started`)]) if (file) fs.rmSync(file, { force: true });
      this.output.remove(job);
    }
    // Bounded tombstones distinguish expired output from an invented job id.
    for (const job of finished.filter(j => j.output_expired).slice(500)) this.jobs.delete(job.id);
    const table: JobTable = { version: 1, jobs: [...this.jobs.values()] };
    const tmp = `${this.tablePath}.tmp-${process.pid}`;
    fs.writeFileSync(tmp, JSON.stringify(table, null, 2), { mode: 0o600 });
    fs.renameSync(tmp, this.tablePath);
  }

  private reattach(): void {
    for (const job of this.jobs.values()) {
      if (job.status !== "running") continue;
      if (fs.existsSync(job.exit_path)) {
        this.finalize(job, "exit-file");
      } else if (!this.alive(job)) {
        this.stopIntent.set(job.id, "lost");
        this.finalize(job, "lost");
      }
    }
    this.persist();
    this.ensurePoller();
  }

  // ---- lifecycle --------------------------------------------------------

  runningJobs(workspaceId?: string): JobRecord[] {
    return [...this.jobs.values()].filter((job) => job.status === "running" && (!workspaceId || job.workspace_id === workspaceId));
  }

  /** Jobs that count against the concurrency caps: background and promoted ones. */
  backgroundRunningCount(workspaceId?: string): number {
    return this.runningJobs(workspaceId).filter((job) => job.origin !== "foreground").length;
  }

  prune(): void { if (Date.now() - this.lastPruned > 1000) { this.lastPruned = Date.now(); this.persist(); } }

  list(workspaceId?: string): JobRecord[] {
    this.prune();
    return [...this.jobs.values()]
      .filter((job) => !workspaceId || job.workspace_id === workspaceId)
      .sort((left, right) => {
        if ((left.status === "running") !== (right.status === "running")) return left.status === "running" ? -1 : 1;
        return right.started_at_ms - left.started_at_ms;
      });
  }

  get(id: string): JobRecord | undefined {
    return this.jobs.get(id);
  }

  require(id: string, workspaceId?: string): JobRecord {
    this.prune();
    const job = this.jobs.get(id);
    if (!job || (workspaceId && job.workspace_id !== workspaceId)) {
      const known = this.list(workspaceId).slice(0, 10).map((item) => item.id);
      throw new CodexProError(`Unknown job_id: ${id}. Known jobs${workspaceId ? " for this workspace" : ""}: ${known.join(", ") || "none"}.`, {
        code: "job_not_found",
        retryUnchanged: false,
        details: { known_job_ids: known }
      });
    }
    return job;
  }

  start(options: StartJobOptions): JobRecord {
    const context = currentToolContext();
    const work = context?.workExecution;
    if (work) options = { ...options, timeoutMs: Math.max(1, Math.min(options.timeoutMs, work.deadline_ms - Date.now(), work.deadline_monotonic_ms === undefined ? Infinity : work.deadline_monotonic_ms - Number(process.hrtime.bigint() / 1_000_000n))) };
    const duplicate = this.runningJobs(options.workspaceId).find(
      (job) => job.origin !== "foreground" && job.command === options.command && job.cwd === options.cwdLabel && JSON.stringify([...(job.input_job_ids ?? [])].sort()) === JSON.stringify([...new Set(options.inputJobIds ?? [])].sort())
    );
    if (!work && duplicate && options.origin === "background") return duplicate;
    if (options.origin !== "foreground") this.assertCapacity(options.workspaceId);

    const inputIds = [...new Set(options.inputJobIds ?? [])];
    for (const input of inputIds) { const source = this.require(input, options.workspaceId); this.output.require(source); this.output.metadata(source); }
    const allInputs = new Set([...this.runningJobs().flatMap(j => j.input_job_ids ?? []), ...inputIds]);
    let reserved = 0;
    for (const id of allInputs) {
      const input = this.require(id);
      reserved += input.status === "running" ? input.output_limit_bytes * 3 + 4096 :
        fileSize(input.stdout_path) + fileSize(input.stderr_path) + fileSize(path.join(outputDir(this.config, input), "stdout.log")) + fileSize(path.join(outputDir(this.config, input), "stderr.log"));
    }
    if (reserved > this.config.maxRetainedJobBytes) throw new CodexProError("Pinned log inputs exceed the retained-output budget. Inspect fewer logs per command.", { code: "job_storage_limit", retryUnchanged: false });
    // Both foreground and background processes consume bounded capture storage.
    if (this.runningJobs().length >= this.config.maxJobs * 2) throw new CodexProError("Active command capacity reached.", { code: "job_limit_reached", retryUnchanged: false });
    const id = `job_${randomBytes(4).toString("hex")}`;
    const startedAtMs = Date.now();
    const stdoutPath = path.join(this.dir, `${id}.out`);
    const stderrPath = path.join(this.dir, `${id}.err`);
    const exitPath = path.join(this.dir, `${id}.exit`);
    const resultPath = path.join(this.dir, `${id}.result.json`);
    const controlPath = path.join(this.dir, `${id}.control`);
    const specPath = path.join(this.dir, `${id}.spec.json`);
    const grantPath = path.join(this.dir, `${id}.grant`);
    const nonce = randomBytes(24).toString("hex");
    const renderedDir = path.join(workspaceOutputDir(this.config, options.workspaceId), id);
    fs.mkdirSync(renderedDir, { recursive: true, mode: 0o700 });
    for (const file of [stdoutPath, stderrPath, path.join(renderedDir, "stdout.log"), path.join(renderedDir, "stderr.log")]) fs.writeFileSync(file, "", { mode: 0o600 });
    fs.writeFileSync(specPath, JSON.stringify({ command: options.command, cwd: options.cwdAbs, stdout: stdoutPath, stderr: stderrPath,
      exit: exitPath, result: resultPath, control: controlPath, outputDir: renderedDir, grant: grantPath, nonce,
      deadline: startedAtMs + options.timeoutMs, timeoutMs: options.timeoutMs, limit: options.outputLimitBytes,
      pathRedactions: this.config.exposeAbsolutePaths ? [] : pathRedactions(this.config, { root: options.root, workspace_id: options.workspaceId })
    }), { mode: 0o600 });
    const runnerArgs = [fileURLToPath(new URL("./jobRunner.js", import.meta.url)), specPath];
    const scopeUnit = this.useScopes ? `codexpro-${id}` : undefined;
    const argv = scopeUnit
      ? ["systemd-run", ["--user", "--scope", "--quiet", "--collect", "--unit", scopeUnit, "--", process.execPath, ...runnerArgs]] as const
      : [process.execPath, runnerArgs] as const;
    const env = { ...options.env, CODEXPRO_JOB_OUTPUT_DIR: workspaceOutputDir(this.config, options.workspaceId),
      ...(scopeUnit ? { XDG_RUNTIME_DIR: process.env.XDG_RUNTIME_DIR, DBUS_SESSION_BUS_ADDRESS: process.env.DBUS_SESSION_BUS_ADDRESS } : {}) };

    const job: JobRecord = {
      id,
      workspace_id: options.workspaceId,
      ...(options.projectId ? { project_id: options.projectId } : {}),
      root: options.root,
      cwd: options.cwdLabel,
      command: options.command,
      command_label: commandLabel(options.command),
      pid: 0,
      runner_version: 2, result_path: resultPath, control_path: controlPath,
      grant_path: grantPath, launch_state: "prepared", ...(work ? { work } : {}),
      input_job_ids: inputIds,
      ...(scopeUnit ? { scope_unit: scopeUnit } : {}),
      origin: options.origin,
      status: "running",
      started_at: new Date(startedAtMs).toISOString(),
      started_at_ms: startedAtMs,
      exit_code: null,
      signal: null,
      deadline_ms: startedAtMs + options.timeoutMs,
      timeout_ms: options.timeoutMs,
      output_limit_bytes: options.outputLimitBytes,
      stdout_path: stdoutPath,
      stderr_path: stderrPath,
      exit_path: exitPath,
      acknowledged: false,
      ...(options.bashSessionId ? { bash_session_id: options.bashSessionId } : {})
    };
    this.jobs.set(id, job);
    // Persist and attach to the operation BEFORE any command can execute. The
    // supervisor cannot launch Bash until the second durable registration grants it.
    this.persist();
    this.ensurePoller();
    let child;
    try {
      this.notify(job);
      context?.workJobPrepared?.(id);
      child = spawn(argv[0], [...argv[1]], { cwd: options.cwdAbs, env, stdio: "ignore", detached: process.platform !== "win32", windowsHide: true });
      child.on("error", () => {}); // Also handle spawn failure before a pid/registration exists.
      if (!child.pid) throw new Error("No supervisor pid");
      job.pid = child.pid;
      job.process_identity = processIdentity(child.pid);
      job.launch_state = "granted";
      this.persist();
      this.notify(job);
      fs.writeFileSync(grantPath, nonce, { mode: 0o600, flag: "wx" });
    } catch (error) {
      if (child?.pid) terminateProcessGroup(child.pid, "SIGTERM");
      job.quiescent = !fs.existsSync(grantPath);
      this.finalize(job, "lost");
      throw error;
    }
    child.unref();
    child.on("exit", (code, signal) => {
      const current = this.jobs.get(id);
      if (!current || current.status !== "running") return;
      // The exit file is the source of truth for the exit code (it survives a
      // restart and does not depend on how systemd-run propagates status).
      this.finalize(current, "exit-event", { code, signal });
    });
    child.on("error", () => {
      const current = this.jobs.get(id);
      if (current && current.status === "running") this.finalize(current, "lost");
    });
    this.persist();
    this.ensurePoller();
    return job;
  }

  private assertCapacity(workspaceId: string): void {
    const inWorkspace = this.backgroundRunningCount(workspaceId);
    const total = this.backgroundRunningCount();
    if (inWorkspace >= this.config.maxJobsPerWorkspace || total >= this.config.maxJobs) {
      const scope = inWorkspace >= this.config.maxJobsPerWorkspace
        ? `this workspace (${inWorkspace}/${this.config.maxJobsPerWorkspace})`
        : `the server (${total}/${this.config.maxJobs})`;
      throw new CodexProError(
        `Background job limit reached for ${scope}. Collect running jobs with jobs(job_ids, wait_ms) or stop some with stop_jobs.`,
        { code: "job_limit_reached", retryUnchanged: false, details: { running_in_workspace: inWorkspace, running_total: total } }
      );
    }
  }

  /** Remaining background capacity for a workspace, for validating a fan-out before starting anything. */
  capacity(workspaceId: string): number {
    return Math.max(0, Math.min(
      this.config.maxJobsPerWorkspace - this.backgroundRunningCount(workspaceId),
      this.config.maxJobs - this.backgroundRunningCount()
    ));
  }

  /** Turn a foreground job that outran its call into a background job (counts against the cap). */
  promote(id: string, timeoutMs: number): JobRecord {
    const job = this.require(id);
    if (job.status !== "running") return job;
    if (this.capacity(job.workspace_id) <= 0) {
      this.stop(id, "timeout");
      throw new CodexProError(
        `Command exceeded its timeout and could not be moved to the background: the job limit is reached. It was stopped.`,
        { code: "job_limit_reached", retryUnchanged: false }
      );
    }
    job.origin = "promoted";
    job.deadline_ms = Math.min(job.deadline_ms, job.started_at_ms + timeoutMs);
    job.timeout_ms = job.deadline_ms - job.started_at_ms;
    job.output_limit_bytes = this.config.maxJobOutputBytes;
    this.persist();
    return job;
  }

  /** Resolve when the job has finished or waitMs elapsed (never cut short by a drain: in-flight tool calls finish). */
  wait(id: string, waitMs: number): Promise<JobRecord> {
    return this.waitFor([id], "all", waitMs).then(() => this.require(id));
  }

  /**
   * Wait until all (or any) of the jobs have finished or waitMs elapsed. Pure
   * collect waits (interruptible) also return as soon as the server starts
   * draining, so a restart never strands a client mid-wait; foreground command
   * waits are not interruptible because the drain lets in-flight calls finish.
   */
  waitFor(
    ids: string[],
    mode: "all" | "any",
    waitMs: number,
    options: { interruptible?: boolean } = {}
  ): Promise<{ jobs: JobRecord[]; interrupted: boolean }> {
    const interruptible = options.interruptible === true;
    const records = ids.map((id) => this.require(id));
    const satisfied = () => {
      const finished = records.map((job) => (this.jobs.get(job.id) ?? job).status !== "running");
      return mode === "any" ? finished.some(Boolean) : finished.every(Boolean);
    };
    const snapshot = (interrupted: boolean) => ({ jobs: records.map((job) => this.jobs.get(job.id) ?? job), interrupted });
    if (satisfied() || waitMs <= 0) return Promise.resolve(snapshot(false));
    if (interruptible && this.draining) return Promise.resolve(snapshot(true));
    return new Promise((resolve) => {
      let done = false;
      const callbacks = new Map<string, () => void>();
      const cleanup = () => {
        clearTimeout(timer); this.drainWaiters.delete(onDrain);
        for (const [id, cb] of callbacks) {
          const remaining = (this.waiters.get(id) ?? []).filter(item => item !== cb);
          if (remaining.length) this.waiters.set(id, remaining); else this.waiters.delete(id);
        }
      };
      const finish = (interrupted: boolean, force = false) => {
        if (done || (!force && !interrupted && !satisfied())) return;
        done = true; cleanup(); resolve(snapshot(interrupted));
      };
      const onDrain = () => finish(true);
      const timer = setTimeout(() => { this.poll(); finish(false, true); }, waitMs);
      for (const job of records) {
        if (job.status !== "running") continue;
        const cb = () => finish(false); callbacks.set(job.id, cb);
        this.waiters.set(job.id, [...(this.waiters.get(job.id) ?? []), cb]);
      }
      if (interruptible) this.drainWaiters.add(onDrain);
    });
  }

  async waitOutput(job: JobRecord, cursor: string | undefined, waitMs: number): Promise<boolean> {
    const end = Date.now() + waitMs;
    while (!this.draining && job.status === "running" && Date.now() < end) {
      const page = this.output.page(job, cursor, 4);
      if (page.returned_bytes) break;
      await new Promise(resolve => setTimeout(resolve, Math.min(100, Math.max(0, end - Date.now()))));
    }
    return this.draining;
  }

  /** Called when the server drains for a restart: every pending wait returns at once with interrupted=true. */
  interruptWaits(): void {
    this.draining = true;
    for (const resolve of [...this.drainWaiters]) resolve();
    this.drainWaiters.clear();
  }

  stop(id: string, reason: JobStopReason = "stopped"): JobRecord {
    const job = this.require(id);
    if (job.status !== "running") return job;
    this.stopIntent.set(id, reason);
    this.terminate(job, "SIGTERM");
    if (!this.killTimers.has(id)) {
      const timer = setTimeout(() => {
        const current = this.jobs.get(id);
        if (current && current.status === "running") this.terminate(current, "SIGKILL");
        this.killTimers.delete(id);
      }, job.runner_version ? 5_000 : KILL_ESCALATION_MS);
      timer.unref();
      this.killTimers.set(id, timer);
    }
    return job;
  }

  private alive(job: JobRecord): boolean {
    if (job.pid <= 0) return false;
    return pidAlive(job.pid) && (!job.process_identity || processIdentity(job.pid) === job.process_identity);
  }

  private terminate(job: JobRecord, signal: NodeJS.Signals): void {
    if (job.runner_version && signal === "SIGTERM" && job.control_path) {
      fs.writeFileSync(job.control_path, this.stopIntent.get(job.id) ?? "stopped", { mode: 0o600 }); return;
    }
    if (!this.alive(job)) return;
    if (job.scope_unit) {
      spawnSync("systemctl", ["--user", "kill", `--signal=${signal}`, `${job.scope_unit}.scope`], { stdio: "ignore", timeout: 5_000 });
    }
    terminateProcessGroup(job.pid, signal);
  }

  acknowledge(ids: string[]): void {
    let changed = false;
    for (const id of ids) {
      const job = this.jobs.get(id);
      if (job && job.status !== "running" && !job.acknowledged) {
        job.acknowledged = true;
        changed = true;
      }
    }
    if (changed) this.persist();
  }

  // ---- output -----------------------------------------------------------

  readOutput(job: JobRecord, maxBytes: number): JobOutput { return this.output.read(job, maxBytes, "head"); }
  readTail(job: JobRecord, maxBytes: number): JobOutput { return this.output.read(job, maxBytes, "tail"); }

  // ---- completion -------------------------------------------------------

  private ensurePoller(): void {
    if (this.poller || !this.runningJobs().length) return;
    this.poller = setInterval(() => this.poll(), POLL_MS);
    this.poller.unref();
  }

  private poll(): void {
    const running = this.runningJobs();
    if (!running.length) {
      if (this.poller) clearInterval(this.poller);
      this.poller = undefined;
      return;
    }
    const now = Date.now();
    for (const job of running) {
      if (fs.existsSync(job.exit_path)) {
        this.finalize(job, "exit-file");
        continue;
      }
      if (!this.alive(job)) {
        if (!this.stopIntent.has(job.id)) this.stopIntent.set(job.id, "lost");
        this.finalize(job, "lost");
        continue;
      }
      if (now > job.deadline_ms && !this.stopIntent.has(job.id)) {
        this.stop(job.id, "timeout");
        continue;
      }
      if (fileSize(job.stdout_path) + fileSize(job.stderr_path) > job.output_limit_bytes && !this.stopIntent.has(job.id)) {
        this.stop(job.id, "output_limit");
      }
    }
  }

  private finalize(job: JobRecord, via: "exit-file" | "exit-event" | "lost", exit?: { code: number | null; signal: NodeJS.Signals | null }): void {
    if (job.status !== "running") return;
    let exitCode: number | null = null;
    try {
      const raw = fs.readFileSync(job.exit_path, "utf8").trim();
      if (/^\d+$/.test(raw)) exitCode = Number(raw);
    } catch {
      if (via === "exit-event" && exit && exit.code !== null) exitCode = exit.code;
    }
    let reason = this.stopIntent.get(job.id);
    let runnerResult: any;
    if (job.result_path) {
      try { runnerResult = JSON.parse(fs.readFileSync(job.result_path, "utf8"));
        exitCode = runnerResult.exit_code; reason = runnerResult.stop_reason ?? reason;
      } catch {}
    }
    this.stopIntent.delete(job.id);
    const timer = this.killTimers.get(job.id);
    if (timer) clearTimeout(timer);
    this.killTimers.delete(job.id);

    job.exit_code = exitCode;
    job.quiescent = runnerResult?.quiescent === true || (job.runner_version === 2 && !!job.grant_path && !fs.existsSync(job.grant_path));
    job.quiescence_scope = runnerResult ? "process_group" : job.quiescent ? "not_launched" : undefined;
    let started: { cgroup?: string } | undefined;
    if (job.scope_unit && !runnerResult) { try { started = JSON.parse(fs.readFileSync(path.join(this.dir, `${job.id}.spec.json.started`), "utf8")); } catch {} }
    if (job.scope_unit && (runnerResult || started)) {
      job.quiescent = this.settleScope(job, runnerResult?.cgroup ?? started?.cgroup);
      job.quiescence_scope = "systemd_scope";
    }
    if (job.quiescent) job.quiesced_at = new Date().toISOString();
    job.signal = runnerResult?.signal ?? exit?.signal ?? (reason ? "SIGTERM" : null);
    job.finished_at_ms = runnerResult?.finished_at_ms ?? Date.now();
    job.finished_at = new Date(job.finished_at_ms!).toISOString();
    job.stop_reason = reason;
    job.status = reason === "timeout"
      ? "timed_out"
      : reason === "stopped"
        ? "stopped"
        : reason ? "failed" : exitCode === 0 ? "succeeded" : "failed";
    if (!job.runner_version && (reason === "output_limit" || reason === "lost")) {
      fs.appendFileSync(job.stderr_path, reason === "output_limit"
        ? `\n[codexpro] Output exceeded ${job.output_limit_bytes} bytes; the command was stopped.\n`
        : "\n[codexpro] The command process disappeared before reporting an exit code.\n");
    }
    if (!job.runner_version && reason === "timeout") {
      fs.appendFileSync(job.stderr_path, `\n[codexpro] Command timed out after ${job.timeout_ms} ms.\n`);
    }
    this.persist();
    this.notify(job);
    for (const resolve of this.waiters.get(job.id) ?? []) resolve();
    this.waiters.delete(job.id);
    if (job.origin !== "foreground") this.journalCompletion(job);
  }

  /** The manager is outside the command scope and can terminate children that
   * daemonized into a different process group. Never equate the launcher's exit
   * with the scope being empty. */
  private settleScope(job: JobRecord, cgroup: unknown): boolean {
    if (process.platform !== "linux" || typeof cgroup !== "string" || path.basename(cgroup) !== `${job.scope_unit}.scope`) return false;
    const directory = path.resolve("/sys/fs/cgroup", `.${cgroup}`);
    if (!directory.startsWith("/sys/fs/cgroup/")) return false;
    const empty = (root: string): boolean => {
      try {
        if (fs.readFileSync(path.join(root, "cgroup.procs"), "utf8").trim()) return false;
        return fs.readdirSync(root, { withFileTypes: true }).filter(entry => entry.isDirectory()).every(entry => empty(path.join(root, entry.name)));
      } catch (error) { return (error as NodeJS.ErrnoException).code === "ENOENT"; }
    };
    if (empty(directory)) return true;
    spawnSync("systemctl", ["--user", "kill", "--kill-whom=all", "--signal=SIGKILL", `${job.scope_unit}.scope`], { stdio: "ignore", timeout: 2000 });
    const until = performance.now() + 1000;
    do {
      if (empty(directory)) return true;
      // A bounded D-Bus round trip yields CPU while the kernel reaps the scope.
      spawnSync("systemctl", ["--user", "show", "--property=ActiveState", `${job.scope_unit}.scope`], { stdio: "ignore", timeout: 200 });
    } while (performance.now() < until);
    return empty(directory);
  }

  private journalCompletion(job: JobRecord): void {
    try {
      if (!this.journal) this.journal = new AuditJournal(this.config);
      if (!this.journal.enabled) return;
      this.journal.record({
        toolName: "bash_job",
        args: { workspace_id: job.workspace_id, project_id: job.project_id, command: job.command, cwd: job.cwd, job_id: job.id, origin: job.origin },
        result: {
          isError: job.status !== "succeeded",
          structuredContent: {
            workspace_id: job.workspace_id,
            ...(job.work ? { work_receipt: job.work } : {}),
            ...(job.project_id ? { project_id: job.project_id } : {}),
            job_id: job.id,
            job_status: job.status,
            origin: job.origin,
            exit_code: job.exit_code,
            signal: job.signal,
            stop_reason: job.stop_reason,
            duration_ms: (job.finished_at_ms ?? Date.now()) - job.started_at_ms,
            timed_out: job.status === "timed_out",
            stdout_bytes: fileSize(job.stdout_path),
            stderr_bytes: fileSize(job.stderr_path)
          }
        },
        startedAtMs: job.started_at_ms,
        finishedAtMs: job.finished_at_ms ?? Date.now(),
        mutating: true
      });
    } catch (error) {
      console.error(`[CodexPro] failed to journal job ${job.id}: ${error instanceof Error ? error.message : String(error)}`);
    }
  }

  /** Compact status for the per-call "Background jobs" line; finished jobs are reported until acknowledged. */
  statusSummary(workspaceId?: string): { running: JobRecord[]; finished: JobRecord[] } {
    const jobs = this.list(workspaceId).filter((job) => job.origin !== "foreground");
    return {
      running: jobs.filter((job) => job.status === "running"),
      finished: jobs.filter((job) => job.status !== "running" && !job.acknowledged)
    };
  }
}

const managers = new Map<string, JobManager>();

export function getJobManager(config: CodexProConfig): JobManager {
  const key = path.resolve(config.jobsDir);
  let manager = managers.get(key);
  if (!manager) {
    manager = new JobManager(config);
    managers.set(key, manager);
  }
  return manager;
}

export function elapsedLabel(ms: number): string {
  if (ms < 1_000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1_000).toFixed(ms < 10_000 ? 1 : 0)} s`;
  return `${(ms / 60_000).toFixed(1)} min`;
}
