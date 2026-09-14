import { OUTPUT_PAGE_MAX } from "./jobOutput.js";
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import type { CodexProConfig } from "./config.js";
import type { Workspace } from "./guard.js";
import { CodexProError, PathGuard } from "./guard.js";
import { getJobManager, type JobManager, type JobOrigin, type JobRecord, type JobStatus } from "./jobs.js";
import { redactSensitiveText } from "./redact.js";

export interface BashResult {
  command: string;
  cwd: string;
  exitCode: number | null;
  signal: NodeJS.Signals | null;
  durationMs: number;
  stdout: string;
  stderr: string;
  truncated: boolean;
  timedOut: boolean;
  bashSessionId?: string;
  /** Every bash command runs as a job; these describe it. */
  jobId: string;
  jobStatus: JobStatus;
  jobOrigin: JobOrigin;
}

const SAFE_ALLOWED_PREFIXES = [
  "pwd",
  "ls",
  "find",
  "git status",
  "git diff",
  "git log",
  "git show",
  "git branch",
  "git rev-parse",
  "git ls-files",
  "npm test",
  "npm run test",
  "npm run typecheck",
  "npm run lint",
  "npm run build",
  "npm run check",
  "pnpm test",
  "pnpm run test",
  "pnpm run typecheck",
  "pnpm run lint",
  "pnpm run build",
  "pnpm run check",
  "yarn test",
  "yarn run test",
  "yarn run typecheck",
  "yarn run lint",
  "yarn run build",
  "yarn run check",
  "bun test",
  "bun run test",
  "bun run typecheck",
  "bun run lint",
  "bun run build",
  "pytest",
  "python -m pytest",
  "python3 -m pytest",
  "uv run pytest",
  "go test",
  "cargo test",
  "cargo check",
  "cargo clippy",
  "tsc",
  "npx tsc",
  "eslint",
  "npx eslint",
  "biome check",
  "npx biome check"
];

const SAFE_BLOCKED_PATTERNS = [
  /(^|\s)rm\s+/,
  /(^|\s)mv\s+/,
  /(^|\s)cp\s+/,
  /(^|\s)dd\s+/,
  /(^|\s)sudo\s+/,
  /(^|\s)chmod\s+/,
  /(^|\s)chown\s+/,
  /(^|\s)kill\s+/,
  /(^|\s)pkill\s+/,
  /(^|\s)curl\s+/,
  /(^|\s)wget\s+/,
  /(^|\s)ssh\s+/,
  /(^|\s)scp\s+/,
  /(^|\s)rsync\s+/,
  /(^|\s)docker\s+/,
  /(^|\s)podman\s+/,
  /(^|\s)git\s+push\b/,
  /(^|\s)git\s+reset\b/,
  /(^|\s)git\s+clean\b/,
  /(^|\s)git\s+checkout\b/,
  /(^|\s)git\s+switch\b/,
  /(^|\s)git\s+restore\b/,
  /(^|\s)(npm|pnpm|yarn)\s+publish\b/,
  /(^|\s)--no-index\b/,
  /(^|\s)--fix\b/,
  /(^|\s)(\/|~(?:\/|\s|$))/,
  /(^|\s)\.\.(?:\/|\s|$)/,
  /\$/,
  /(^|[\s:])(?:\.env(?:[./\s:]|$)|\.git(?:[\/\s:]|$)|node_modules(?:[\/\s:]|$)|\.ssh(?:[\/\s:]|$)|id_rsa(?:[.\s:]|$)|id_ed25519(?:[.\s:]|$)|[^\s:]*\.(?:pem|key)(?:[\s:]|$))/,
  /(^|\s)['"]?-exec(?:['"]|\s|$)/,
  /(^|\s)['"]?-execdir(?:['"]|\s|$)/,
  /(^|\s)['"]?-delete(?:['"]|\s|$)/,
  /(^|\s)['"]?-ok(?:['"]|\s|$)/,
  /(^|\s)['"]?-okdir(?:['"]|\s|$)/,
  /(^|\s)['"]?-fprint0?(?:['"]|\s|$)/,
  /(^|\s)['"]?-fprintf(?:['"]|\s|$)/,
  /(^|\s)['"]?-fls(?:['"]|\s|$)/,
  /(^|\s)['"]?--output(?:=|['"]|\s|$)/,
  /(^|\s)(sed|perl)\s+.*(^|\s)-i(\s|$)/,
  /(^|\s)(cat|grep|rg|head|tail|wc)\s+/,
  /[;&|<>`]/,
  /[\r\n]/
];

function compact(command: string): string {
  return command.trim().replace(/\s+/g, " ");
}

function startsWithAllowedPrefix(command: string): boolean {
  const normalized = compact(command);
  return isAllowedPackageScript(normalized) || SAFE_ALLOWED_PREFIXES.some((prefix) => normalized === prefix || normalized.startsWith(`${prefix} `));
}

function isAllowedPackageScript(command: string): boolean {
  const packageScriptPattern =
    /^(?:npm|pnpm|yarn|bun)\s+run\s+(?:test|typecheck|lint|build|check)(?::[A-Za-z0-9._-]+)*(?:\s+--\s+[A-Za-z0-9._:= -]+)?$/;
  return packageScriptPattern.test(command);
}

function assertSafeAllowlistedCommand(command: string, context: "safe-mode" | "batch-verification"): void {
  const raw = command.trim();
  const normalized = compact(command);
  for (const pattern of SAFE_BLOCKED_PATTERNS) {
    if (pattern.test(raw) || pattern.test(normalized)) {
      throw new CodexProError(
        context === "batch-verification"
          ? `Batch-embedded Bash is verification-only and blocked this command: ${normalized}\nUse the standalone bash tool for deliberate trusted mutations.`
          : `Command is blocked in CODEXPRO_BASH_MODE=safe: ${normalized}\nUse separate read/search/git tools, or restart with CODEXPRO_BASH_MODE=full only for trusted repos.`,
        { code: "bash_blocked", retryUnchanged: false, details: { bash_context: context } }
      );
    }
  }
  if (!startsWithAllowedPrefix(normalized)) {
    throw new CodexProError(
      context === "batch-verification"
        ? `Batch-embedded Bash is verification-only and this command is not in the verification allowlist: ${normalized}\nAllowed examples include npm test, npm run typecheck, pytest, go test, cargo test, tsc, eslint, and git status.`
        : `Command is not in the safe bash allowlist: ${normalized}\nAllowed examples: ls, find, git status, git diff, npm test, npm run typecheck, npm run build:clients, pytest, go test, cargo test. Use read/search tools for file contents. Use CODEXPRO_BASH_MODE=full for trusted local automation.`,
      { code: "bash_blocked", retryUnchanged: false, details: { bash_context: context } }
    );
  }
}

/**
 * Batch-embedded bash follows the server's bash mode: full mode runs any
 * command (same as the standalone bash tool), safe mode keeps the
 * verification allowlist, off mode rejects.
 */
export function assertVerificationCommand(config: CodexProConfig, command: string): void {
  if (!command?.trim()) throw new CodexProError("command is required.", { code: "args_invalid", retryUnchanged: false });
  if (config.bashMode === "off") throw bashDisabledError();
  if (config.bashMode === "full") return;
  assertSafeAllowlistedCommand(command, "batch-verification");
}

function bashDisabledError(): CodexProError {
  return new CodexProError("bash tool is disabled. Start with CODEXPRO_BASH_MODE=safe or CODEXPRO_BASH_MODE=full to enable it.", {
    code: "bash_disabled",
    retryUnchanged: false
  });
}

function assertSafeCommand(config: CodexProConfig, command: string): void {
  if (config.bashMode === "off") throw bashDisabledError();
  if (config.bashMode === "full") return;
  assertSafeAllowlistedCommand(command, "safe-mode");
}

export function assertBashSession(config: CodexProConfig, sessionId?: string): string | undefined {
  const requested = sessionId?.trim();
  if (!config.bashSessionId) {
    if (config.requireBashSession) {
      throw new CodexProError("bash session guard is enabled but no server bash session id is configured.", { code: "bash_session_required", retryUnchanged: false });
    }
    return undefined;
  }
  if (!requested) {
    if (config.requireBashSession) {
      throw new CodexProError(`bash session id is required. Retry with session_id="${config.bashSessionId}".`, { code: "bash_session_required", retryUnchanged: false, recovery: { tool: "bash", message: "Retry with the server bash session id.", args: { session_id: config.bashSessionId } } });
    }
    return config.bashSessionId;
  }
  if (requested !== config.bashSessionId) {
    throw new CodexProError(`bash session id mismatch. This CodexPro server accepts session_id="${config.bashSessionId}".`, { code: "bash_session_mismatch", retryUnchanged: false, recovery: { tool: "bash", message: "Retry with the server bash session id.", args: { session_id: config.bashSessionId } } });
  }
  return config.bashSessionId;
}

function isUsableAbsoluteDir(candidate: string | undefined): string | undefined {
  if (!candidate) return undefined;
  const trimmed = candidate.trim();
  if (!trimmed) return undefined;
  if (!path.isAbsolute(trimmed) && !path.win32.isAbsolute(trimmed)) return undefined;
  try {
    const resolved = path.resolve(trimmed);
    if (fs.existsSync(resolved) && fs.statSync(resolved).isDirectory()) return resolved;
  } catch {
    // Ignore unreadable candidates and keep searching.
  }
  return undefined;
}

/** Resolve a usable absolute home for restricted child processes. Rejects relative junk like "=". */
export function resolveUsableHomeDir(env: NodeJS.ProcessEnv = process.env): string {
  return (
    isUsableAbsoluteDir(env.USERPROFILE) ??
    isUsableAbsoluteDir(env.HOME) ??
    isUsableAbsoluteDir(os.homedir()) ??
    path.resolve(os.homedir())
  );
}

export function makeRestrictedBashEnv(
  config: CodexProConfig,
  env: NodeJS.ProcessEnv = process.env
): NodeJS.ProcessEnv {
  if (config.inheritEnv) {
    return { ...env, NO_COLOR: "1", CI: env.CI ?? "1" };
  }
  const home = resolveUsableHomeDir(env);
  const restricted: NodeJS.ProcessEnv = {
    PATH: env.PATH ?? "/usr/local/bin:/usr/bin:/bin",
    HOME: home,
    USER: env.USER ?? env.USERNAME ?? "",
    SHELL: env.SHELL ?? "/bin/bash",
    TMPDIR: isUsableAbsoluteDir(env.TMPDIR) ?? isUsableAbsoluteDir(env.TMP) ?? os.tmpdir(),
    TERM: "dumb",
    NO_COLOR: "1",
    CI: "1"
  };
  if (process.platform === "win32") {
    restricted.USERPROFILE = home;
    const appData = isUsableAbsoluteDir(env.APPDATA);
    const localAppData = isUsableAbsoluteDir(env.LOCALAPPDATA);
    if (appData) restricted.APPDATA = appData;
    if (localAppData) restricted.LOCALAPPDATA = localAppData;
    if (env.USERNAME) restricted.USERNAME = env.USERNAME;
    if (env.HOMEDRIVE && env.HOMEPATH && path.win32.isAbsolute(path.win32.join(env.HOMEDRIVE, env.HOMEPATH))) {
      restricted.HOMEDRIVE = env.HOMEDRIVE;
      restricted.HOMEPATH = env.HOMEPATH;
    }
  }
  return restricted;
}

function makeEnv(config: CodexProConfig): NodeJS.ProcessEnv {
  return makeRestrictedBashEnv(config);
}

function bashExecutable(): string {
  return fs.existsSync("/bin/bash") ? "/bin/bash" : "bash";
}

function trimOutput(value: string, maxBytes: number): { value: string; truncated: boolean } {
  const buffer = Buffer.from(value, "utf8");
  if (buffer.byteLength <= maxBytes) return { value, truncated: false };
  const sliced = buffer.subarray(0, maxBytes).toString("utf8");
  return { value: `${sliced}\n...[output truncated to ${maxBytes} bytes]`, truncated: true };
}

export interface RunBashOptions {
  cwd?: string;
  timeoutMs?: number;
  sessionId?: string;
  inputJobIds?: string[];
  /** Start the command as a background job and return right away. */
  background?: boolean;
  /** What to do when a foreground command outruns timeout_ms. Default: background. */
  onTimeout?: "background" | "kill";
}

const BACKGROUND_GRACE_MS = 1_000;

function resultFromJob(config: CodexProConfig, jobs: JobManager, job: JobRecord, command: string, cwdLabel: string): BashResult {
  const running = job.status === "running";
  const output = running ? jobs.readTail(job, BASH_STATUS_TAIL_BYTES) : jobs.readOutput(job, Math.min(config.maxOutputBytes, OUTPUT_PAGE_MAX / 2));
  return {
    command,
    cwd: cwdLabel,
    exitCode: job.exit_code,
    signal: (job.signal as NodeJS.Signals | null) ?? null,
    durationMs: (job.finished_at_ms ?? Date.now()) - job.started_at_ms,
    stdout: output.stdout,
    stderr: output.stderr,
    truncated: output.truncated,
    timedOut: job.status === "timed_out",
    jobId: job.id,
    jobStatus: job.status,
    jobOrigin: job.origin,
    ...(job.bash_session_id ? { bashSessionId: job.bash_session_id } : {})
  };
}

const BASH_STATUS_TAIL_BYTES = 4 * 1024;

export async function runBash(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  command: string,
  options: RunBashOptions = {}
): Promise<BashResult> {
  if (!command?.trim()) throw new CodexProError("command is required.", { code: "args_invalid", retryUnchanged: false });
  const bashSessionId = assertBashSession(config, options.sessionId);
  assertSafeCommand(config, command);
  const cwdResolved = guard.resolve(workspace, options.cwd ?? ".");
  const cwdLabel = path.relative(workspace.root, cwdResolved.absPath) || ".";
  const timeoutMs = Math.max(1_000, Math.min(options.timeoutMs ?? config.bashTimeoutMs, config.maxBashTimeoutMs));
  const onTimeout = options.onTimeout ?? "background";
  const jobs = getJobManager(config);
  const base = {
    workspaceId: workspace.id,
    projectId: workspace.projectId,
    root: workspace.root,
    cwdAbs: cwdResolved.absPath,
    cwdLabel,
    command,
    env: makeEnv(config),
    bashSessionId,
    inputJobIds: options.inputJobIds
  };

  if (options.background) {
    const job = jobs.start({ ...base, origin: "background", timeoutMs: config.jobTimeoutMs, outputLimitBytes: config.maxJobOutputBytes });
    // Quick commands finish inside the grace period and come back complete.
    const settled = await jobs.wait(job.id, BACKGROUND_GRACE_MS);
    return resultFromJob(config, jobs, settled, command, cwdLabel);
  }

  const job = jobs.start({
    ...base,
    origin: "foreground",
    timeoutMs: onTimeout === "kill" ? Math.min(timeoutMs, config.jobTimeoutMs) : config.jobTimeoutMs,
    outputLimitBytes: config.maxJobOutputBytes
  });
  let settled = await jobs.wait(job.id, timeoutMs);
  if (settled.status === "running") {
    if (onTimeout === "background") {
      settled = jobs.promote(job.id, config.jobTimeoutMs);
    } else {
      // The runner's own deadline fires at the same moment; give it a beat to finalize.
      jobs.stop(job.id, "timeout");
      settled = await jobs.wait(job.id, KILL_ESCALATION_GRACE_MS);
    }
  }
  return resultFromJob(config, jobs, settled, command, cwdLabel);
}

const KILL_ESCALATION_GRACE_MS = 3_000;
