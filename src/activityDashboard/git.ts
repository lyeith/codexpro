import { spawnSync } from "node:child_process";
import fs from "node:fs";
import type { CodexProConfig } from "../config.js";
import { PathGuard } from "../guard.js";
import type { ProjectDefinition } from "../projects/types.js";
import { redactSensitiveText } from "../redact.js";
import { isSafeDashboardPath, normalizeGitPath, unique } from "./format.js";
import type { ActivityDashboardGit } from "./types.js";

const MAX_DIFF_PATHS = 120;
const MAX_DIFF_BYTES = 512 * 1024;
const MAX_GIT_METADATA_BYTES = 256 * 1024;
const GIT_TIMEOUT_MS = 4_000;

interface GitRunResult {
  ok: boolean;
  stdout: string;
  truncated: boolean;
}

function runGit(root: string, args: string[], maxBytes = MAX_GIT_METADATA_BYTES): GitRunResult {
  const result = spawnSync("git", args, {
    cwd: root,
    encoding: "utf8",
    maxBuffer: maxBytes,
    timeout: GIT_TIMEOUT_MS,
    env: {
      ...process.env,
      GIT_OPTIONAL_LOCKS: "0",
      GIT_TERMINAL_PROMPT: "0",
      LC_ALL: "C",
      NO_COLOR: "1"
    }
  });
  const stdout = typeof result.stdout === "string" ? result.stdout : "";
  const errorCode = result.error && typeof result.error === "object" && "code" in result.error
    ? String(result.error.code)
    : "";
  const truncated = errorCode === "ENOBUFS" || Buffer.byteLength(stdout, "utf8") >= maxBytes;
  if (truncated && stdout) {
    return {
      ok: true,
      stdout: `${stdout.slice(0, maxBytes)}\n… diff output truncated by CodexPro …\n`,
      truncated: true
    };
  }
  return {
    ok: !result.error && result.status === 0,
    stdout,
    truncated: false
  };
}

function splitNul(value: string): string[] {
  return value.split("\0").filter((item) => item.length > 0);
}

function parseShortStat(value: string): { additions: number; deletions: number } {
  const additions = /(?:^|,)\s*(\d+) insertion(?:s)?\(\+\)/.exec(value)?.[1];
  const deletions = /(?:^|,)\s*(\d+) deletion(?:s)?\(-\)/.exec(value)?.[1];
  return {
    additions: additions ? Number(additions) : 0,
    deletions: deletions ? Number(deletions) : 0
  };
}

function unavailableGit(message: string): ActivityDashboardGit {
  return {
    available: false,
    message,
    dirty: false,
    trackedChangedPaths: [],
    untrackedPaths: [],
    hiddenPathCount: 0,
    omittedPathCount: 0,
    additions: 0,
    deletions: 0,
    diff: "",
    diffTruncated: false
  };
}

export function collectProjectGit(
  config: CodexProConfig,
  project: ProjectDefinition,
  guard = new PathGuard(config)
): ActivityDashboardGit {
  try {
    if (!fs.existsSync(project.root) || !fs.statSync(project.root).isDirectory()) {
      return unavailableGit("Project root is unavailable.");
    }
  } catch {
    return unavailableGit("Project root is unavailable.");
  }

  const inside = runGit(project.root, ["rev-parse", "--is-inside-work-tree"], 8 * 1024);
  if (!inside.ok || inside.stdout.trim() !== "true") {
    return unavailableGit("Not a Git working tree.");
  }

  const verifiedHead = runGit(project.root, ["rev-parse", "--verify", "HEAD"], 8 * 1024);
  const hasHead = verifiedHead.ok;
  const branchResult = runGit(project.root, ["branch", "--show-current"], 8 * 1024);
  const headResult = hasHead ? runGit(project.root, ["rev-parse", "--short=12", "HEAD"], 8 * 1024) : undefined;
  const committedAtResult = hasHead ? runGit(project.root, ["log", "-1", "--format=%cI"], 8 * 1024) : undefined;

  const trackedResult = hasHead
    ? runGit(project.root, ["diff", "--relative", "--name-only", "-z", "HEAD", "--", "."])
    : { ok: true, stdout: "", truncated: false };
  const untrackedResult = runGit(project.root, ["ls-files", "--others", "--exclude-standard", "-z", "--", "."]);
  if (!trackedResult.ok || !untrackedResult.ok) {
    return unavailableGit("Git working-tree status could not be read.");
  }

  const allTracked = unique(splitNul(trackedResult.stdout).map(normalizeGitPath));
  const allUntracked = unique(splitNul(untrackedResult.stdout).map(normalizeGitPath));
  const safeTracked = allTracked.filter((item) => isSafeDashboardPath(guard, item));
  const safeUntracked = allUntracked.filter((item) => isSafeDashboardPath(guard, item));
  const hiddenPathCount = allTracked.length + allUntracked.length - safeTracked.length - safeUntracked.length;
  const renderedTracked = safeTracked.slice(0, MAX_DIFF_PATHS);
  const remainingSlots = Math.max(0, MAX_DIFF_PATHS - renderedTracked.length);
  const renderedUntracked = safeUntracked.slice(0, remainingSlots);
  const omittedPathCount = safeTracked.length + safeUntracked.length - renderedTracked.length - renderedUntracked.length;

  let diff = "";
  let diffTruncated = false;
  let additions = 0;
  let deletions = 0;
  if (hasHead && renderedTracked.length) {
    const stat = runGit(project.root, ["diff", "--relative", "--shortstat", "HEAD", "--", ...renderedTracked]);
    if (stat.ok) ({ additions, deletions } = parseShortStat(stat.stdout));
    const renderedDiff = runGit(
      project.root,
      ["diff", "--relative", "--no-color", "--no-ext-diff", "--no-textconv", "HEAD", "--", ...renderedTracked],
      MAX_DIFF_BYTES
    );
    if (renderedDiff.ok) {
      diff = redactSensitiveText(renderedDiff.stdout.trim());
      diffTruncated = renderedDiff.truncated;
    } else {
      diff = "Tracked diff is too large or could not be rendered; the changed-path summary remains available.";
      diffTruncated = true;
    }
  } else if (!hasHead) {
    diff = "This Git working tree has no commit yet.";
  }

  return {
    available: true,
    branch: branchResult.ok && branchResult.stdout.trim() ? redactSensitiveText(branchResult.stdout.trim()) : "detached",
    head: headResult?.ok ? headResult.stdout.trim() : undefined,
    committedAt: committedAtResult?.ok ? committedAtResult.stdout.trim() : undefined,
    dirty: allTracked.length > 0 || allUntracked.length > 0,
    trackedChangedPaths: renderedTracked.map(redactSensitiveText),
    untrackedPaths: renderedUntracked.map(redactSensitiveText),
    hiddenPathCount,
    omittedPathCount,
    additions,
    deletions,
    diff,
    diffTruncated
  };
}
