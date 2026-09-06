import {
  AuditJournal,
  type CodexProActionV1,
  type CodexProDashboardActionV1,
  type GitEvidence,
  type PathEvidence
} from "../audit.js";
import type { CodexProConfig } from "../config.js";
import { PathGuard } from "../guard.js";
import { redactSensitiveText } from "../redact.js";
import { humanBytes, humanDuration, isSafeDashboardPath, normalizeGitPath, plural, unique } from "./format.js";
import { collectProjectGit } from "./git.js";
import { buildTimeline, timelineBinLabel, UNATTRIBUTED_LABEL, UNKNOWN_LABEL } from "./timeline.js";
import type {
  ActionAttribution,
  ActivityDashboardAction,
  ActivityDashboardEvidence,
  ActivityDashboardField,
  ActivityDashboardGitEvidence,
  ActivityDashboardProject,
  ActivityDashboardSnapshot
} from "./types.js";

// Compact per-project list; the global recent table carries the detail.
const ACTIONS_PER_PROJECT = 5;
const RECENT_ACTION_LIMIT = 30;
// The timeline shows the newest actions, but never more than this window so bins stay readable.
const TIMELINE_ACTION_LIMIT = 250;
const TIMELINE_WINDOW_MS = 14 * 86_400_000;

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
  fallbackProjectId?: string
): { path: string; href: string } | undefined {
  if (action.tool_name !== "batch") return undefined;
  const candidate = metadataString(action.result_metadata, "batch_path")
    ?? metadataString(action.request_metadata, "batch_path");
  if (!candidate || !isSafeDashboardPath(guard, candidate)) return undefined;
  const projectId = action.project_id ?? fallbackProjectId;
  if (!projectId) return undefined;
  const batchPath = normalizeGitPath(candidate);
  const params = new URLSearchParams({ project_id: projectId, path: batchPath });
  if (action.workspace_id) params.set("workspace_id", action.workspace_id);
  return { path: batchPath, href: `/activity/batch?${params.toString()}` };
}

interface ResolvedAttribution {
  projectId?: string;
  projectLabel: string;
  attribution: ActionAttribution;
}

function dashboardAction(
  action: CodexProDashboardActionV1,
  guard: PathGuard,
  resolved: ResolvedAttribution
): ActivityDashboardAction {
  const safePaths = safeActionPaths(action, guard);
  const batch = dashboardBatchReference(action, guard, resolved.projectId);
  return {
    actionId: action.action_id,
    sequence: action.sequence,
    finishedAt: action.finished_at,
    projectId: resolved.projectId,
    projectLabel: resolved.projectLabel,
    workspaceId: action.workspace_id,
    attribution: resolved.attribution,
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

/**
 * Best-effort attribution for records journaled before project ids were
 * recorded on non-mutating calls: a workspace id seen with exactly one
 * project id elsewhere in the retained journal is assumed to belong to it.
 */
function workspaceProjectMap(retained: CodexProDashboardActionV1[]): Map<string, string> {
  const workspaceProjects = new Map<string, string>();
  const ambiguous = new Set<string>();
  for (const action of retained) {
    if (!action.workspace_id || !action.project_id || ambiguous.has(action.workspace_id)) continue;
    const previous = workspaceProjects.get(action.workspace_id);
    if (previous && previous !== action.project_id) {
      workspaceProjects.delete(action.workspace_id);
      ambiguous.add(action.workspace_id);
    } else {
      workspaceProjects.set(action.workspace_id, action.project_id);
    }
  }
  return workspaceProjects;
}

function resolveAttribution(
  action: CodexProDashboardActionV1,
  projectLabels: Map<string, string>,
  workspaceProjects: Map<string, string>
): ResolvedAttribution {
  if (action.project_id) {
    const label = projectLabels.get(action.project_id);
    return label
      ? { projectId: action.project_id, projectLabel: label, attribution: "recorded" }
      : { projectId: action.project_id, projectLabel: UNKNOWN_LABEL, attribution: "unknown" };
  }
  const recovered = action.workspace_id ? workspaceProjects.get(action.workspace_id) : undefined;
  if (recovered) {
    return { projectId: recovered, projectLabel: projectLabels.get(recovered) ?? UNKNOWN_LABEL, attribution: "recovered" };
  }
  return { projectLabel: UNATTRIBUTED_LABEL, attribution: "unattributed" };
}

export function collectActivityDashboard(
  config: CodexProConfig,
  journal = new AuditJournal(config),
  nowMs = Date.now()
): ActivityDashboardSnapshot {
  const generatedAt = new Date(nowMs).toISOString();
  const audit = journal.status();
  const guard = new PathGuard(config);
  const projectLabels = new Map(config.projects.map((project) => [project.id, project.label]));
  const retained = journal.listForDashboard().actions;
  const workspaceProjects = workspaceProjectMap(retained);

  const allActions = retained
    .slice()
    .reverse()
    .map((action) => dashboardAction(action, guard, resolveAttribution(action, projectLabels, workspaceProjects)));

  const projects = config.projects.map((project) => {
    const actions = allActions.filter((action) => action.projectId === project.id).slice(0, ACTIONS_PER_PROJECT);
    return {
      id: project.id,
      label: project.label,
      latestActivityAt: actions[0]?.finishedAt,
      actions,
      git: collectProjectGit(config, project, guard, { cache: true })
    } satisfies ActivityDashboardProject;
  });

  projects.sort((left, right) => {
    if (left.latestActivityAt && right.latestActivityAt) return right.latestActivityAt.localeCompare(left.latestActivityAt);
    if (left.latestActivityAt) return -1;
    if (right.latestActivityAt) return 1;
    return left.label.localeCompare(right.label);
  });

  const timelineActions = allActions
    .slice(0, TIMELINE_ACTION_LIMIT)
    .filter((action) => nowMs - Date.parse(action.finishedAt) <= TIMELINE_WINDOW_MS);
  const timeline = buildTimeline(timelineActions, nowMs);
  const timelineNote = timeline
    ? `Last ${timeline.actionCount} actions · ${timeline.lanes.length} lanes · ${timelineBinLabel(timeline.binMs)} per cell`
    : "No retained actions";

  return {
    generatedAt,
    audit,
    projects,
    recentActions: allActions.slice(0, RECENT_ACTION_LIMIT),
    timeline,
    timelineNote
  };
}
