import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import {
  AuditJournal,
  type ActionStatusResult,
  type CodexProActionV1,
  type CodexProDashboardActionV1,
  type GitEvidence,
  type PathEvidence
} from "./audit.js";
import type { CodexProConfig } from "./config.js";
import { PathGuard, normalizeRelPath } from "./guard.js";
import type { ProjectDefinition } from "./projects/types.js";
import { redactSensitiveText } from "./redact.js";

const ACTIONS_PER_PROJECT = 8;
const MAX_DIFF_PATHS = 120;
const MAX_DIFF_BYTES = 512 * 1024;
const MAX_GIT_METADATA_BYTES = 256 * 1024;
const GIT_TIMEOUT_MS = 4_000;

export interface ActivityDashboardField {
  key: string;
  label: string;
  value: string;
  mono?: boolean;
  tone?: "positive" | "negative" | "muted";
}

export interface ActivityDashboardEvidence {
  label: string;
  value: string;
}

export interface ActivityDashboardGitEvidence {
  branch?: string;
  head?: string;
  dirty: boolean;
  changedPathCount: number;
}

export interface ActivityDashboardAction {
  actionId: string;
  sequence: number;
  finishedAt: string;
  toolName: string;
  operation: string;
  operationClass: CodexProActionV1["operation_class"];
  status: CodexProActionV1["status"];
  durationMs: number;
  mutating: boolean;
  headline: string;
  changedPaths: string[];
  hiddenPathCount: number;
  changedPathsTruncated: boolean;
  requestFields: ActivityDashboardField[];
  resultFields: ActivityDashboardField[];
  pathEvidence: ActivityDashboardEvidence[];
  gitBefore?: ActivityDashboardGitEvidence;
  gitAfter?: ActivityDashboardGitEvidence;
  errorCode?: string;
  batchPath?: string;
  batchHref?: string;
  shellScripts: Array<{
    operationId?: string;
    script: string;
    truncated: boolean;
  }>;
}

export interface ActivityDashboardGit {
  available: boolean;
  message?: string;
  branch?: string;
  head?: string;
  committedAt?: string;
  dirty: boolean;
  trackedChangedPaths: string[];
  untrackedPaths: string[];
  hiddenPathCount: number;
  omittedPathCount: number;
  additions: number;
  deletions: number;
  diff: string;
  diffTruncated: boolean;
}

export interface ActivityDashboardProject {
  id: string;
  label: string;
  latestActivityAt?: string;
  actions: ActivityDashboardAction[];
  git: ActivityDashboardGit;
}

export interface ActivityDashboardSnapshot {
  generatedAt: string;
  audit: ActionStatusResult;
  projects: ActivityDashboardProject[];
}

export interface ActivityBatchView {
  projectId: string;
  projectLabel: string;
  workspaceId?: string;
  path: string;
  autoStored: boolean;
  definition: unknown;
}

interface GitRunResult {
  ok: boolean;
  stdout: string;
  truncated: boolean;
}


type SplitDiffTone = "context" | "removed" | "added" | "empty";

interface SplitDiffCell {
  lineNumber?: number;
  text: string;
  tone: SplitDiffTone;
}

type SplitDiffRow =
  | { kind: "line"; before: SplitDiffCell; after: SplitDiffCell }
  | { kind: "note"; text: string };

interface SplitDiffHunk {
  header: string;
  rows: SplitDiffRow[];
}

interface SplitDiffFile {
  beforePath: string;
  afterPath: string;
  metadata: string[];
  hunks: SplitDiffHunk[];
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

function unique(values: string[]): string[] {
  return [...new Set(values)];
}

function normalizeGitPath(value: string): string {
  return normalizeRelPath(value).replace(/^\.\//, "");
}

function isSafeDashboardPath(guard: PathGuard, value: string): boolean {
  const normalized = normalizeGitPath(value);
  return Boolean(
    normalized &&
    normalized !== "." &&
    !path.isAbsolute(normalized) &&
    !normalized.startsWith("../") &&
    !guard.isBlockedRelativePath(normalized)
  );
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

const METADATA_LABELS: Record<string, string> = {
  additions: "Lines added",
  ast_kind: "AST node kind",
  ast_language: "AST language",
  ast_mode: "AST query mode",
  ast_provider: "AST provider",
  ast_provider_version: "AST provider version",
  ast_selector: "AST selector",
  ast_strictness: "AST strictness",
  already_open: "Already open",
  already_open_count: "Already-open workspaces",
  auto_stored: "Auto-stored",
  batch_path: "Batch file",
  batch_source: "Batch source",
  batch_tag: "Batch tag",
  pruned_batch_paths_truncated: "Pruned batch list truncated",
  base_ref: "Base ref",
  bytes: "Result size",
  changed: "Changed",
  changed_files_count: "Changed files",
  changed_paths_count: "Changed paths",
  child_structured_truncated_count: "Structured child results truncated",
  child_text_truncated_count: "Child outputs truncated",
  command_bytes: "Command length",
  command_digest: "Command fingerprint",
  command_label: "Safe command label",
  command_name: "Command family",
  content_bytes: "Content size",
  config_format: "Configuration format",
  continue_on_error: "Continue on error",
  context_after: "Context after",
  context_before: "Context before",
  contexts_count: "Context blocks",
  cursor_supplied: "Continuation cursor supplied",
  count: "Count",
  created: "Created",
  create_dirs: "Create directories",
  cwd: "Working directory",
  deletions: "Lines removed",
  directory: "Directory",
  duration_ms: "Reported duration",
  diff_target: "Git comparison target",
  efficiency_hint: "Efficiency guidance",
  edit_content_bytes: "Edit content size",
  edit_mode: "Edit mode",
  edit_operations: "Edit operations",
  edit_tag_supplied: "Edit tag supplied",
  edits_applied: "Edits applied",
  editable_matches_count: "Editable matches",
  end_line: "End line",
  error_code: "Error code",
  existed: "Already existed",
  exit_code: "Exit code",
  expected_replacements: "Expected replacements",
  expected_sha256_supplied: "SHA-256 precondition",
  failed_count: "Failed operations",
  files_count: "Files",
  glob: "File filter",
  globs_count: "Glob filters",
  include_diff: "Include diff",
  group_by_file: "Group context by file",
  has_more: "More matches available",
  include_hidden: "Include hidden",
  include_relationships: "Include relationships",
  include_symbols: "Include symbols",
  include_untracked: "Include untracked files",
  include_tree: "Include tree",
  initial_branch: "Initial branch",
  intent: "Search intent",
  is_error: "Tool error",
  matches_count: "Matches",
  max_bytes: "Byte limit",
  max_depth: "Maximum depth",
  max_entries: "Maximum entries",
  max_files: "Maximum files",
  max_results: "Maximum results",
  max_worktrees: "Maximum worktrees",
  file_mutation_count: "File mutations",
  mode: "Mode",
  new_text_bytes: "Replacement text",
  old_text_bytes: "Matched text",
  operation_count: "Operations",
  output_limited: "Output limited",
  output_truncated: "Aggregate output truncated",
  overwrite: "Overwrite",
  parent_id: "Parent project",
  patch_bytes: "Patch size",
  pattern_bytes: "Pattern size",
  pattern_digest: "Pattern fingerprint",
  persist: "Persist batch",
  persistence_default: "Persistence default",
  persistence_requested: "Persistence requested",
  path: "Path",
  paths_count: "Paths",
  project_id: "Project",
  project_ids_count: "Projects requested",
  query_bytes: "Query length",
  query_digest: "Query fingerprint",
  query_fingerprint: "Search page fingerprint",
  regex: "Regular expression",
  replace_all: "Replace all",
  replacements: "Replacements",
  recovery_tool: "Recovery tool",
  repository_supplied: "Repository supplied",
  requested_root_digest: "Root fingerprint",
  retry_unchanged: "Retry unchanged",
  session_id_supplied: "Bash session supplied",
  signal: "Signal",
  skipped_count: "Skipped operations",
  search_kind: "Search kind",
  search_scope: "Search scope",
  search_used: "Search engine",
  source: "Source",
  source_supplied: "Source supplied",
  staged: "Staged",
  start_line: "Start line",
  state: "State",
  status: "Reported status",
  stderr_bytes: "Standard error",
  stdout_bytes: "Standard output",
  succeeded: "Succeeded",
  succeeded_count: "Succeeded operations",
  target_path_count: "Patch targets",
  timed_out: "Timed out",
  timeout_ms: "Timeout",
  truncated: "Truncated",
  executed_operation_count: "Executed operations",
  failed_index: "Failed index",
  failed_operation_id: "Failed operation",
  from_index: "Resume index",
  from_operation: "Resume operation",
  git_excluded: "Locally Git-excluded",
  persisted: "Persisted",
  pruned_batch_count: "Pruned batch files",
  resumable_from: "Resumable from",
  retention_limit: "Batch retention limit",
  start_index: "Start index",
  start_operation_id: "Start operation",
  total_operation_count: "Total operations",
  verification_command_count: "Verification commands",
  workspace_id: "Workspace",
  workspace_results_truncated_count: "Workspace results truncated",
  workspaces_count: "Workspaces opened",
  warnings_count: "Warnings"
};

const METADATA_ORDER = [
  "command_label", "command_name", "path", "batch_path", "cwd", "glob", "globs_count", "intent", "search_kind", "search_scope", "config_format", "regex",
  "ast_mode", "ast_language", "ast_kind", "ast_selector", "ast_strictness", "ast_provider", "ast_provider_version", "pattern_bytes",
  "context_before", "context_after", "group_by_file", "cursor_supplied", "base_ref", "diff_target", "include_untracked", "max_results", "include_hidden",
  "batch_source", "batch_tag", "persist", "persisted", "persistence_default", "persistence_requested", "auto_stored", "git_excluded", "retention_limit", "pruned_batch_count", "efficiency_hint",
  "mode", "from_operation", "from_index", "start_operation_id", "start_index", "operation_count", "total_operation_count", "executed_operation_count", "file_mutation_count", "verification_command_count", "edit_mode", "edit_tag_supplied", "edit_operations", "error_code", "retry_unchanged",
  "start_line", "end_line", "old_text_bytes", "new_text_bytes", "edit_content_bytes", "content_bytes", "patch_bytes",
  "expected_replacements", "replace_all", "expected_sha256_supplied", "continue_on_error", "timeout_ms", "session_id_supplied",
  "exit_code", "signal", "timed_out", "additions", "deletions", "replacements", "edits_applied", "bytes",
  "succeeded_count", "failed_count", "failed_operation_id", "failed_index", "resumable_from", "skipped_count", "child_text_truncated_count", "child_structured_truncated_count",
  "stdout_bytes", "stderr_bytes", "matches_count", "editable_matches_count", "contexts_count", "has_more", "search_used", "warnings_count", "changed_files_count", "changed_paths_count", "files_count",
  "paths_count", "count", "already_open", "already_open_count", "changed", "created", "existed", "succeeded", "truncated", "output_limited",
  "output_truncated", "workspace_results_truncated_count", "state", "status", "project_ids_count", "workspaces_count", "project_id", "workspace_id",
  "command_digest", "query_digest", "pattern_digest", "query_fingerprint"
];

function metadataNumber(metadata: Record<string, unknown>, key: string): number | undefined {
  const value = metadata[key];
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function metadataString(metadata: Record<string, unknown>, key: string): string | undefined {
  const value = metadata[key];
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function metadataBoolean(metadata: Record<string, unknown>, key: string): boolean | undefined {
  const value = metadata[key];
  return typeof value === "boolean" ? value : undefined;
}

function humanBytes(value: number): string {
  const absolute = Math.abs(value);
  if (absolute < 1024) return `${value} B`;
  if (absolute < 1024 * 1024) return `${(value / 1024).toFixed(absolute < 10 * 1024 ? 1 : 0)} KiB`;
  return `${(value / (1024 * 1024)).toFixed(absolute < 10 * 1024 * 1024 ? 1 : 0)} MiB`;
}

function humanDuration(value: number): string {
  if (value < 1000) return `${value} ms`;
  if (value < 60_000) return `${(value / 1000).toFixed(value < 10_000 ? 1 : 0)} s`;
  return `${(value / 60_000).toFixed(1)} min`;
}

function metadataLabel(key: string): string {
  if (METADATA_LABELS[key]) return METADATA_LABELS[key];
  return key.replaceAll("_", " ").replace(/\b\w/g, (value) => value.toUpperCase());
}

function metadataValue(key: string, value: unknown): string | undefined {
  if (typeof value === "boolean") return value ? "yes" : "no";
  if (typeof value === "number" && Number.isFinite(value)) {
    if (key === "bytes" || key.endsWith("_bytes")) return humanBytes(value);
    if (key.endsWith("_ms")) return humanDuration(value);
    return String(value);
  }
  if (typeof value === "string") {
    const clean = redactSensitiveText(value.trim());
    if (!clean) return undefined;
    if (key.endsWith("_digest") || key.endsWith("_fingerprint")) {
      return `sha256:${clean.slice(0, 12)}${clean.length > 12 ? "…" : ""}`;
    }
    return clean;
  }
  if (Array.isArray(value)) return `${value.length} entries`;
  return undefined;
}

function metadataFields(metadata: Record<string, unknown>, guard: PathGuard): ActivityDashboardField[] {
  const hasCommandLabel = Boolean(metadataString(metadata, "command_label"));
  const pathKeys = new Set(["path", "batch_path", "cwd", "directory"]);
  const fields: ActivityDashboardField[] = [];
  for (const [key, value] of Object.entries(metadata)) {
    if (hasCommandLabel && key === "command_name") continue;
    if (pathKeys.has(key) && typeof value === "string" && !isSafeDashboardPath(guard, value)) continue;
    const formatted = metadataValue(key, value);
    if (formatted === undefined) continue;
    fields.push({
      key,
      label: metadataLabel(key),
      value: formatted,
      mono: pathKeys.has(key) || key.endsWith("_digest") || key.endsWith("_fingerprint") || key.endsWith("_id") || key === "command_label",
      tone: key === "additions" ? "positive" : key === "deletions" || key === "stderr_bytes" ? "negative" : undefined
    });
  }
  return fields.sort((left, right) => {
    const leftIndex = METADATA_ORDER.indexOf(left.key);
    const rightIndex = METADATA_ORDER.indexOf(right.key);
    if (leftIndex >= 0 || rightIndex >= 0) {
      return (leftIndex < 0 ? Number.MAX_SAFE_INTEGER : leftIndex) - (rightIndex < 0 ? Number.MAX_SAFE_INTEGER : rightIndex);
    }
    return left.label.localeCompare(right.label);
  });
}

function safeActionPaths(action: CodexProActionV1, guard: PathGuard): { paths: string[]; hidden: number } {
  const candidates = unique(action.changed_paths.map(normalizeGitPath));
  const paths = candidates.filter((item) => isSafeDashboardPath(guard, item)).map(redactSensitiveText);
  return { paths, hidden: candidates.length - paths.length };
}

function requestPath(action: CodexProActionV1, guard: PathGuard): string | undefined {
  for (const key of ["path", "cwd", "directory"]) {
    const value = metadataString(action.request_metadata, key);
    if (value && isSafeDashboardPath(guard, value)) return redactSensitiveText(normalizeGitPath(value));
  }
  return undefined;
}

function deltaLabel(metadata: Record<string, unknown>): string | undefined {
  const additions = metadataNumber(metadata, "additions");
  const deletions = metadataNumber(metadata, "deletions");
  if (additions === undefined && deletions === undefined) return undefined;
  return `+${additions ?? 0} −${deletions ?? 0}`;
}

function plural(value: number, singular: string, pluralValue = `${singular}s`): string {
  return `${value} ${value === 1 ? singular : pluralValue}`;
}

function actionHeadline(action: CodexProActionV1, changedPaths: string[], guard: PathGuard): string {
  const request = action.request_metadata;
  const result = action.result_metadata;
  const target = changedPaths[0] ?? requestPath(action, guard);
  const delta = deltaLabel(result);
  const count = (key: string) => metadataNumber(result, key);

  switch (action.tool_name) {
    case "bash": {
      const label = metadataString(request, "command_label") ?? metadataString(request, "command_name") ?? "Command";
      const exitCode = metadataNumber(result, "exit_code");
      const timedOut = metadataBoolean(result, "timed_out");
      return [label, timedOut ? "timed out" : exitCode !== undefined ? `exit ${exitCode}` : undefined].filter(Boolean).join(" · ");
    }
    case "edit": {
      const edits = metadataNumber(result, "edits_applied") ?? metadataNumber(request, "edit_operations");
      const replacements = metadataNumber(result, "replacements") ?? metadataNumber(request, "expected_replacements");
      const work = edits !== undefined
        ? plural(edits, "operation")
        : replacements !== undefined
          ? plural(replacements, "replacement")
          : undefined;
      return [target ?? "File edit", delta, work].filter(Boolean).join(" · ");
    }
    case "batch": {
      const operations = metadataNumber(result, "operation_count") ?? metadataNumber(request, "operation_count");
      const totalOperations = metadataNumber(result, "total_operation_count");
      const operationLabel = operations !== undefined
        ? totalOperations !== undefined && totalOperations !== operations
          ? `${operations} of ${totalOperations} operations`
          : plural(operations, "operation")
        : "Batch";
      const failed = metadataNumber(result, "failed_count");
      const skipped = metadataNumber(result, "skipped_count");
      const outcome = failed
        ? plural(failed, "failure")
        : skipped
          ? plural(skipped, "skipped operation")
          : metadataBoolean(result, "succeeded") === true
            ? "completed"
            : undefined;
      const startIndex = metadataNumber(result, "start_index") ?? metadataNumber(request, "from_index");
      const startId = metadataString(result, "start_operation_id") ?? metadataString(request, "from_operation");
      const resumed = startIndex !== undefined && startIndex > 0 ? `from ${startId ?? `#${startIndex}`}` : undefined;
      const stored = metadataString(result, "batch_path") ? (metadataBoolean(result, "auto_stored") ? "saved" : "stored") : undefined;
      const paths = action.changed_path_count ? plural(action.changed_path_count, "changed path") : undefined;
      return [operationLabel, resumed ?? stored, outcome, paths].filter(Boolean).join(" · ");
    }
    case "write": {
      const bytes = metadataNumber(result, "bytes") ?? metadataNumber(request, "content_bytes");
      const created = metadataBoolean(result, "created");
      return [target ?? "File write", delta, bytes !== undefined ? humanBytes(bytes) : undefined, created ? "created" : undefined].filter(Boolean).join(" · ");
    }
    case "apply_patch": {
      const pathCount = action.changed_path_count || metadataNumber(request, "target_path_count") || changedPaths.length;
      return [plural(pathCount, "path"), delta].filter(Boolean).join(" · ");
    }
    case "import_file": {
      const bytes = metadataNumber(result, "bytes");
      return [target ?? "Imported file", bytes !== undefined ? humanBytes(bytes) : undefined].filter(Boolean).join(" · ");
    }
    case "read": {
      const start = metadataNumber(request, "start_line");
      const end = metadataNumber(request, "end_line");
      const bytes = metadataNumber(result, "bytes");
      const range = start !== undefined || end !== undefined ? `lines ${start ?? 1}${end !== undefined ? `–${end}` : "+"}` : undefined;
      return [target ?? "File read", range, bytes !== undefined ? humanBytes(bytes) : undefined, metadataBoolean(result, "truncated") ? "truncated" : undefined].filter(Boolean).join(" · ");
    }
    case "search": {
      const matches = count("matches_count") ?? count("count");
      const kind = metadataString(request, "search_kind") ?? metadataString(result, "search_kind");
      const scope = metadataString(request, "search_scope") ?? metadataString(result, "search_scope");
      const label = target ?? (kind === "config" ? "Configuration query" : "Repository search");
      return [
        label,
        matches !== undefined ? plural(matches, "match", "matches") : undefined,
        kind === "config" ? "config" : metadataBoolean(request, "regex") ? "regex" : undefined,
        scope && scope !== "workspace" ? scope.replaceAll("_", " ") : undefined,
        metadataBoolean(result, "has_more") ? "more available" : undefined
      ].filter(Boolean).join(" · ");
    }
    case "ast_grep": {
      const matches = count("matches_count") ?? count("count");
      const language = metadataString(request, "ast_language");
      const mode = metadataString(result, "ast_mode");
      const kind = metadataString(request, "ast_kind");
      const version = metadataString(result, "ast_provider_version");
      return [
        target ?? "Structural search",
        matches !== undefined ? plural(matches, "match", "matches") : undefined,
        language,
        mode === "kind" && kind ? `kind ${kind}` : mode,
        version ? `ast-grep ${version}` : undefined,
        metadataBoolean(result, "has_more") ? "more available" : undefined
      ].filter(Boolean).join(" · ");
    }
    case "tree": {
      const entries = count("files_count") ?? count("paths_count") ?? count("count");
      return [target ?? "Workspace tree", entries !== undefined ? plural(entries, "entry", "entries") : undefined].filter(Boolean).join(" · ");
    }
    case "show_changes":
    case "git_diff":
    case "git_status": {
      const files = count("changed_files_count") ?? count("changed_paths_count");
      return [target ?? "Working tree", files !== undefined ? plural(files, "changed file") : undefined, delta].filter(Boolean).join(" · ");
    }
    case "open_workspace": {
      const workspaces = count("workspaces_count") ?? count("count") ?? metadataNumber(request, "project_ids_count");
      const target = metadataString(request, "project_id") ?? (workspaces !== undefined ? plural(workspaces, "workspace") : "Workspace");
      const alreadyOpenCount = metadataNumber(result, "already_open_count");
      const reuse = alreadyOpenCount
        ? `${plural(alreadyOpenCount, "workspace")} reused`
        : metadataBoolean(result, "already_open")
          ? "already open"
          : undefined;
      return [target, reuse, metadataBoolean(request, "include_tree") ? "with tree" : undefined].filter(Boolean).join(" · ");
    }
    case "open_current_workspace":
      return ["Workspace", metadataBoolean(request, "include_tree") ? "with tree" : undefined].filter(Boolean).join(" · ");
    case "inspect_workspace": {
      const files = count("files_count");
      return [target ?? "Workspace inspection", files !== undefined ? plural(files, "file") : undefined].filter(Boolean).join(" · ");
    }
    default: {
      const resultCount = count("count");
      return [target ?? action.operation, resultCount !== undefined ? plural(resultCount, "result") : undefined, delta].filter(Boolean).join(" · ");
    }
  }
}

function pathEvidenceState(value: PathEvidence | undefined): string {
  if (!value || !value.exists) return "missing";
  const kind = value.kind ?? "item";
  return value.size === undefined ? kind : `${kind} · ${humanBytes(value.size)}`;
}

function actionPathEvidence(action: CodexProActionV1, guard: PathGuard): ActivityDashboardEvidence[] {
  const before = new Map((action.path_evidence_before ?? [])
    .filter((item) => isSafeDashboardPath(guard, item.path))
    .map((item) => [normalizeGitPath(item.path), item]));
  const after = new Map((action.path_evidence_after ?? [])
    .filter((item) => isSafeDashboardPath(guard, item.path))
    .map((item) => [normalizeGitPath(item.path), item]));
  const paths = unique([...before.keys(), ...after.keys()]).slice(0, 6);
  return paths.map((item) => ({
    label: redactSensitiveText(item),
    value: `${pathEvidenceState(before.get(item))} → ${pathEvidenceState(after.get(item))}`
  }));
}

function dashboardGitEvidence(value: GitEvidence | undefined): ActivityDashboardGitEvidence | undefined {
  if (!value) return undefined;
  return {
    branch: value.branch ? redactSensitiveText(value.branch) : undefined,
    head: value.head,
    dirty: value.dirty,
    changedPathCount: value.changed_path_count
  };
}

function dashboardBatchReference(
  action: CodexProDashboardActionV1,
  guard: PathGuard,
  fallbackProjectId: string
): { path: string; href: string } | undefined {
  if (action.tool_name !== "batch") return undefined;
  const candidate = metadataString(action.result_metadata, "batch_path")
    ?? metadataString(action.request_metadata, "batch_path");
  if (!candidate || !isSafeDashboardPath(guard, candidate)) return undefined;
  const batchPath = normalizeGitPath(candidate);
  const projectId = action.project_id ?? fallbackProjectId;
  const params = new URLSearchParams({ project_id: projectId, path: batchPath });
  if (action.workspace_id) params.set("workspace_id", action.workspace_id);
  return { path: batchPath, href: `/activity/batch?${params.toString()}` };
}

function dashboardAction(
  action: CodexProDashboardActionV1,
  guard: PathGuard,
  fallbackProjectId: string
): ActivityDashboardAction {
  const safePaths = safeActionPaths(action, guard);
  const batch = dashboardBatchReference(action, guard, fallbackProjectId);
  return {
    actionId: action.action_id,
    sequence: action.sequence,
    finishedAt: action.finished_at,
    toolName: action.tool_name,
    operation: action.operation,
    operationClass: action.operation_class,
    status: action.status,
    durationMs: action.duration_ms,
    mutating: action.mutating,
    headline: actionHeadline(action, safePaths.paths, guard),
    changedPaths: safePaths.paths,
    hiddenPathCount: safePaths.hidden,
    changedPathsTruncated: action.changed_paths_truncated,
    requestFields: metadataFields(action.request_metadata, guard),
    resultFields: metadataFields(action.result_metadata, guard),
    pathEvidence: actionPathEvidence(action, guard),
    gitBefore: dashboardGitEvidence(action.git_before),
    gitAfter: dashboardGitEvidence(action.git_after),
    errorCode: action.error_code,
    batchPath: batch?.path,
    batchHref: batch?.href,
    shellScripts: (action.dashboard_metadata?.shell_scripts ?? []).map((item) => ({
      operationId: item.operation_id,
      script: item.script,
      truncated: item.truncated === true
    }))
  };
}

export function collectActivityDashboard(
  config: CodexProConfig,
  journal = new AuditJournal(config)
): ActivityDashboardSnapshot {
  const audit = journal.status();
  const guard = new PathGuard(config);
  const projects = config.projects.map((project) => {
    const actions = journal.listForDashboard({ projectId: project.id, limit: ACTIONS_PER_PROJECT })
      .actions
      .slice()
      .reverse()
      .map((action) => dashboardAction(action, guard, project.id));
    return {
      id: project.id,
      label: project.label,
      latestActivityAt: actions[0]?.finishedAt,
      actions,
      git: collectProjectGit(config, project, guard)
    } satisfies ActivityDashboardProject;
  });

  projects.sort((left, right) => {
    if (left.latestActivityAt && right.latestActivityAt) return right.latestActivityAt.localeCompare(left.latestActivityAt);
    if (left.latestActivityAt) return -1;
    if (right.latestActivityAt) return 1;
    return left.label.localeCompare(right.label);
  });

  return {
    generatedAt: new Date().toISOString(),
    audit,
    projects
  };
}

function escapeHtml(value: unknown): string {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}


function parseDiffPath(value: string): string {
  const withoutTimestamp = value.trim().split("\t", 1)[0] ?? value.trim();
  const unquoted = withoutTimestamp.startsWith('"') && withoutTimestamp.endsWith('"')
    ? withoutTimestamp.slice(1, -1)
    : withoutTimestamp;
  return unquoted === "/dev/null" ? unquoted : unquoted.replace(/^[ab]\//, "");
}

function parseUnifiedDiff(value: string): SplitDiffFile[] {
  const files: SplitDiffFile[] = [];
  let file: SplitDiffFile | undefined;
  let hunk: SplitDiffHunk | undefined;
  let beforeLine = 0;
  let afterLine = 0;
  let removed: SplitDiffCell[] = [];
  let added: SplitDiffCell[] = [];

  const flushChanges = () => {
    if (!hunk || (!removed.length && !added.length)) return;
    const count = Math.max(removed.length, added.length);
    for (let index = 0; index < count; index += 1) {
      hunk.rows.push({
        kind: "line",
        before: removed[index] ?? { text: "", tone: "empty" },
        after: added[index] ?? { text: "", tone: "empty" }
      });
    }
    removed = [];
    added = [];
  };

  for (const line of value.replace(/\r\n/g, "\n").split("\n")) {
    if (line.startsWith("diff --git ")) {
      flushChanges();
      const paths = /^diff --git a\/(.+) b\/(.+)$/.exec(line);
      file = {
        beforePath: paths?.[1] ?? "Before",
        afterPath: paths?.[2] ?? "After",
        metadata: [],
        hunks: []
      };
      files.push(file);
      hunk = undefined;
      continue;
    }
    if (!file) continue;

    const hunkHeader = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(?:.*)$/.exec(line);
    if (hunkHeader) {
      flushChanges();
      beforeLine = Number(hunkHeader[1]);
      afterLine = Number(hunkHeader[2]);
      hunk = { header: line, rows: [] };
      file.hunks.push(hunk);
      continue;
    }

    if (!hunk && line.startsWith("--- ")) {
      file.beforePath = parseDiffPath(line.slice(4));
      continue;
    }
    if (!hunk && line.startsWith("+++ ")) {
      file.afterPath = parseDiffPath(line.slice(4));
      continue;
    }
    if (!hunk) {
      if (line) file.metadata.push(line);
      continue;
    }

    const prefix = line[0];
    if (prefix === " ") {
      flushChanges();
      const text = line.slice(1);
      hunk.rows.push({
        kind: "line",
        before: { lineNumber: beforeLine, text, tone: "context" },
        after: { lineNumber: afterLine, text, tone: "context" }
      });
      beforeLine += 1;
      afterLine += 1;
    } else if (prefix === "-") {
      removed.push({ lineNumber: beforeLine, text: line.slice(1), tone: "removed" });
      beforeLine += 1;
    } else if (prefix === "+") {
      added.push({ lineNumber: afterLine, text: line.slice(1), tone: "added" });
      afterLine += 1;
    } else {
      flushChanges();
      if (line) hunk.rows.push({ kind: "note", text: line });
    }
  }
  flushChanges();
  return files;
}

function renderSplitDiffCell(cell: SplitDiffCell, side: "before" | "after"): string {
  const lineNumber = cell.lineNumber === undefined ? "" : String(cell.lineNumber);
  return `<span class="diff-line-number ${side} ${cell.tone}">${escapeHtml(lineNumber)}</span><code class="diff-line-code ${side} ${cell.tone}">${escapeHtml(cell.text)}</code>`;
}

function renderSplitDiffRow(row: SplitDiffRow): string {
  if (row.kind === "note") return `<div class="diff-note">${escapeHtml(row.text)}</div>`;
  return `<div class="diff-row">${renderSplitDiffCell(row.before, "before")}${renderSplitDiffCell(row.after, "after")}</div>`;
}

function renderSplitDiffFile(file: SplitDiffFile): string {
  const visiblePath = file.afterPath === "/dev/null" ? file.beforePath : file.afterPath;
  const metadata = file.metadata.map((line) => `<div class="diff-metadata">${escapeHtml(line)}</div>`).join("");
  const hunks = file.hunks.map((item) => `<div class="diff-hunk-header">${escapeHtml(item.header)}</div>${item.rows.map(renderSplitDiffRow).join("")}`).join("");
  const contents = metadata || hunks
    ? `${metadata}${hunks}`
    : `<div class="diff-note">No textual hunk is available for this mode, rename, or binary change.</div>`;
  return `<section class="diff-file">
    <header class="diff-file-heading"><code>${escapeHtml(visiblePath)}</code><span>shared scroll · matched lines</span></header>
    <div class="split-diff-scroll" tabindex="0" aria-label="Side-by-side diff for ${escapeHtml(visiblePath)}">
      <div class="split-diff-grid">
        <div class="diff-side-heading before"><span>Before</span><code>${escapeHtml(file.beforePath)}</code></div>
        <div class="diff-side-heading after"><span>After</span><code>${escapeHtml(file.afterPath)}</code></div>
        ${contents}
      </div>
    </div>
  </section>`;
}

function renderSplitDiff(value: string): string {
  const files = parseUnifiedDiff(value);
  if (!files.length) return `<pre class="diff-fallback">${escapeHtml(value)}</pre>`;
  return `<div class="split-diff">${files.map(renderSplitDiffFile).join("")}</div>`;
}

function statusTone(status: ActivityDashboardAction["status"]): string {
  if (status === "succeeded") return "good";
  if (status === "cancelled" || status === "blocked") return "warn";
  return "bad";
}

function renderFieldSection(title: string, fields: ActivityDashboardField[]): string {
  if (!fields.length) return "";
  return `<section class="action-section">
    <h4>${escapeHtml(title)}</h4>
    <dl class="field-grid">${fields.map((field) => `<div>
      <dt>${escapeHtml(field.label)}</dt>
      <dd class="${field.mono ? "mono-value" : ""} ${field.tone ?? ""}">${escapeHtml(field.value)}</dd>
    </div>`).join("")}</dl>
  </section>`;
}

function renderActionEvidence(title: string, evidence: ActivityDashboardEvidence[]): string {
  if (!evidence.length) return "";
  return `<section class="action-section">
    <h4>${escapeHtml(title)}</h4>
    <dl class="evidence-list">${evidence.map((item) => `<div><dt>${escapeHtml(item.label)}</dt><dd>${escapeHtml(item.value)}</dd></div>`).join("")}</dl>
  </section>`;
}

function gitEvidenceText(value: ActivityDashboardGitEvidence): string {
  const identity = [value.branch, value.head?.slice(0, 12)].filter(Boolean).join(" @ ") || "No Git identity";
  return `${identity} · ${value.dirty ? plural(value.changedPathCount, "changed path") : "clean"}`;
}

function renderActionGit(action: ActivityDashboardAction): string {
  if (!action.gitBefore && !action.gitAfter) return "";
  return `<section class="action-section">
    <h4>Git evidence</h4>
    <div class="git-transition">
      ${action.gitBefore ? `<div><span>Before</span><code>${escapeHtml(gitEvidenceText(action.gitBefore))}</code></div>` : ""}
      ${action.gitAfter ? `<div><span>After</span><code>${escapeHtml(gitEvidenceText(action.gitAfter))}</code></div>` : ""}
    </div>
  </section>`;
}


function renderShellScripts(action: ActivityDashboardAction): string {
  if (!action.shellScripts.length) {
    return action.toolName === "bash"
      ? `<p class="privacy-note"><strong>Shell script unavailable:</strong> this retained action predates exact-command capture.</p>`
      : "";
  }
  return action.shellScripts.map((item, index) => {
    const title = item.operationId
      ? `Shell script · ${item.operationId}`
      : action.shellScripts.length > 1
        ? `Shell script ${index + 1}`
        : "Shell script";
    return `<section class="shell-script"><div class="shell-script-head"><h4>${escapeHtml(title)}</h4><span>${item.truncated ? "truncated at journal limit" : "exact command"}</span></div><pre>${escapeHtml(item.script)}</pre></section>`;
  }).join("");
}

function renderAction(action: ActivityDashboardAction): string {
  const pathNotes = [
    action.hiddenPathCount ? `${plural(action.hiddenPathCount, "blocked path")} hidden` : "",
    action.changedPathsTruncated ? "changed-path list truncated" : ""
  ].filter(Boolean);
  const changedPaths = action.changedPaths.length
    ? `<section class="action-section changed-paths"><h4>Changed paths</h4><div class="path-list">${action.changedPaths.map((item) => `<code class="path tracked">${escapeHtml(item)}</code>`).join("")}</div></section>`
    : "";
  const batchLink = action.batchHref && action.batchPath
    ? `<a class="batch-link" href="${escapeHtml(action.batchHref)}" data-local-link target="_blank" rel="noopener"><span>Open saved batch</span><code>${escapeHtml(action.batchPath)}</code><b aria-hidden="true">↗</b></a>`
    : "";
  const shellScripts = renderShellScripts(action);
  const error = action.errorCode
    ? `<p class="error-note"><strong>Error code:</strong> <code>${escapeHtml(action.errorCode)}</code></p>`
    : "";
  return `<details class="action-card" data-action-id="${escapeHtml(action.actionId)}">
    <summary>
      <div class="action-time"><time datetime="${escapeHtml(action.finishedAt)}" data-local-time>${escapeHtml(action.finishedAt)}</time><span>#${escapeHtml(action.sequence)}</span></div>
      <div class="action-summary-main">
        <div class="action-title"><code>${escapeHtml(action.toolName)}</code><strong>${escapeHtml(action.headline)}</strong></div>
        <div class="action-subtitle"><span>${escapeHtml(action.operation)}</span><span>${escapeHtml(action.operationClass)}</span><span>${escapeHtml(humanDuration(action.durationMs))}</span>${action.mutating ? `<span class="mutating">mutating</span>` : ""}</div>
      </div>
      <span class="status ${statusTone(action.status)}">${escapeHtml(action.status)}</span>
    </summary>
    <div class="action-body">
      ${changedPaths}
      ${batchLink}
      ${pathNotes.length ? `<p class="safety-note">${escapeHtml(pathNotes.join(" · "))}</p>` : ""}
      <div class="action-detail-grid">
        ${renderFieldSection("Request", action.requestFields)}
        ${renderFieldSection("Result", action.resultFields)}
        ${renderActionEvidence("File evidence", action.pathEvidence)}
        ${renderActionGit(action)}
      </div>
      ${shellScripts}
      ${error}
      <div class="action-identity"><span>Action</span><code>${escapeHtml(action.actionId)}</code></div>
    </div>
  </details>`;
}

function renderPathList(title: string, paths: string[], tone: string): string {
  if (!paths.length) return "";
  return `<div class="path-group">
    <strong>${escapeHtml(title)}</strong>
    <div class="path-list">${paths.map((item) => `<code class="path ${tone}">${escapeHtml(item)}</code>`).join("")}</div>
  </div>`;
}

function renderGit(project: ActivityDashboardProject): string {
  const git = project.git;
  if (!git.available) {
    return `<section class="git-panel unavailable"><div><strong>Git</strong><span>${escapeHtml(git.message ?? "Unavailable")}</span></div></section>`;
  }
  if (!git.dirty) {
    return `<section class="git-panel clean">
      <div><strong>Working tree</strong><span>Clean at <code>${escapeHtml(git.head ?? "no commit")}</code></span></div>
      ${git.committedAt ? `<time datetime="${escapeHtml(git.committedAt)}" data-local-time>${escapeHtml(git.committedAt)}</time>` : ""}
    </section>`;
  }

  const visibleCount = git.trackedChangedPaths.length + git.untrackedPaths.length;
  const notes = [
    git.hiddenPathCount ? `${git.hiddenPathCount} safety-blocked path${git.hiddenPathCount === 1 ? "" : "s"} hidden` : "",
    git.omittedPathCount ? `${git.omittedPathCount} additional path${git.omittedPathCount === 1 ? "" : "s"} omitted` : "",
    git.diffTruncated ? "diff output truncated" : ""
  ].filter(Boolean);
  return `<details class="git-details">
    <summary>
      <span><strong>Diff from HEAD</strong><small>${escapeHtml(`${visibleCount} visible path${visibleCount === 1 ? "" : "s"}`)}</small></span>
      <span class="delta"><b>+${git.additions}</b><b>−${git.deletions}</b></span>
    </summary>
    <div class="git-body">
      ${renderPathList("Tracked changes", git.trackedChangedPaths, "tracked")}
      ${renderPathList("Untracked files (contents not rendered)", git.untrackedPaths, "untracked")}
      ${notes.length ? `<p class="safety-note">${escapeHtml(notes.join(" · "))}</p>` : ""}
      ${renderSplitDiff(git.diff || "No tracked diff. The working tree contains only untracked or safety-filtered paths.")}
    </div>
  </details>`;
}

function renderProject(project: ActivityDashboardProject): string {
  const git = project.git;
  const activity = project.actions.length
    ? `<div class="action-list">${project.actions.map(renderAction).join("")}</div>`
    : `<p class="empty">No retained activity for this project.</p>`;
  return `<article class="project-card" data-project="${escapeHtml(project.id)}">
    <header class="project-head">
      <div>
        <span class="project-id">${escapeHtml(project.id)}</span>
        <h2>${escapeHtml(project.label)}</h2>
      </div>
      <div class="project-meta">
        ${git.available ? `<span class="branch">${escapeHtml(git.branch ?? "detached")}</span>` : ""}
        ${project.latestActivityAt ? `<time datetime="${escapeHtml(project.latestActivityAt)}" data-local-time>${escapeHtml(project.latestActivityAt)}</time>` : ""}
      </div>
    </header>
    ${renderGit(project)}
    <section class="activity-block">
      <div class="section-title"><h3>Latest CodexPro actions</h3><span>${escapeHtml(`${project.actions.length} retained`)}</span></div>
      ${activity}
    </section>
  </article>`;
}

export function renderActivityDashboardPage(snapshot: ActivityDashboardSnapshot): string {
  const auditWarning = snapshot.audit.enabled
    ? snapshot.audit.gap_detected || snapshot.audit.malformed_records
      ? `<div class="banner bad">The action journal reports ${escapeHtml(snapshot.audit.malformed_records)} malformed record(s)${snapshot.audit.gap_detected ? " and a sequence gap" : ""}. Activity may be incomplete.</div>`
      : ""
    : `<div class="banner warn">Debug activity is disabled. Git state is available, but recent CodexPro actions require <code>--audit metadata</code>.</div>`;
  const projectCards = snapshot.projects.length
    ? snapshot.projects.map(renderProject).join("")
    : `<div class="banner warn">No runnable projects are configured.</div>`;

  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="icon" href="/favicon.ico">
  <title>CodexPro Activity & Changes</title>
  <style>
    :root {
      color-scheme: light;
      font-family: "Geist", "Aptos", ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      --paper: #f4f6f9;
      --panel: #ffffff;
      --ink: #172033;
      --soft: #5b667a;
      --rule: #dce2eb;
      --accent: #2563eb;
      --good: #137a47;
      --good-bg: #eaf8f0;
      --warn: #9a5a00;
      --warn-bg: #fff5dc;
      --bad: #b42318;
      --bad-bg: #fff0ee;
      --mono: "Fira Code", "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    }
    * { box-sizing: border-box; }
    body { margin: 0; background: var(--paper); color: var(--ink); }
    button, a { font: inherit; }
    main { width: min(1500px, calc(100% - 32px)); margin: 0 auto; padding: 24px 0 56px; }
    .topbar { display: flex; align-items: center; justify-content: space-between; gap: 20px; margin-bottom: 18px; }
    .brand { display: flex; align-items: center; gap: 12px; }
    .brand img { width: 42px; height: 42px; border-radius: 11px; }
    .eyebrow, .project-id { display: block; color: var(--soft); font-size: 12px; font-weight: 750; letter-spacing: .09em; text-transform: uppercase; }
    h1, h2, h3, p { margin-top: 0; }
    h1 { margin-bottom: 4px; font-size: clamp(25px, 4vw, 38px); letter-spacing: -.035em; }
    h2 { margin-bottom: 0; font-size: 21px; letter-spacing: -.02em; }
    h3 { margin-bottom: 0; font-size: 15px; }
    .subtitle { margin: 0; color: var(--soft); }
    .actions { display: flex; align-items: center; gap: 8px; }
    .button { border: 1px solid var(--rule); border-radius: 9px; background: var(--panel); color: var(--ink); padding: 9px 12px; text-decoration: none; cursor: pointer; }
    .button.primary { border-color: var(--accent); background: var(--accent); color: white; }
    .summary { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 10px; margin: 18px 0; }
    .metric { background: var(--panel); border: 1px solid var(--rule); border-radius: 12px; padding: 14px; }
    .metric span { display: block; color: var(--soft); font-size: 12px; margin-bottom: 5px; }
    .metric strong { font-family: var(--mono); font-size: 16px; }
    .banner { margin: 12px 0; border: 1px solid var(--rule); border-radius: 10px; background: var(--panel); padding: 12px 14px; }
    .banner.warn { border-color: #f0cc88; background: var(--warn-bg); color: var(--warn); }
    .banner.bad { border-color: #f0aaa3; background: var(--bad-bg); color: var(--bad); }
    .project-grid { display: grid; gap: 16px; }
    .project-card { overflow: hidden; border: 1px solid var(--rule); border-radius: 14px; background: var(--panel); box-shadow: 0 8px 28px rgba(23, 32, 51, .05); }
    .project-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; padding: 18px 20px 14px; }
    .project-meta { display: flex; align-items: flex-end; flex-direction: column; gap: 6px; color: var(--soft); font-size: 12px; }
    .branch { border: 1px solid var(--rule); border-radius: 999px; padding: 4px 8px; font-family: var(--mono); color: var(--ink); }
    .git-panel { display: flex; justify-content: space-between; gap: 12px; margin: 0 20px 16px; border: 1px solid var(--rule); border-radius: 10px; background: #f8fafc; padding: 11px 12px; color: var(--soft); font-size: 13px; }
    .git-panel div { display: flex; align-items: center; gap: 8px; }
    .git-panel.clean { border-color: #b9e3ca; background: var(--good-bg); color: var(--good); }
    .git-panel.unavailable { opacity: .85; }
    .git-details { margin: 0 20px 16px; border: 1px solid #f0cc88; border-radius: 10px; background: #fffaf0; }
    .git-details summary { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 12px 14px; cursor: pointer; list-style: none; }
    .git-details summary::-webkit-details-marker { display: none; }
    .git-details summary span:first-child { display: flex; align-items: baseline; gap: 8px; }
    .git-details small { color: var(--soft); }
    .delta { display: flex; gap: 9px; font-family: var(--mono); }
    .delta b:first-child { color: var(--good); }
    .delta b:last-child { color: var(--bad); }
    .git-body { border-top: 1px solid #f0cc88; padding: 14px; }
    .path-group { margin-bottom: 11px; }
    .path-group > strong { display: block; margin-bottom: 6px; font-size: 12px; color: var(--soft); }
    .path-list { display: flex; flex-wrap: wrap; gap: 5px; }
    .path { border-radius: 6px; padding: 4px 6px; font-size: 11px; }
    .path.tracked { background: #e9eef7; }
    .path.untracked { background: var(--warn-bg); color: var(--warn); }
    .safety-note { color: var(--soft); font-size: 12px; }
    .diff-fallback, .shell-script pre { max-height: 560px; overflow: auto; margin: 12px 0 0; border-radius: 8px; background: #111827; color: #e5e7eb; padding: 13px; font: 12px/1.55 var(--mono); white-space: pre; tab-size: 2; }
    .shell-script { overflow: hidden; margin-top: 10px; border: 1px solid #25324a; border-radius: 8px; background: #111827; }
    .shell-script-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; background: #172033; padding: 8px 11px; color: #d9e2f2; }
    .shell-script-head h4 { margin: 0; font-size: 10px; letter-spacing: .07em; text-transform: uppercase; }
    .shell-script-head span { color: #9fb0ca; font-size: 10px; }
    .shell-script pre { margin: 0; border-radius: 0; background: transparent; }
    .split-diff { display: grid; gap: 12px; margin-top: 12px; }
    .diff-file { overflow: hidden; border: 1px solid var(--rule); border-radius: 9px; background: var(--panel); }
    .diff-file-heading { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 9px 11px; background: #f3f6fa; }
    .diff-file-heading code { overflow-wrap: anywhere; font-size: 12px; font-weight: 750; }
    .diff-file-heading span { flex: 0 0 auto; color: var(--soft); font-size: 10px; text-transform: uppercase; }
    .split-diff-scroll { max-height: 620px; overflow: auto; border-top: 1px solid var(--rule); overscroll-behavior: contain; }
    .split-diff-grid { display: grid; grid-template-columns: 58px minmax(440px, 1fr) 58px minmax(440px, 1fr); min-width: 1040px; font: 12px/1.55 var(--mono); }
    .diff-side-heading { position: sticky; top: 0; z-index: 3; display: flex; align-items: baseline; gap: 9px; border-bottom: 1px solid #cfd7e4; background: #edf1f7; padding: 7px 9px; }
    .diff-side-heading.before { grid-column: 1 / 3; border-right: 1px solid #c7d0df; }
    .diff-side-heading.after { grid-column: 3 / 5; }
    .diff-side-heading span { color: var(--soft); font: 700 9px/1 var(--mono); letter-spacing: .06em; text-transform: uppercase; }
    .diff-side-heading code { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .diff-metadata, .diff-hunk-header, .diff-note { grid-column: 1 / -1; white-space: pre; }
    .diff-metadata { background: #f8fafc; padding: 3px 10px; color: var(--soft); }
    .diff-hunk-header { border-top: 1px solid #d7dfeb; border-bottom: 1px solid #d7dfeb; background: #eef4ff; padding: 4px 10px; color: #38517d; }
    .diff-note { background: #fff8e8; padding: 4px 10px; color: var(--warn); }
    .diff-row { display: contents; }
    .diff-line-number, .diff-line-code { min-height: 23px; border-top: 1px solid #edf0f5; }
    .diff-line-number { padding: 2px 8px 2px 4px; color: #8791a3; text-align: right; user-select: none; }
    .diff-line-code { padding: 2px 9px; white-space: pre; tab-size: 2; }
    .diff-line-code.before { border-right: 1px solid #cfd7e4; }
    .diff-line-number.removed, .diff-line-code.removed { background: #fff0ee; }
    .diff-line-number.added, .diff-line-code.added { background: #eaf8f0; }
    .diff-line-number.empty, .diff-line-code.empty { background: #f7f9fc; }
    .diff-line-code.empty { color: transparent; }
    .activity-block { border-top: 1px solid var(--rule); padding: 14px 20px 20px; }
    .section-title { display: flex; justify-content: space-between; align-items: baseline; gap: 10px; margin-bottom: 10px; }
    .section-title span { color: var(--soft); font-size: 12px; }
    code { font-family: var(--mono); }
    .action-list { display: grid; gap: 8px; }
    .action-card { overflow: hidden; border: 1px solid #e4e8ef; border-radius: 10px; background: #fbfcfe; }
    .action-card[open] { border-color: #b9c9e8; background: var(--panel); box-shadow: 0 8px 22px rgba(23, 32, 51, .06); }
    .action-card > summary { display: grid; grid-template-columns: minmax(155px, 190px) minmax(0, 1fr) auto 16px; align-items: center; gap: 12px; padding: 11px 12px; cursor: pointer; list-style: none; }
    .action-card > summary::-webkit-details-marker { display: none; }
    .action-card > summary::after { content: "›"; color: var(--soft); font-size: 22px; line-height: 1; transition: transform 120ms ease; }
    .action-card[open] > summary::after { transform: rotate(90deg); }
    .action-card[open] > summary { border-bottom: 1px solid var(--rule); background: #f8faff; }
    .action-time { display: flex; flex-direction: column; gap: 3px; color: var(--soft); font-size: 12px; }
    .action-time span { font-family: var(--mono); font-size: 10px; color: #8a94a6; }
    .action-summary-main { min-width: 0; }
    .action-title { display: flex; align-items: baseline; gap: 9px; min-width: 0; }
    .action-title code { flex: 0 0 auto; border-radius: 5px; background: #e9eef7; padding: 3px 6px; color: #273754; font-size: 11px; font-weight: 750; }
    .action-title strong { min-width: 0; overflow: hidden; font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
    .action-subtitle { display: flex; flex-wrap: wrap; gap: 5px 10px; margin-top: 5px; color: var(--soft); font-size: 10px; }
    .action-subtitle span:not(.mutating) + span:not(.mutating)::before { content: "·"; margin-right: 10px; color: #a2aaba; }
    .mutating { border-radius: 999px; background: #edf1f7; padding: 1px 5px; color: #59677e; font-weight: 700; }
    .status { display: inline-block; border-radius: 999px; padding: 3px 7px; font-size: 11px; font-weight: 750; white-space: nowrap; }
    .status.good { background: var(--good-bg); color: var(--good); }
    .status.warn { background: var(--warn-bg); color: var(--warn); }
    .status.bad { background: var(--bad-bg); color: var(--bad); }
    .action-body { padding: 13px 14px 14px; }
    .action-detail-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
    .action-section { min-width: 0; border: 1px solid #e7ebf1; border-radius: 8px; background: #fcfdff; padding: 10px; }
    .action-section h4 { margin: 0 0 8px; color: var(--soft); font-size: 10px; letter-spacing: .07em; text-transform: uppercase; }
    .changed-paths { margin-bottom: 10px; }
    .field-grid, .evidence-list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 7px; margin: 0; }
    .field-grid > div, .evidence-list > div { min-width: 0; border-radius: 6px; background: #f3f6fa; padding: 7px 8px; }
    .field-grid dt, .evidence-list dt { margin-bottom: 3px; color: var(--soft); font-size: 9px; letter-spacing: .035em; text-transform: uppercase; }
    .field-grid dd, .evidence-list dd { overflow-wrap: anywhere; margin: 0; font-size: 12px; }
    .mono-value, .evidence-list dt, .git-transition code, .action-identity code { font-family: var(--mono); }
    .field-grid dd.positive { color: var(--good); font-weight: 750; }
    .field-grid dd.negative { color: var(--bad); font-weight: 750; }
    .field-grid dd.muted { color: var(--soft); }
    .git-transition { display: grid; gap: 7px; }
    .git-transition div { display: grid; grid-template-columns: 52px minmax(0, 1fr); align-items: baseline; gap: 8px; }
    .git-transition span { color: var(--soft); font-size: 10px; text-transform: uppercase; }
    .git-transition code { overflow-wrap: anywhere; font-size: 11px; }
    .privacy-note, .error-note { margin: 10px 0 0; border-radius: 7px; padding: 8px 10px; font-size: 11px; }
    .privacy-note { background: #eef4ff; color: #38517d; }
    .error-note { background: var(--bad-bg); color: var(--bad); }
    .action-identity { display: flex; gap: 8px; margin-top: 10px; color: var(--soft); font-size: 10px; }
    .action-identity code { overflow-wrap: anywhere; color: #667085; }

    .batch-link { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 9px; margin-bottom: 10px; border: 1px solid #bfd0ef; border-radius: 8px; background: #eef4ff; padding: 9px 10px; color: #294f91; text-decoration: none; }
    .batch-link:hover { border-color: #7fa3e3; background: #e5efff; }
    .batch-link span { font-size: 11px; font-weight: 750; }
    .batch-link code { overflow: hidden; color: #38517d; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
    .batch-link b { font-size: 12px; }
    .empty { margin: 12px 0 0; color: var(--soft); }
    .foot { margin-top: 20px; color: var(--soft); font-size: 12px; text-align: center; }
    @media (max-width: 820px) {
      main { width: min(100% - 20px, 1500px); padding-top: 14px; }
      .topbar, .project-head { align-items: stretch; flex-direction: column; }
      .project-meta { align-items: flex-start; }
      .summary { grid-template-columns: repeat(2, minmax(0, 1fr)); }
      .actions { flex-wrap: wrap; }
      .project-head, .activity-block { padding-left: 14px; padding-right: 14px; }
      .git-panel, .git-details { margin-left: 14px; margin-right: 14px; }
      .action-card > summary { grid-template-columns: minmax(0, 1fr) auto 16px; gap: 8px; }
      .action-time { grid-column: 1 / -1; flex-direction: row; justify-content: space-between; }
      .action-summary-main { grid-column: 1; }
      .action-card > summary > .status { grid-column: 2; }
      .action-detail-grid, .field-grid, .evidence-list { grid-template-columns: 1fr; }
      .action-title { align-items: flex-start; flex-direction: column; gap: 5px; }
      .action-title strong { white-space: normal; }
    }
  </style>
</head>
<body>
  <main>
    <header class="topbar">
      <div class="brand">
        <img src="/favicon.ico" alt="">
        <div><span class="eyebrow">Authenticated local control</span><h1>Activity & changes</h1><p class="subtitle">Recent CodexPro tool calls and each configured checkout’s working-tree diff.</p></div>
      </div>
      <div class="actions">
        <a class="button" href="/setup" data-local-link>Setup</a>
        <button class="button primary" type="button" data-refresh>Refresh</button>
      </div>
    </header>
    <section class="summary" aria-label="Activity status">
      <div class="metric"><span>Projects</span><strong>${escapeHtml(snapshot.projects.length)}</strong></div>
      <div class="metric"><span>Retained actions</span><strong>${escapeHtml(snapshot.audit.action_count)}</strong></div>
      <div class="metric"><span>Latest sequence</span><strong>${escapeHtml(snapshot.audit.latest_sequence)}</strong></div>
      <div class="metric"><span>Updated</span><strong><time datetime="${escapeHtml(snapshot.generatedAt)}" data-local-time>${escapeHtml(snapshot.generatedAt)}</time></strong></div>
    </section>
    ${auditWarning}
    <section class="project-grid">${projectCards}</section>
    <footer class="foot">Auto-refreshes every 15 seconds while no panel is open. Exact Bash scripts and safety-filtered tracked diffs are rendered; blocked paths and untracked file contents remain hidden.</footer>
  </main>
  <script>
    const authStorageName = "codexpro.activity.credential";
    const initialUrl = new URL(window.location.href);
    const queryCredential = initialUrl.searchParams.get("codexpro_token") || initialUrl.searchParams.get("token") || "";
    if (queryCredential) sessionStorage.setItem(authStorageName, queryCredential);
    const connectorCredential = queryCredential || sessionStorage.getItem(authStorageName) || "";
    if (queryCredential) {
      initialUrl.searchParams.delete("codexpro_token");
      initialUrl.searchParams.delete("token");
      const clean = initialUrl.searchParams.toString();
      history.replaceState(null, "", initialUrl.pathname + (clean ? "?" + clean : "") + initialUrl.hash);
    }
    function authenticatedLocalUrl(pathname) {
      const target = new URL(pathname, window.location.origin);
      if (connectorCredential) target.searchParams.set("codexpro_token", connectorCredential);
      return target.pathname + target.search;
    }
    document.querySelectorAll("[data-local-link]").forEach((link) => {
      link.setAttribute("href", authenticatedLocalUrl(link.getAttribute("href") || "/"));
    });
    document.querySelectorAll("[data-local-time]").forEach((element) => {
      const value = element.getAttribute("datetime");
      const parsed = value ? new Date(value) : null;
      if (!parsed || Number.isNaN(parsed.getTime())) return;
      element.textContent = parsed.toLocaleString();
      element.setAttribute("title", value);
    });
    document.querySelector("[data-refresh]")?.addEventListener("click", () => {
      window.location.assign(authenticatedLocalUrl("/activity"));
    });
    window.setInterval(() => {
      if (document.hidden || document.querySelector("details[open]")) return;
      window.location.assign(authenticatedLocalUrl("/activity"));
    }, 15_000);
  </script>
</body>
</html>`;
}


function batchOperationCards(definition: unknown): string {
  const root = definition && typeof definition === "object" && !Array.isArray(definition)
    ? definition as Record<string, unknown>
    : {};
  const operations = Array.isArray(root.operations) ? root.operations : [];
  if (!operations.length) return `<p class="empty">This JSON has no operation list.</p>`;
  return operations.map((raw, index) => {
    const operation = raw && typeof raw === "object" && !Array.isArray(raw)
      ? raw as Record<string, unknown>
      : {};
    const id = typeof operation.id === "string" && operation.id ? operation.id : `op_${index + 1}`;
    const tool = typeof operation.tool === "string" && operation.tool ? operation.tool : "unknown";
    const args = operation.args && typeof operation.args === "object" && !Array.isArray(operation.args)
      ? operation.args
      : {};
    return `<details class="batch-operation" open>
      <summary><span><b>${escapeHtml(index)}</b><code>${escapeHtml(id)}</code></span><strong>${escapeHtml(tool)}</strong></summary>
      <pre>${escapeHtml(JSON.stringify(args, null, 2) ?? "{}")}</pre>
    </details>`;
  }).join("");
}

export function renderActivityBatchPage(view: ActivityBatchView): string {
  const root = view.definition && typeof view.definition === "object" && !Array.isArray(view.definition)
    ? view.definition as Record<string, unknown>
    : {};
  const operationCount = Array.isArray(root.operations) ? root.operations.length : 0;
  const mode = typeof root.mode === "string" ? root.mode : "unknown";
  const continueOnError = root.continue_on_error === true;
  const rawDefinition = JSON.stringify(view.definition, null, 2) ?? "null";
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="icon" href="/favicon.ico">
  <title>${escapeHtml(view.path)} · CodexPro Batch</title>
  <style>
    :root { color-scheme: light; font-family: "Geist", "Aptos", ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; --paper: #f4f6f9; --panel: #fff; --ink: #172033; --soft: #5b667a; --rule: #dce2eb; --accent: #2563eb; --mono: "Fira Code", "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; }
    * { box-sizing: border-box; }
    body { margin: 0; background: var(--paper); color: var(--ink); }
    main { width: min(1100px, calc(100% - 28px)); margin: 0 auto; padding: 24px 0 48px; }
    a { color: inherit; }
    .back { display: inline-flex; align-items: center; gap: 7px; margin-bottom: 16px; color: var(--accent); font-size: 13px; font-weight: 700; text-decoration: none; }
    .head, .panel { border: 1px solid var(--rule); border-radius: 14px; background: var(--panel); box-shadow: 0 8px 28px rgba(23,32,51,.05); }
    .head { padding: 20px; }
    .eyebrow { color: var(--soft); font-size: 11px; font-weight: 750; letter-spacing: .08em; text-transform: uppercase; }
    h1 { overflow-wrap: anywhere; margin: 5px 0 7px; font: 700 clamp(22px, 4vw, 34px)/1.15 var(--mono); letter-spacing: -.025em; }
    .project { margin: 0; color: var(--soft); }
    .badges { display: flex; flex-wrap: wrap; gap: 7px; margin-top: 15px; }
    .badge { border-radius: 999px; background: #eef2f7; padding: 5px 9px; font: 11px var(--mono); }
    .note { margin: 12px 0 0; border-radius: 8px; background: #fff7e5; padding: 9px 11px; color: #895000; font-size: 12px; }
    .panel { margin-top: 14px; padding: 16px; }
    .panel h2 { margin: 0 0 12px; font-size: 15px; }
    .operation-list { display: grid; gap: 8px; }
    .batch-operation { overflow: hidden; border: 1px solid #e4e8ef; border-radius: 9px; background: #fbfcfe; }
    .batch-operation summary { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 12px; cursor: pointer; }
    .batch-operation summary span { display: flex; align-items: center; gap: 9px; min-width: 0; }
    .batch-operation summary b { display: inline-grid; min-width: 24px; height: 24px; place-items: center; border-radius: 6px; background: #e9eef7; color: #59677e; font: 10px var(--mono); }
    .batch-operation summary code, .batch-operation summary strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .batch-operation summary strong { color: #38517d; font: 700 11px var(--mono); }
    pre { overflow: auto; max-height: 580px; margin: 0; border-top: 1px solid #e4e8ef; background: #111827; padding: 13px; color: #e5e7eb; font: 12px/1.55 var(--mono); white-space: pre; tab-size: 2; }
    .raw pre { border: 0; border-radius: 9px; }
    .empty { margin: 0; color: var(--soft); }
  </style>
</head>
<body>
  <main>
    <a class="back" href="/activity" data-local-link>← Activity & changes</a>
    <header class="head">
      <span class="eyebrow">Saved CodexPro batch</span>
      <h1>${escapeHtml(view.path)}</h1>
      <p class="project">${escapeHtml(view.projectLabel)} <code>${escapeHtml(view.projectId)}</code>${view.workspaceId ? ` · workspace <code>${escapeHtml(view.workspaceId)}</code>` : ""}</p>
      <div class="badges"><span class="badge">${escapeHtml(mode)}</span><span class="badge">${escapeHtml(operationCount)} operation${operationCount === 1 ? "" : "s"}</span><span class="badge">continue on error: ${continueOnError ? "yes" : "no"}</span><span class="badge">${view.autoStored ? "auto-stored" : "custom JSON"}</span></div>
      <p class="note">This is the current saved definition. If the batch was edited after the action ran, it may differ from the historical invocation. Exact commands and arguments can contain credentials or other sensitive values; treat this authenticated page as sensitive.</p>
    </header>
    <section class="panel"><h2>Operations</h2><div class="operation-list">${batchOperationCards(view.definition)}</div></section>
    <details class="panel raw"><summary><strong>Raw JSON</strong></summary><pre>${escapeHtml(rawDefinition)}</pre></details>
  </main>
  <script>
    const authStorageName = "codexpro.activity.credential";
    const initialUrl = new URL(window.location.href);
    const queryCredential = initialUrl.searchParams.get("codexpro_token") || initialUrl.searchParams.get("token") || "";
    if (queryCredential) sessionStorage.setItem(authStorageName, queryCredential);
    const connectorCredential = queryCredential || sessionStorage.getItem(authStorageName) || "";
    if (queryCredential) {
      initialUrl.searchParams.delete("codexpro_token");
      initialUrl.searchParams.delete("token");
      const clean = initialUrl.searchParams.toString();
      history.replaceState(null, "", initialUrl.pathname + (clean ? "?" + clean : "") + initialUrl.hash);
    }
    document.querySelectorAll("[data-local-link]").forEach((link) => {
      const target = new URL(link.getAttribute("href") || "/", window.location.origin);
      if (connectorCredential) target.searchParams.set("codexpro_token", connectorCredential);
      link.setAttribute("href", target.pathname + target.search);
    });
  </script>
</body>
</html>`;
}
