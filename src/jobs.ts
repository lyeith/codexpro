import { spawn, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { AuditJournal } from "./audit.js";
import type { CodexProConfig } from "./config.js";
import { CodexProError } from "./guard.js";
import { terminateProcessGroup } from "./processOps.js";
import { redactSensitiveText } from "./redact.js";

/**
 * Background job runner for bash commands.
 *
 * Every bash command is started as a job with file-backed stdout/stderr and an
 * exit-code file written by a wrapper shell, so a command can outlive the tool
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
const FINISHED_HISTORY = 50;
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

function readHead(filePath: string, maxBytes: number): { text: string; bytes: number; truncated: boolean } {
  const bytes = fileSize(filePath);
  if (!bytes) return { text: "", bytes: 0, truncated: false };
  const length = Math.min(bytes, maxBytes);
  const fd = fs.openSync(filePath, "r");
  try {
    const buffer = Buffer.alloc(length);
    fs.readSync(fd, buffer, 0, length, 0);
    const text = buffer.toString("utf8");
    return bytes > maxBytes
      ? { text: `${text}\n...[output truncated to ${maxBytes} bytes]`, bytes, truncated: true }
      : { text, bytes, truncated: false };
  } finally {
    fs.closeSync(fd);
  }
}

function readTail(filePath: string, maxBytes: number): { text: string; bytes: number; truncated: boolean } {
  const bytes = fileSize(filePath);
  if (!bytes) return { text: "", bytes: 0, truncated: false };
  const length = Math.min(bytes, maxBytes);
  const fd = fs.openSync(filePath, "r");
  try {
    const buffer = Buffer.alloc(length);
    fs.readSync(fd, buffer, 0, length, bytes - length);
    const text = buffer.toString("utf8").replace(/^�+/, "");
    return bytes > maxBytes ? { text: `…\n${text}`, bytes, truncated: true } : { text, bytes, truncated: false };
  } finally {
    fs.closeSync(fd);
  }
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
  private readonly dir: string;
  private readonly tablePath: string;
  private readonly jobs = new Map<string, JobRecord>();
  private readonly waiters = new Map<string, Array<() => void>>();
  private readonly killTimers = new Map<string, NodeJS.Timeout>();
  private readonly stopIntent = new Map<string, JobStopReason>();
  private poller?: NodeJS.Timeout;
  private journal?: AuditJournal;
  private readonly useScopes: boolean;

  constructor(private readonly config: CodexProConfig) {
    this.dir = config.jobsDir;
    this.tablePath = path.join(this.dir, "jobs.json");
    fs.mkdirSync(this.dir, { recursive: true, mode: 0o700 });
    this.useScopes = detectSystemdScopes();
    this.load();
    this.reattach();
  }

  get scopesEnabled(): boolean {
    return this.useScopes;
  }

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
    const finished = [...this.jobs.values()].filter((job) => job.status !== "running")
      .sort((left, right) => (right.finished_at_ms ?? 0) - (left.finished_at_ms ?? 0));
    for (const stale of finished.slice(FINISHED_HISTORY)) {
      this.jobs.delete(stale.id);
      for (const file of [stale.stdout_path, stale.stderr_path, stale.exit_path]) fs.rmSync(file, { force: true });
    }
    const table: JobTable = { version: 1, jobs: [...this.jobs.values()] };
    const tmp = `${this.tablePath}.tmp-${process.pid}`;
    fs.writeFileSync(tmp, JSON.stringify(table, null, 2), { mode: 0o600 });
    fs.renameSync(tmp, this.tablePath);
  }

  private reattach(): void {
    let changed = false;
    for (const job of this.jobs.values()) {
      if (job.status !== "running") continue;
      if (fs.existsSync(job.exit_path)) {
        this.finalize(job, "exit-file");
        changed = true;
      } else if (!pidAlive(job.pid)) {
        this.stopIntent.set(job.id, "lost");
        this.finalize(job, "lost");
        changed = true;
      }
    }
    if (changed) this.persist();
    this.ensurePoller();
  }

  // ---- lifecycle --------------------------------------------------------

  runningJobs(workspaceId?: string): JobRecord[] {
    return [...this.jobs.values()].filter((job) => job.status === "running" && (!workspaceId || job.workspace_id === workspaceId));
  }

  /** Jobs that count against the concurrency cap: background and promoted ones. */
  backgroundRunningCount(): number {
    return this.runningJobs().filter((job) => job.origin !== "foreground").length;
  }

  list(workspaceId?: string): JobRecord[] {
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
    if (options.origin !== "foreground") this.assertCapacity();
    const duplicate = this.runningJobs(options.workspaceId).find(
      (job) => job.origin !== "foreground" && job.command === options.command && job.cwd === options.cwdLabel
    );
    if (duplicate && options.origin === "background") return duplicate;

    const id = `job_${randomBytes(4).toString("hex")}`;
    const stdoutPath = path.join(this.dir, `${id}.out`);
    const stderrPath = path.join(this.dir, `${id}.err`);
    const exitPath = path.join(this.dir, `${id}.exit`);
    const outFd = fs.openSync(stdoutPath, "a", 0o600);
    const errFd = fs.openSync(stderrPath, "a", 0o600);
    const bashExe = fs.existsSync("/bin/bash") ? "/bin/bash" : "bash";
    // $0 = command, $1 = exit file. Quoting through argv keeps arbitrary command text intact.
    const wrapper = `"${bashExe}" -lc "$0"; printf %s "$?" > "$1"`;
    const wrapperArgs = ["-c", wrapper, options.command, exitPath];
    const scopeUnit = this.useScopes ? `codexpro-${id}` : undefined;
    const argv = scopeUnit
      ? ["systemd-run", ["--user", "--scope", "--quiet", "--collect", "--unit", scopeUnit, "--", bashExe, ...wrapperArgs]] as const
      : [bashExe, wrapperArgs] as const;
    const env = scopeUnit
      ? { ...options.env, XDG_RUNTIME_DIR: process.env.XDG_RUNTIME_DIR, DBUS_SESSION_BUS_ADDRESS: process.env.DBUS_SESSION_BUS_ADDRESS }
      : options.env;

    let child;
    try {
      child = spawn(argv[0], [...argv[1]], {
        cwd: options.cwdAbs,
        env,
        stdio: ["ignore", outFd, errFd],
        detached: process.platform !== "win32",
        windowsHide: true
      });
    } finally {
      fs.closeSync(outFd);
      fs.closeSync(errFd);
    }
    if (!child.pid) throw new CodexProError("Failed to start the command process.", { code: "job_start_failed", retryUnchanged: false });
    child.unref();

    const startedAtMs = Date.now();
    const job: JobRecord = {
      id,
      workspace_id: options.workspaceId,
      ...(options.projectId ? { project_id: options.projectId } : {}),
      root: options.root,
      cwd: options.cwdLabel,
      command: options.command,
      command_label: commandLabel(options.command),
      pid: child.pid,
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

  private assertCapacity(): void {
    const running = this.backgroundRunningCount();
    if (running >= this.config.maxJobs) {
      throw new CodexProError(
        `Background job limit reached (${running}/${this.config.maxJobs} running). Wait for one with jobs(job_id, wait_ms) or stop one with stop_job.`,
        { code: "job_limit_reached", retryUnchanged: false, recovery: { tool: "bash", message: "Collect or stop a running job first." } }
      );
    }
  }

  /** Turn a foreground job that outran its call into a background job (counts against the cap). */
  promote(id: string, timeoutMs: number): JobRecord {
    const job = this.require(id);
    if (job.status !== "running") return job;
    if (this.backgroundRunningCount() >= this.config.maxJobs) {
      this.stop(id, "timeout");
      throw new CodexProError(
        `Command exceeded its timeout and could not be moved to the background: job limit reached (${this.config.maxJobs}). It was stopped.`,
        { code: "job_limit_reached", retryUnchanged: false }
      );
    }
    job.origin = "promoted";
    job.deadline_ms = job.started_at_ms + timeoutMs;
    job.timeout_ms = timeoutMs;
    job.output_limit_bytes = this.config.maxJobOutputBytes;
    this.persist();
    return job;
  }

  /** Resolve when the job has finished or waitMs elapsed. */
  wait(id: string, waitMs: number): Promise<JobRecord> {
    const job = this.require(id);
    if (job.status !== "running" || waitMs <= 0) return Promise.resolve(job);
    return new Promise((resolve) => {
      let done = false;
      const finish = () => {
        if (done) return;
        done = true;
        clearTimeout(timer);
        resolve(this.jobs.get(id) ?? job);
      };
      const timer = setTimeout(finish, waitMs);
      timer.unref();
      const list = this.waiters.get(id) ?? [];
      list.push(finish);
      this.waiters.set(id, list);
    });
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
      }, KILL_ESCALATION_MS);
      timer.unref();
      this.killTimers.set(id, timer);
    }
    return job;
  }

  private terminate(job: JobRecord, signal: NodeJS.Signals): void {
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

  /** Bounded head of both streams (what a completed foreground call returns). */
  readOutput(job: JobRecord, maxBytes: number): JobOutput {
    const out = readHead(job.stdout_path, maxBytes);
    const err = readHead(job.stderr_path, maxBytes);
    return {
      stdout: redactSensitiveText(out.text),
      stderr: redactSensitiveText(err.text),
      stdout_bytes: out.bytes,
      stderr_bytes: err.bytes,
      truncated: out.truncated || err.truncated
    };
  }

  /** Bounded tail of both streams (status views). */
  readTail(job: JobRecord, maxBytes: number): JobOutput {
    const out = readTail(job.stdout_path, maxBytes);
    const err = readTail(job.stderr_path, maxBytes);
    return {
      stdout: redactSensitiveText(out.text),
      stderr: redactSensitiveText(err.text),
      stdout_bytes: out.bytes,
      stderr_bytes: err.bytes,
      truncated: out.truncated || err.truncated
    };
  }

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
      if (!pidAlive(job.pid)) {
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
    const reason = this.stopIntent.get(job.id);
    this.stopIntent.delete(job.id);
    const timer = this.killTimers.get(job.id);
    if (timer) clearTimeout(timer);
    this.killTimers.delete(job.id);

    job.exit_code = exitCode;
    job.signal = exit?.signal ?? (reason ? "SIGTERM" : null);
    job.finished_at_ms = Date.now();
    job.finished_at = new Date(job.finished_at_ms).toISOString();
    job.stop_reason = reason;
    job.status = reason === "timeout"
      ? "timed_out"
      : reason === "stopped"
        ? "stopped"
        : exitCode === 0 ? "succeeded" : "failed";
    if (reason === "output_limit" || reason === "lost") {
      fs.appendFileSync(job.stderr_path, reason === "output_limit"
        ? `\n[codexpro] Output exceeded ${job.output_limit_bytes} bytes; the command was stopped.\n`
        : "\n[codexpro] The command process disappeared before reporting an exit code.\n");
    }
    if (reason === "timeout") {
      fs.appendFileSync(job.stderr_path, `\n[codexpro] Command timed out after ${job.timeout_ms} ms.\n`);
    }
    this.persist();
    for (const resolve of this.waiters.get(job.id) ?? []) resolve();
    this.waiters.delete(job.id);
    if (job.origin !== "foreground") this.journalCompletion(job);
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
            ...(job.project_id ? { project_id: job.project_id } : {}),
            job_id: job.id,
            job_status: job.status,
            origin: job.origin,
            exit_code: job.exit_code,
            signal: job.signal,
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
