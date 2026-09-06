import { spawnSync } from "node:child_process";
import type { CodexProConfig } from "./config.js";
import type { Workspace } from "./guard.js";
import { CodexProError, PathGuard } from "./guard.js";
import { redactSensitiveText } from "./redact.js";

function runGit(workspace: Workspace, args: string[], maxOutputBytes: number): string {
  const result = spawnSync("git", args, {
    cwd: workspace.root,
    encoding: "utf8",
    maxBuffer: maxOutputBytes,
    env: { ...process.env, NO_COLOR: "1" }
  });
  if (result.error) {
    return `git unavailable or failed: ${result.error.message}`;
  }
  if (result.status !== 0) {
    const stderr = result.stderr?.trim() || "";
    const stdout = result.stdout?.trim() || "";
    return stderr || stdout || `git exited with status ${result.status}`;
  }
  return redactSensitiveText(result.stdout.trim() || "(no output)");
}

function isGitFailure(output: string): boolean {
  const trimmed = output.trim().toLowerCase();
  return (
    trimmed.startsWith("fatal:") ||
    trimmed.startsWith("error:") ||
    trimmed.startsWith("git unavailable or failed:") ||
    trimmed.startsWith("git exited with status") ||
    trimmed.startsWith("usage: git ") ||
    trimmed.includes("not a git repository")
  );
}

function outputLines(output: string): string[] {
  return output.trim() === "(no output)" ? [] : output.split("\n").map((line) => line.trim()).filter(Boolean);
}

export function gitStatus(config: CodexProConfig, workspace: Workspace, guard?: PathGuard, filePath?: string, staged = false): string {
  const args = staged ? ["diff", "--cached", "--name-status"] : ["status", "--short", "--branch"];
  if (filePath?.trim()) {
    if (!guard) return "path-scoped git status requires a path guard";
    const resolved = guard.resolve(workspace, filePath);
    args.push("--", resolved.relPath);
  }
  return runGit(workspace, args, config.maxOutputBytes);
}

export function gitDiff(config: CodexProConfig, guard: PathGuard, workspace: Workspace, filePath?: string, staged = false): string {
  const args = ["diff", "--no-color", "--no-ext-diff", "--no-textconv"];
  if (staged) args.push("--staged");
  if (filePath?.trim()) {
    const resolved = guard.resolve(workspace, filePath);
    args.push("--", resolved.relPath);
  }
  return runGit(workspace, args, config.maxOutputBytes);
}

export function gitDiffStatus(config: CodexProConfig, guard: PathGuard, workspace: Workspace, filePath?: string, staged = false): string {
  const args = ["diff", "--name-status"];
  if (staged) args.push("--staged");
  const untrackedArgs = ["ls-files", "--others", "--exclude-standard"];
  if (filePath?.trim()) {
    const resolved = guard.resolve(workspace, filePath);
    args.push("--", resolved.relPath);
    untrackedArgs.push("--", resolved.relPath);
  }
  const diffStatus = runGit(workspace, args, config.maxOutputBytes);
  if (staged || isGitFailure(diffStatus)) return diffStatus;
  const untracked = runGit(workspace, untrackedArgs, config.maxOutputBytes);
  if (isGitFailure(untracked)) return diffStatus;
  const lines = [...outputLines(diffStatus), ...outputLines(untracked).map((line) => `?? ${line}`)];
  return lines.length ? lines.join("\n") : "(no output)";
}

export function gitLog(config: CodexProConfig, workspace: Workspace, maxCount = 8): string {
  const count = Math.max(1, Math.min(Math.floor(maxCount), 30));
  return runGit(workspace, ["log", `--max-count=${count}`, "--oneline", "--decorate"], config.maxOutputBytes);
}

export interface GitCommitResult {
  commit: string;
  branch: string;
  message: string;
  files: string[];
  skipped_blocked: string[];
  summary: string;
}

/**
 * Stage and commit workspace changes. Blocked paths (secrets, artifacts) are
 * never staged even with all=true; explicit paths must be inside the workspace.
 */
export function gitCommit(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  options: { message: string; paths?: string[]; all?: boolean }
): GitCommitResult {
  const message = options.message.trim();
  if (!message) throw new CodexProError("message is required.", { code: "args_invalid", retryUnchanged: false });
  const inside = runGit(workspace, ["rev-parse", "--is-inside-work-tree"], 8 * 1024);
  if (isGitFailure(inside) || inside.trim() !== "true") {
    throw new CodexProError(`Not a Git working tree: ${inside}`, { code: "git_unavailable", retryUnchanged: false });
  }

  let candidates: string[];
  if (options.paths?.length) {
    candidates = options.paths.map((item) => guard.resolve(workspace, item).relPath);
  } else {
    const status = runGit(workspace, ["status", "--porcelain=v1", "-uall", "-z"], config.maxOutputBytes);
    if (isGitFailure(status)) throw new CodexProError(status, { code: "git_unavailable", retryUnchanged: false });
    candidates = status === "(no output)"
      ? []
      : status.split("\0").filter((entry) => entry.length > 3).map((entry) => entry.slice(3));
  }
  const skippedBlocked = candidates.filter((item) => guard.isBlockedRelativePath(item));
  const files = candidates.filter((item) => !guard.isBlockedRelativePath(item));
  if (!files.length) {
    throw new CodexProError(
      skippedBlocked.length
        ? `Nothing to commit: every changed path is blocked by safety rules (${skippedBlocked.join(", ")}).`
        : "Nothing to commit: the working tree has no changes.",
      { code: "nothing_to_commit", retryUnchanged: false, recovery: { tool: "show_changes", message: "Review the working tree first." } }
    );
  }
  const staged = runGit(workspace, ["add", "--", ...files], config.maxOutputBytes);
  if (isGitFailure(staged)) throw new CodexProError(staged, { code: "git_command_failed", retryUnchanged: false });
  const commit = spawnSync("git", ["commit", "--quiet", "--only", "-F", "-", "--", ...files], {
    cwd: workspace.root,
    encoding: "utf8",
    input: message,
    maxBuffer: config.maxOutputBytes,
    env: { ...process.env, NO_COLOR: "1" }
  });
  if (commit.error || commit.status !== 0) {
    const detail = commit.error?.message || commit.stderr?.trim() || commit.stdout?.trim() || `git exited with status ${commit.status}`;
    throw new CodexProError(`git commit failed: ${redactSensitiveText(detail)}`, { code: "git_command_failed", retryUnchanged: false });
  }
  const sha = runGit(workspace, ["rev-parse", "--short=12", "HEAD"], 8 * 1024);
  const branch = runGit(workspace, ["branch", "--show-current"], 8 * 1024);
  const summary = runGit(workspace, ["show", "--stat", "--oneline", "--no-color", "-1", "HEAD"], config.maxOutputBytes);
  return {
    commit: sha.trim(),
    branch: branch.trim() === "(no output)" ? "detached" : branch.trim(),
    message,
    files,
    skipped_blocked: skippedBlocked,
    summary
  };
}

export function assertGitCleanEnoughForWrite(_workspace: Workspace): void {
  // Reserved for future policy hooks. The first version allows writes and returns diffs.
  return;
}
