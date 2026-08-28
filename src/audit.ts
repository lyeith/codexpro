import { createHash, randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import type { CodexProConfig } from "./config.js";
import type { Workspace } from "./guard.js";
import type { ToolCallContext } from "./toolContext.js";

export const ACTION_SCHEMA_VERSION = "1.0" as const;
export const ACTION_STATUSES = ["succeeded", "failed", "timed_out", "cancelled", "blocked"] as const;
export const ACTION_OPERATION_CLASSES = ["read", "write", "execute", "git", "lifecycle", "analysis", "handoff", "administrative"] as const;

export type ActionStatus = (typeof ACTION_STATUSES)[number];
export type ActionOperationClass = (typeof ACTION_OPERATION_CLASSES)[number];

export interface GitPathState {
  path: string;
  status: string;
  state_fingerprint: string;
}

export interface GitEvidence {
  available: true;
  head?: string;
  branch?: string;
  dirty: boolean;
  changed_path_count: number;
  changed_paths: string[];
  changed_paths_truncated: boolean;
  path_states: GitPathState[];
  status_fingerprint: string;
}

export interface PathEvidence {
  path: string;
  exists: boolean;
  kind?: "file" | "directory" | "symlink" | "other";
  size?: number;
  mtime_ms?: number;
}

export interface ActionEvidenceSnapshot {
  project_id?: string;
  workspace_id?: string;
  targets: string[];
  git?: GitEvidence;
  paths: PathEvidence[];
}

export interface ActionRecordInput {
  toolName: string;
  invocationSurface?: "direct" | "codexpro";
  args: unknown;
  result?: unknown;
  error?: unknown;
  startedAtMs: number;
  finishedAtMs: number;
  mutating: boolean;
  context?: ToolCallContext;
  before?: ActionEvidenceSnapshot;
  after?: ActionEvidenceSnapshot;
}

export interface CodexProActionV1 {
  schema_version: typeof ACTION_SCHEMA_VERSION;
  sequence: number;
  action_id: string;
  occurred_at: string;
  finished_at: string;
  project_id?: string;
  workspace_id?: string;
  tool_name: string;
  operation: string;
  operation_class: ActionOperationClass;
  mutating: boolean;
  invocation_surface: "direct" | "codexpro";
  actor_ref: string;
  request_ref: string;
  transport_session_ref?: string;
  server_session_ref: string;
  request_fingerprint?: string;
  status: ActionStatus;
  duration_ms: number;
  targets: string[];
  changed_paths: string[];
  changed_path_count: number;
  changed_paths_truncated: boolean;
  git_before?: GitEvidence;
  git_after?: GitEvidence;
  path_evidence_before?: PathEvidence[];
  path_evidence_after?: PathEvidence[];
  request_metadata: Record<string, unknown>;
  result_metadata: Record<string, unknown>;
  result_ref: string;
  error_code?: string;
  summary: string;
}

export interface ActionListOptions {
  afterSequence?: number;
  limit?: number;
  mutatingOnly?: boolean;
  toolName?: string;
  operationClass?: ActionOperationClass;
  status?: ActionStatus;
  projectId?: string;
  workspaceId?: string;
}

export interface ActionListResult {
  enabled: boolean;
  mode: CodexProConfig["auditMode"];
  schema_version: typeof ACTION_SCHEMA_VERSION;
  actions: CodexProActionV1[];
  next_sequence: number;
  earliest_sequence: number;
  latest_sequence: number;
  has_more: boolean;
  malformed_records: number;
  gap_detected: boolean;
}

export interface ActionStatusResult {
  enabled: boolean;
  mode: CodexProConfig["auditMode"];
  schema_version: typeof ACTION_SCHEMA_VERSION;
  storage_format: "jsonl";
  journal_ref: "codexpro://actions";
  retained_from_sequence: number;
  latest_sequence: number;
  next_sequence: number;
  action_count: number;
  malformed_records: number;
  gap_detected: boolean;
  storage_bytes: number;
  deduplicated_requests: number;
  retention: {
    max_bytes: number;
    retain_actions: number;
    rotation_count: number;
    dropped_through_sequence: number;
    compacted_at?: string;
  };
}

export interface ActionRecordResult {
  enabled: boolean;
  recorded: boolean;
  duplicate: boolean;
  status: ActionStatus;
  action_id?: string;
  sequence?: number;
}

interface ToolDescriptor {
  operation: string;
  operationClass: ActionOperationClass;
  mutating: boolean;
}

interface IndexedAction {
  sequence: number;
  actionId: string;
  requestFingerprint?: string;
  start: number;
  end: number;
}

interface OutcomeClassification {
  status: ActionStatus;
  errorCode?: string;
}

interface AuditJournalIndexV1 {
  schema_version: typeof ACTION_SCHEMA_VERSION;
  journal_ref: typeof JOURNAL_REF;
  rotation_count: number;
  retained_from_sequence: number;
  dropped_through_sequence: number;
  latest_sequence: number;
  updated_at: string;
  compacted_at?: string;
}

const MAX_EVENT_BYTES = 131_072;
const MAX_LIST_LIMIT = 500;
const DEFAULT_LIST_LIMIT = 100;
const MAX_PATH_CHARS = 320;
const MAX_PATHS = 24;
const MAX_TARGETS = 40;
const JOURNAL_READ_CHUNK_BYTES = 64 * 1024;
const LOCK_WAIT_MS = 5_000;
const LOCK_STALE_MS = 30_000;
const LOCK_POLL_MS = 10;
const JOURNAL_REF = "codexpro://actions" as const;
const LOCK_SLEEP_ARRAY = new Int32Array(new SharedArrayBuffer(4));

const TOOL_DESCRIPTORS = new Map<string, ToolDescriptor>([
  ["server_config", { operation: "server.config", operationClass: "administrative", mutating: false }],
  ["list_projects", { operation: "project.list", operationClass: "lifecycle", mutating: false }],
  ["create_project", { operation: "project.create", operationClass: "lifecycle", mutating: true }],
  ["codexpro_self_test", { operation: "server.self_test", operationClass: "administrative", mutating: true }],
  ["codexpro_inventory", { operation: "capability.inventory", operationClass: "analysis", mutating: false }],
  ["load_skill", { operation: "capability.read", operationClass: "read", mutating: false }],
  ["list_workspaces", { operation: "workspace.list", operationClass: "lifecycle", mutating: false }],
  ["create_workspace", { operation: "workspace.create", operationClass: "lifecycle", mutating: true }],
  ["release_workspace", { operation: "workspace.release", operationClass: "lifecycle", mutating: true }],
  ["remove_workspace", { operation: "workspace.remove", operationClass: "lifecycle", mutating: true }],
  ["open_current_workspace", { operation: "workspace.open", operationClass: "lifecycle", mutating: false }],
  ["open_workspace", { operation: "workspace.open", operationClass: "lifecycle", mutating: false }],
  ["workspace_snapshot", { operation: "workspace.inspect", operationClass: "analysis", mutating: false }],
  ["inspect_workspace", { operation: "workspace.inspect", operationClass: "analysis", mutating: false }],
  ["tree", { operation: "file.list", operationClass: "read", mutating: false }],
  ["search", { operation: "file.search", operationClass: "read", mutating: false }],
  ["read", { operation: "file.read", operationClass: "read", mutating: false }],
  ["view_image", { operation: "file.image", operationClass: "read", mutating: false }],
  ["write", { operation: "file.write", operationClass: "write", mutating: true }],
  ["edit", { operation: "file.edit", operationClass: "write", mutating: true }],
  ["apply_patch", { operation: "file.patch", operationClass: "write", mutating: true }],
  ["import_file", { operation: "file.import", operationClass: "write", mutating: true }],
  ["bash", { operation: "command.run", operationClass: "execute", mutating: true }],
  ["git_status", { operation: "git.status", operationClass: "git", mutating: false }],
  ["git_diff", { operation: "git.diff", operationClass: "git", mutating: false }],
  ["show_changes", { operation: "git.review", operationClass: "git", mutating: false }],
  ["read_handoff", { operation: "handoff.read", operationClass: "handoff", mutating: false }],
  ["wait_for_handoff", { operation: "handoff.wait", operationClass: "handoff", mutating: false }],
  ["codex_context", { operation: "context.read", operationClass: "analysis", mutating: false }],
  ["export_pro_context", { operation: "context.export", operationClass: "write", mutating: true }],
  ["handoff_to_agent", { operation: "handoff.write", operationClass: "handoff", mutating: true }],
  ["handoff_to_codex", { operation: "handoff.write", operationClass: "handoff", mutating: true }],
  ["codex_sessions", { operation: "codex.session.list", operationClass: "read", mutating: false }],
  ["read_codex_session", { operation: "codex.session.read", operationClass: "read", mutating: false }],
  ["codexpro.list_actions", { operation: "server.actions", operationClass: "administrative", mutating: false }]
]);

function digest(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

function opaqueRef(prefix: string, value: string | undefined): string {
  return `${prefix}_${digest(value || "unknown").slice(0, 32)}`;
}

function utf8Bytes(value: unknown): number | undefined {
  return typeof value === "string" ? Buffer.byteLength(value, "utf8") : undefined;
}

function boundedString(value: unknown, max = 240): string | undefined {
  if (typeof value !== "string") return undefined;
  const clean = value.replace(/[\0-\x1f\x7f]/g, " ").trim();
  if (!clean) return undefined;
  return clean.slice(0, max);
}

function safeIdentifier(value: unknown): string | undefined {
  const clean = boundedString(value, 160);
  return clean && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$/.test(clean) ? clean : undefined;
}

function normalizedRelativePath(value: unknown): string | undefined {
  const clean = boundedString(value, 4_096);
  if (!clean || path.isAbsolute(clean) || path.win32.isAbsolute(clean)) return undefined;
  const normalized = path.posix.normalize(clean.replaceAll("\\", "/"));
  if (normalized === ".." || normalized.startsWith("../") || normalized.includes("/../")) return undefined;
  return normalized;
}

function safeRelativePath(value: unknown): string | undefined {
  const normalized = normalizedRelativePath(value);
  if (!normalized) return undefined;
  if (normalized.length <= MAX_PATH_CHARS) return normalized;
  return `${normalized.slice(0, MAX_PATH_CHARS - 17)}~${digest(normalized).slice(0, 16)}`;
}

function boolValue(value: unknown): boolean | undefined {
  return typeof value === "boolean" ? value : undefined;
}

function numberValue(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function objectValue(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {};
}

function assignDefined(target: Record<string, unknown>, values: Record<string, unknown>): void {
  for (const [key, value] of Object.entries(values)) {
    if (value !== undefined) target[key] = value;
  }
}

function uniqueBounded(values: Array<string | undefined>, limit: number): { values: string[]; truncated: boolean } {
  const unique: string[] = [];
  const seen = new Set<string>();
  let truncated = false;
  for (const value of values) {
    if (!value || seen.has(value)) continue;
    seen.add(value);
    if (unique.length < limit) unique.push(value);
    else truncated = true;
  }
  return { values: unique, truncated };
}

const SAFE_COMMAND_LABELS = new Set([
  "bash", "biome", "bun", "bundle", "cargo", "cat", "clang", "clang++", "cmake", "cmd", "composer",
  "deno", "docker", "dotnet", "eslint", "find", "git", "go", "gradle", "gradlew", "grep", "head",
  "java", "javac", "ls", "make", "mvn", "ninja", "node", "npm", "npx", "perl", "php", "pnpm",
  "powershell", "pwd", "pwsh", "pytest", "rg", "ruby", "rustc", "sed", "sh", "swift", "tail", "tsc",
  "uv", "wc", "xcodebuild", "yarn", "zsh"
]);

function commandName(command: unknown): string | undefined {
  if (typeof command !== "string") return undefined;
  for (const token of command.trim().split(/\s+/)) {
    if (!token || token.includes("=")) continue;
    if (!/^[A-Za-z0-9_./:-]+$/.test(token)) return "complex";
    const baseName = path.posix.basename(token.replaceAll("\\", "/")).toLowerCase().replace(/\.exe$/, "");
    if (/^python(?:\d+(?:\.\d+)*)?$/.test(baseName)) return "python";
    return SAFE_COMMAND_LABELS.has(baseName) ? baseName : "other";
  }
  return undefined;
}

function patchPaths(patch: unknown): string[] {
  if (typeof patch !== "string") return [];
  const candidates: Array<string | undefined> = [];
  for (const line of patch.split(/\r?\n/)) {
    const match = /^(?:---|\+\+\+)\s+(?:[ab]\/)?(.+?)(?:\t.*)?$/.exec(line);
    if (!match || match[1] === "/dev/null") continue;
    candidates.push(safeRelativePath(match[1]));
  }
  return uniqueBounded(candidates, MAX_PATHS).values;
}

function summarizeArgs(tool: string, rawArgs: unknown): Record<string, unknown> {
  const args = objectValue(rawArgs);
  const summary: Record<string, unknown> = {};
  assignDefined(summary, {
    workspace_id: safeIdentifier(args.workspace_id),
    project_id: safeIdentifier(args.project_id)
  });

  switch (tool) {
    case "open_workspace":
      assignDefined(summary, {
        project_id: safeIdentifier(args.project_id),
        requested_root_digest: typeof args.root === "string" ? digest(args.root) : undefined,
        include_tree: boolValue(args.include_tree),
        max_depth: numberValue(args.max_depth)
      });
      break;
    case "open_current_workspace":
      assignDefined(summary, {
        include_tree: boolValue(args.include_tree),
        max_depth: numberValue(args.max_depth)
      });
      break;
    case "create_project":
      assignDefined(summary, {
        project_id: safeIdentifier(args.project_id),
        parent_id: safeIdentifier(args.parent_id),
        directory: safeRelativePath(args.directory),
        source: boundedString(args.source, 24),
        repository_supplied: typeof args.repository === "string",
        repository_digest: typeof args.repository === "string" ? digest(args.repository) : undefined,
        initial_branch: boundedString(args.initial_branch, 255),
        base_ref: boundedString(args.base_ref, 256),
        max_worktrees: numberValue(args.max_worktrees)
      });
      break;
    case "create_workspace":
      assignDefined(summary, {
        project_id: safeIdentifier(args.project_id),
        base_ref: boundedString(args.base_ref, 256),
        idempotency_key_supplied: typeof args.idempotency_key === "string"
      });
      break;
    case "release_workspace":
    case "remove_workspace":
      assignDefined(summary, { workspace_id: safeIdentifier(args.workspace_id) });
      break;
    case "read":
      assignDefined(summary, {
        path: safeRelativePath(args.path),
        start_line: numberValue(args.start_line),
        end_line: numberValue(args.end_line),
        max_bytes: numberValue(args.max_bytes)
      });
      break;
    case "write":
      assignDefined(summary, {
        path: safeRelativePath(args.path),
        content_bytes: utf8Bytes(args.content),
        create_dirs: boolValue(args.create_dirs),
        overwrite: boolValue(args.overwrite),
        expected_sha256_supplied: typeof args.expected_sha256 === "string"
      });
      break;
    case "edit":
      assignDefined(summary, {
        path: safeRelativePath(args.path),
        old_text_bytes: utf8Bytes(args.old_text),
        new_text_bytes: utf8Bytes(args.new_text),
        replace_all: boolValue(args.replace_all),
        expected_replacements: numberValue(args.expected_replacements),
        expected_sha256_supplied: typeof args.expected_sha256 === "string"
      });
      break;
    case "apply_patch":
      assignDefined(summary, {
        patch_bytes: utf8Bytes(args.patch),
        target_path_count: patchPaths(args.patch).length
      });
      break;
    case "import_file":
      assignDefined(summary, {
        path: safeRelativePath(args.destination ?? args.path ?? args.target_path),
        source_supplied: Boolean(args.file ?? args.source ?? args.url),
        overwrite: boolValue(args.overwrite),
        expected_sha256_supplied: typeof args.expected_sha256 === "string"
      });
      break;
    case "bash": {
      const command = typeof args.command === "string" ? args.command : undefined;
      assignDefined(summary, {
        cwd: safeRelativePath(args.cwd),
        command_name: commandName(command),
        command_digest: command ? digest(command) : undefined,
        command_bytes: utf8Bytes(command),
        timeout_ms: numberValue(args.timeout_ms),
        session_id_supplied: typeof args.session_id === "string"
      });
      break;
    }
    case "search": {
      const query = typeof args.query === "string" ? args.query : undefined;
      assignDefined(summary, {
        path: safeRelativePath(args.path),
        glob: boundedString(args.glob, 512),
        query_digest: query ? digest(query) : undefined,
        query_bytes: utf8Bytes(query),
        regex: boolValue(args.regex),
        include_hidden: boolValue(args.include_hidden),
        max_results: numberValue(args.max_results),
        intent: boundedString(args.intent, 32),
        symbol: typeof args.symbol === "string" ? opaqueRef("symbol", args.symbol) : undefined
      });
      break;
    }
    case "tree":
      assignDefined(summary, {
        path: safeRelativePath(args.path),
        max_depth: numberValue(args.max_depth),
        include_hidden: boolValue(args.include_hidden),
        max_entries: numberValue(args.max_entries)
      });
      break;
    case "inspect_workspace":
      assignDefined(summary, {
        path: safeRelativePath(args.path),
        max_files: numberValue(args.max_files),
        include_symbols: boolValue(args.include_symbols),
        include_relationships: boolValue(args.include_relationships)
      });
      break;
    case "show_changes":
    case "git_status":
    case "git_diff":
      assignDefined(summary, {
        path: safeRelativePath(args.path),
        staged: boolValue(args.staged),
        include_diff: boolValue(args.include_diff)
      });
      break;
    case "handoff_to_agent":
    case "handoff_to_codex":
      assignDefined(summary, {
        agent: boundedString(args.agent, 80),
        model: boundedString(args.model, 160),
        title_digest: typeof args.title === "string" ? digest(args.title) : undefined,
        plan_bytes: utf8Bytes(args.plan),
        append: boolValue(args.append)
      });
      break;
    case "codexpro_self_test":
      assignDefined(summary, {
        write_probe: boolValue(args.write_probe),
        bash_probe: boolValue(args.bash_probe),
        pro_context_probe: boolValue(args.pro_context_probe)
      });
      break;
    case "load_skill":
      assignDefined(summary, {
        skill_ref: typeof args.name === "string" ? opaqueRef("skill", args.name) : undefined,
        source: boundedString(args.source, 32),
        max_bytes: numberValue(args.max_bytes)
      });
      break;
    case "read_codex_session":
      assignDefined(summary, {
        session_ref: typeof args.session_id === "string" ? opaqueRef("session", args.session_id) : undefined,
        max_bytes: numberValue(args.max_bytes)
      });
      break;
    default:
      assignDefined(summary, {
        path: safeRelativePath(args.path),
        cwd: safeRelativePath(args.cwd),
        limit: numberValue(args.limit)
      });
      break;
  }

  return summary;
}

function structuredResult(rawResult: unknown): Record<string, unknown> {
  const result = objectValue(rawResult);
  const structured = objectValue(result.structuredContent);
  return Object.keys(structured).length ? structured : result;
}

function summarizeResult(tool: string, rawResult: unknown): Record<string, unknown> {
  const root = objectValue(rawResult);
  const result = structuredResult(rawResult);
  const summary: Record<string, unknown> = {};
  assignDefined(summary, {
    is_error: root.isError === true ? true : undefined,
    workspace_id: safeIdentifier(result.workspace_id ?? result.selected_workspace_id),
    project_id: safeIdentifier(result.project_id),
    path: safeRelativePath(result.path),
    changed: boolValue(result.changed),
    created: boolValue(result.created),
    existed: boolValue(result.existed),
    succeeded: boolValue(result.succeeded),
    truncated: boolValue(result.truncated),
    output_limited: boolValue(result.output_limited),
    state: boundedString(result.state, 80),
    status: boundedString(result.status, 80),
    exit_code: numberValue(result.exitCode ?? result.exit_code),
    duration_ms: numberValue(result.durationMs ?? result.duration_ms),
    bytes: numberValue(result.bytes),
    additions: numberValue(result.additions),
    deletions: numberValue(result.deletions),
    replacements: numberValue(result.replacements),
    count: numberValue(result.count)
  });

  const countArray = (key: string, outputKey = `${key}_count`) => {
    const value = result[key];
    if (Array.isArray(value)) summary[outputKey] = value.length;
  };
  countArray("changed_files");
  countArray("changed_paths");
  countArray("paths");
  countArray("files");
  countArray("matches");
  countArray("projects");
  countArray("workspaces");

  if (tool === "bash") {
    assignDefined(summary, {
      signal: boundedString(result.signal, 40),
      stdout_bytes: utf8Bytes(result.stdout),
      stderr_bytes: utf8Bytes(result.stderr)
    });
  }

  return summary;
}

function descriptorFor(tool: string, mutating: boolean): ToolDescriptor {
  return TOOL_DESCRIPTORS.get(tool) ?? {
    operation: mutating ? "tool.mutate" : "tool.read",
    operationClass: mutating ? "write" : "read",
    mutating
  };
}

function resultPaths(rawResult: unknown): string[] {
  const result = structuredResult(rawResult);
  const candidates: Array<string | undefined> = [safeRelativePath(result.path)];
  for (const key of ["paths", "changed_paths", "changed_files"]) {
    const value = result[key];
    if (!Array.isArray(value)) continue;
    for (const item of value) {
      if (typeof item === "string") {
        const tabPath = item.includes("\t") ? item.slice(item.lastIndexOf("\t") + 1) : item;
        candidates.push(safeRelativePath(tabPath));
      } else {
        const object = objectValue(item);
        candidates.push(safeRelativePath(object.path));
      }
    }
  }
  return uniqueBounded(candidates, MAX_PATHS).values;
}

function requestPaths(config: CodexProConfig, tool: string, rawArgs: unknown): string[] {
  const args = objectValue(rawArgs);
  const candidates: Array<string | undefined> = [];
  switch (tool) {
    case "write":
    case "edit":
    case "read":
    case "view_image":
      candidates.push(safeRelativePath(args.path));
      break;
    case "apply_patch":
      candidates.push(...patchPaths(args.patch));
      break;
    case "import_file":
      candidates.push(safeRelativePath(args.destination ?? args.path ?? args.target_path));
      break;
    case "codexpro_self_test":
      if (args.write_probe !== false) candidates.push(safeRelativePath(path.posix.join(config.contextDir, "codexpro-self-test.md")));
      break;
    case "export_pro_context":
      candidates.push(safeRelativePath(path.posix.join(config.contextDir, "pro-context.md")));
      break;
    case "handoff_to_agent":
    case "handoff_to_codex":
      candidates.push(safeRelativePath(path.posix.join(config.contextDir, "current-plan.md")));
      break;
    default:
      break;
  }
  return uniqueBounded(candidates, MAX_PATHS).values;
}

function targetRefs(config: CodexProConfig, tool: string, rawArgs: unknown, rawResult?: unknown): string[] {
  const args = objectValue(rawArgs);
  const candidates: Array<string | undefined> = [
    ...requestPaths(config, tool, rawArgs),
    ...resultPaths(rawResult)
  ];
  const projectId = safeIdentifier(args.project_id ?? structuredResult(rawResult).project_id);
  const workspaceId = safeIdentifier(args.workspace_id ?? structuredResult(rawResult).workspace_id ?? structuredResult(rawResult).selected_workspace_id);
  if (projectId) candidates.push(`project:${projectId}`);
  if (workspaceId && (tool === "create_workspace" || tool === "release_workspace" || tool === "remove_workspace")) {
    candidates.push(`workspace:${workspaceId}`);
  }
  if (tool === "create_project") {
    const parentId = safeIdentifier(args.parent_id);
    const directory = safeRelativePath(args.directory);
    if (parentId) candidates.push(`parent:${parentId}`);
    if (directory) candidates.push(`directory:${directory}`);
  }
  return uniqueBounded(candidates, MAX_TARGETS).values;
}

function isSubpath(child: string, parent: string): boolean {
  const relative = path.relative(parent, child);
  return relative === "" || (!relative.startsWith("..") && !path.isAbsolute(relative));
}

function projectIdForWorkspace(config: CodexProConfig, workspace: Workspace | undefined): string | undefined {
  if (!workspace) return undefined;
  if (workspace.projectId) return safeIdentifier(workspace.projectId);
  return [...config.projects]
    .filter((project) => isSubpath(workspace.root, project.root))
    .sort((left, right) => right.root.length - left.root.length)[0]?.id;
}

function capturePathEvidence(workspace: Workspace, relativePaths: string[]): PathEvidence[] {
  const evidence: PathEvidence[] = [];
  for (const relativePath of relativePaths.slice(0, MAX_PATHS)) {
    const clean = safeRelativePath(relativePath);
    if (!clean) continue;
    const absolute = path.resolve(workspace.root, clean);
    if (!isSubpath(absolute, workspace.root)) continue;
    try {
      const stat = fs.lstatSync(absolute);
      const kind: PathEvidence["kind"] = stat.isFile()
        ? "file"
        : stat.isDirectory()
          ? "directory"
          : stat.isSymbolicLink()
            ? "symlink"
            : "other";
      evidence.push({
        path: clean,
        exists: true,
        kind,
        size: stat.size,
        mtime_ms: Math.floor(stat.mtimeMs)
      });
    } catch (error) {
      const code = error && typeof error === "object" && "code" in error ? String(error.code) : "";
      evidence.push({ path: clean, exists: code !== "ENOENT" });
    }
  }
  return evidence;
}

function runGit(root: string, args: string[], maxBuffer: number): { ok: boolean; stdout: string } {
  const result = spawnSync("git", args, {
    cwd: root,
    encoding: "utf8",
    maxBuffer,
    env: { ...process.env, NO_COLOR: "1", GIT_OPTIONAL_LOCKS: "0" },
    windowsHide: true
  });
  return {
    ok: !result.error && result.status === 0,
    stdout: typeof result.stdout === "string" ? result.stdout : ""
  };
}

interface ParsedGitPathState {
  actualPath: string;
  displayPath: string;
  status: string;
}

function parsePorcelainPathStates(value: string): ParsedGitPathState[] {
  const records = value.split("\0");
  const states = new Map<string, ParsedGitPathState>();
  const add = (rawPath: unknown, rawStatus: string) => {
    const actualPath = normalizedRelativePath(rawPath);
    const displayPath = safeRelativePath(actualPath);
    if (!actualPath || !displayPath) return;
    states.set(actualPath, {
      actualPath,
      displayPath,
      status: rawStatus.replaceAll(" ", ".").slice(0, 2)
    });
  };

  for (let index = 0; index < records.length; index += 1) {
    const record = records[index] ?? "";
    if (!record || record.length < 4) continue;
    const status = record.slice(0, 2);
    add(record.slice(3), status);
    if ((status.includes("R") || status.includes("C")) && index + 1 < records.length) {
      add(records[index + 1], status);
      index += 1;
    }
  }
  return [...states.values()];
}

function gitPathStateFingerprint(root: string, state: ParsedGitPathState): string {
  const absolute = path.resolve(root, state.actualPath);
  if (!isSubpath(absolute, root)) return digest(`${state.status}\0outside-root`);
  try {
    const stat = fs.lstatSync(absolute);
    return digest([
      state.status,
      String(stat.mode),
      String(stat.size),
      String(Math.floor(stat.mtimeMs)),
      String(Math.floor(stat.ctimeMs)),
      String(stat.ino)
    ].join("\0"));
  } catch (error) {
    const code = error && typeof error === "object" && "code" in error ? String(error.code) : "unknown";
    return digest(`${state.status}\0${code}`);
  }
}

function captureGitEvidence(config: CodexProConfig, workspace: Workspace): GitEvidence | undefined {
  const maxBuffer = Math.max(64_000, Math.min(config.maxOutputBytes, 2_000_000));
  const inside = runGit(workspace.root, ["rev-parse", "--is-inside-work-tree"], maxBuffer);
  if (!inside.ok || inside.stdout.trim() !== "true") return undefined;

  const headResult = runGit(workspace.root, ["rev-parse", "--verify", "HEAD"], maxBuffer);
  const branchResult = runGit(workspace.root, ["symbolic-ref", "--quiet", "--short", "HEAD"], maxBuffer);
  const statusResult = runGit(workspace.root, ["status", "--porcelain=v1", "-z", "--untracked-files=all"], maxBuffer);
  const parsedStates = statusResult.ok ? parsePorcelainPathStates(statusResult.stdout) : [];
  const allPathStates: GitPathState[] = parsedStates.map((state) => ({
    path: state.displayPath,
    status: state.status,
    state_fingerprint: gitPathStateFingerprint(workspace.root, state)
  }));
  const allPaths = allPathStates.map((state) => state.path);
  const bounded = uniqueBounded(allPaths, MAX_PATHS);
  const head = headResult.ok ? boundedString(headResult.stdout.trim(), 128) : undefined;
  const branch = branchResult.ok ? boundedString(branchResult.stdout.trim(), 240) : undefined;
  const fingerprintInput = [
    head ?? "",
    statusResult.ok ? statusResult.stdout : "status-unavailable",
    ...allPathStates.map((state) => `${state.path}\0${state.state_fingerprint}`)
  ].join("\0");
  return {
    available: true,
    ...(head ? { head } : {}),
    ...(branch ? { branch } : {}),
    dirty: allPathStates.length > 0,
    changed_path_count: allPathStates.length,
    changed_paths: bounded.values,
    changed_paths_truncated: bounded.truncated,
    path_states: allPathStates.slice(0, MAX_PATHS),
    status_fingerprint: digest(fingerprintInput)
  };
}

function evidenceChanged(before: PathEvidence | undefined, after: PathEvidence | undefined): boolean {
  return JSON.stringify(before ?? null) !== JSON.stringify(after ?? null);
}

function changedPathsFromEvidence(
  mutating: boolean,
  status: ActionStatus,
  rawResult: unknown,
  before: ActionEvidenceSnapshot | undefined,
  after: ActionEvidenceSnapshot | undefined
): { values: string[]; truncated: boolean; count: number } {
  const candidates: Array<string | undefined> = [];
  if (mutating && status === "succeeded") candidates.push(...resultPaths(rawResult));

  let evidenceTruncated = false;
  if (before?.git && after?.git && before.git.status_fingerprint !== after.git.status_fingerprint) {
    const beforeStates = new Map(before.git.path_states.map((state) => [state.path, state]));
    const afterStates = new Map(after.git.path_states.map((state) => [state.path, state]));
    for (const pathName of new Set([...beforeStates.keys(), ...afterStates.keys()])) {
      if (beforeStates.get(pathName)?.state_fingerprint !== afterStates.get(pathName)?.state_fingerprint) {
        candidates.push(pathName);
      }
    }
    evidenceTruncated = before.git.changed_paths_truncated || after.git.changed_paths_truncated;
  } else if (!before?.git && after?.git?.dirty) {
    candidates.push(...after.git.changed_paths);
    evidenceTruncated = after.git.changed_paths_truncated;
  }

  const beforePaths = new Map((before?.paths ?? []).map((item) => [item.path, item]));
  const afterPaths = new Map((after?.paths ?? []).map((item) => [item.path, item]));
  for (const pathName of new Set([...beforePaths.keys(), ...afterPaths.keys()])) {
    if (evidenceChanged(beforePaths.get(pathName), afterPaths.get(pathName))) candidates.push(pathName);
  }

  const all = uniqueBounded(candidates, Number.MAX_SAFE_INTEGER).values;
  const bounded = uniqueBounded(all, MAX_PATHS);
  return {
    values: bounded.values,
    truncated: bounded.truncated || evidenceTruncated,
    count: all.length
  };
}

function errorTextForClassification(rawResult: unknown, error: unknown): string {
  if (error instanceof Error) return `${error.name}: ${error.message}`;
  if (error !== undefined) return String(error);
  const root = objectValue(rawResult);
  const structured = objectValue(root.structuredContent);
  const parts: string[] = [];
  if (typeof structured.error === "string") parts.push(structured.error);
  if (typeof structured.stderr === "string") parts.push(structured.stderr);
  if (Array.isArray(root.content)) {
    for (const item of root.content) {
      const object = objectValue(item);
      if (object.type === "text" && typeof object.text === "string") parts.push(object.text);
    }
  }
  return parts.join("\n");
}

function normalizedErrorCode(value: string): string {
  const normalized = value.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "");
  return normalized.slice(0, 80) || "error";
}

function classifyOutcome(tool: string, rawResult: unknown, error: unknown, context?: ToolCallContext): OutcomeClassification {
  const root = objectValue(rawResult);
  const result = structuredResult(rawResult);
  const text = errorTextForClassification(rawResult, error);
  const lower = text.toLowerCase();
  const rawExitCode = result.exitCode !== undefined ? result.exitCode : result.exit_code;
  const exitCode = numberValue(rawExitCode);
  const signal = boundedString(result.signal, 40);

  if (/timed?\s*out|timeout/.test(lower)) return { status: "timed_out", errorCode: "timeout" };
  if (context?.signal.aborted) return { status: "cancelled", errorCode: "cancelled" };

  if (tool === "bash") {
    if (exitCode !== undefined && exitCode !== 0) return { status: "failed", errorCode: `command_exit_${exitCode}` };
    if (signal) return { status: "failed", errorCode: `command_signal_${normalizedErrorCode(signal)}` };
    if (rawExitCode === null || (exitCode === undefined && root.isError === true)) return { status: "failed", errorCode: "command_failed" };
  }

  if (error !== undefined || root.isError === true) {
    if (
      /\b(blocked|disabled|forbidden|unauthori[sz]ed|outside allowed|not in the safe|safe bash allowlist|not available in the current mode|permission denied|approval)\b/.test(lower)
    ) {
      return { status: "blocked", errorCode: "policy_blocked" };
    }
    if (/invalid arguments?/.test(lower)) return { status: "failed", errorCode: "invalid_arguments" };
    if (/changed since|stale|conflict|already exists/.test(lower)) return { status: "failed", errorCode: "conflict" };
    if (/not found|does not exist|unknown project|unknown workspace/.test(lower)) return { status: "failed", errorCode: "not_found" };
    const name = error && typeof error === "object" && "name" in error ? String(error.name) : "tool_error";
    return { status: "failed", errorCode: normalizedErrorCode(name) };
  }

  return { status: "succeeded" };
}

function actionSummary(tool: string, status: ActionStatus, changedPathCount: number, resultMetadata: Record<string, unknown>): string {
  const pathSuffix = changedPathCount > 0
    ? `; ${changedPathCount} changed path${changedPathCount === 1 ? "" : "s"}`
    : "";
  if (tool === "bash" && typeof resultMetadata.exit_code === "number") {
    return `${tool} ${status}; exit ${resultMetadata.exit_code}${pathSuffix}`;
  }
  return `${tool} ${status}${pathSuffix}`;
}

function eventMatches(event: CodexProActionV1, options: ActionListOptions): boolean {
  if (options.mutatingOnly && !event.mutating) return false;
  if (options.toolName && event.tool_name !== options.toolName) return false;
  if (options.operationClass && event.operation_class !== options.operationClass) return false;
  if (options.status && event.status !== options.status) return false;
  if (options.projectId && event.project_id !== options.projectId) return false;
  if (options.workspaceId && event.workspace_id !== options.workspaceId) return false;
  return true;
}

function parseAction(line: string): CodexProActionV1 | undefined {
  try {
    const parsed = JSON.parse(line) as CodexProActionV1;
    if (
      parsed?.schema_version !== ACTION_SCHEMA_VERSION ||
      !Number.isSafeInteger(parsed.sequence) || parsed.sequence < 1 ||
      typeof parsed.action_id !== "string" ||
      typeof parsed.tool_name !== "string" ||
      !ACTION_STATUSES.includes(parsed.status)
    ) {
      return undefined;
    }
    return parsed;
  } catch {
    return undefined;
  }
}

function sleepSync(milliseconds: number): void {
  Atomics.wait(LOCK_SLEEP_ARRAY, 0, 0, milliseconds);
}

function lockOwnerIsAlive(lockPath: string): boolean {
  try {
    const firstLine = fs.readFileSync(lockPath, "utf8").split(/\r?\n/, 1)[0];
    const pid = Number(firstLine);
    if (!Number.isSafeInteger(pid) || pid <= 0) return false;
    try {
      process.kill(pid, 0);
      return true;
    } catch (error) {
      const code = error && typeof error === "object" && "code" in error ? String(error.code) : "";
      if (code === "EPERM") return true;
      if (code === "ESRCH") return false;
      return true;
    }
  } catch {
    return false;
  }
}

export class AuditJournal {
  readonly config: CodexProConfig;
  readonly serverSessionRef = `srv_${randomUUID().replaceAll("-", "")}`;
  private warned = false;
  private indexInitialized = false;
  private indexedOffset = 0;
  private journalIdentity?: string;
  private entries: IndexedAction[] = [];
  private byActionId = new Map<string, IndexedAction>();
  private byRequestFingerprint = new Map<string, IndexedAction>();
  private malformedRecords = 0;
  private gapDetected = false;
  private deduplicatedRequests = 0;
  private highestSequenceObserved = 0;
  private retentionIndex?: AuditJournalIndexV1;

  constructor(config: CodexProConfig) {
    this.config = config;
  }

  get enabled(): boolean {
    return this.config.auditMode !== "off";
  }

  capture(toolName: string, args: unknown, workspace?: Workspace, result?: unknown): ActionEvidenceSnapshot {
    const requestTargetPaths = requestPaths(this.config, toolName, args);
    const afterPaths = resultPaths(result);
    const paths = uniqueBounded([...requestTargetPaths, ...afterPaths], MAX_PATHS).values;
    try {
      return {
        ...(projectIdForWorkspace(this.config, workspace) ? { project_id: projectIdForWorkspace(this.config, workspace) } : {}),
        ...(workspace?.id ? { workspace_id: workspace.id } : {}),
        targets: targetRefs(this.config, toolName, args, result),
        ...(workspace ? { git: captureGitEvidence(this.config, workspace) } : {}),
        paths: workspace ? capturePathEvidence(workspace, paths) : []
      };
    } catch {
      return {
        ...(projectIdForWorkspace(this.config, workspace) ? { project_id: projectIdForWorkspace(this.config, workspace) } : {}),
        ...(workspace?.id ? { workspace_id: workspace.id } : {}),
        targets: targetRefs(this.config, toolName, args, result),
        paths: []
      };
    }
  }

  record(input: ActionRecordInput): ActionRecordResult {
    const outcome = classifyOutcome(input.toolName, input.result, input.error, input.context);
    if (!this.enabled) {
      return { enabled: false, recorded: false, duplicate: false, status: outcome.status };
    }

    try {
      return this.withJournalLock(() => {
        this.refreshIndex();
        this.terminatePartialRecordIfNeeded();
        this.refreshIndex();

        const actorRef = opaqueRef("actor", input.context?.principalId);
        const requestRef = opaqueRef("request", input.context?.requestId);
        const transportSessionRef = input.context?.transportSessionId
          ? opaqueRef("transport", input.context.transportSessionId)
          : undefined;
        const requestFingerprint = input.context?.requestId
          ? digest([
              actorRef,
              transportSessionRef ?? this.serverSessionRef,
              requestRef,
              input.toolName
            ].join("\0"))
          : undefined;
        const duplicate = requestFingerprint ? this.byRequestFingerprint.get(requestFingerprint) : undefined;
        if (duplicate) {
          this.deduplicatedRequests += 1;
          return {
            enabled: true,
            recorded: false,
            duplicate: true,
            status: outcome.status,
            action_id: duplicate.actionId,
            sequence: duplicate.sequence
          };
        }

        const descriptor = descriptorFor(input.toolName, input.mutating);
        const requestMetadata = summarizeArgs(input.toolName, input.args);
        const resultMetadata = summarizeResult(input.toolName, input.result);
        const changed = changedPathsFromEvidence(
          input.mutating,
          outcome.status,
          input.result,
          input.before,
          input.after
        );
        const targets = uniqueBounded([
          ...targetRefs(this.config, input.toolName, input.args, input.result),
          ...(input.before?.targets ?? []),
          ...(input.after?.targets ?? [])
        ], MAX_TARGETS).values;
        const result = structuredResult(input.result);
        const projectId = safeIdentifier(
          objectValue(input.args).project_id ??
          result.project_id ??
          input.after?.project_id ??
          input.before?.project_id
        );
        const workspaceId = safeIdentifier(
          objectValue(input.args).workspace_id ??
          result.workspace_id ??
          result.selected_workspace_id ??
          input.after?.workspace_id ??
          input.before?.workspace_id
        );
        const sequence = this.highestSequenceObserved + 1;
        const actionId = `cpa_${randomUUID().replaceAll("-", "")}`;
        const event: CodexProActionV1 = {
          schema_version: ACTION_SCHEMA_VERSION,
          sequence,
          action_id: actionId,
          occurred_at: new Date(input.startedAtMs).toISOString(),
          finished_at: new Date(input.finishedAtMs).toISOString(),
          ...(projectId ? { project_id: projectId } : {}),
          ...(workspaceId ? { workspace_id: workspaceId } : {}),
          tool_name: input.toolName,
          operation: descriptor.operation,
          operation_class: descriptor.operationClass,
          mutating: input.mutating || descriptor.mutating,
          invocation_surface: input.invocationSurface ?? "direct",
          actor_ref: actorRef,
          request_ref: requestRef,
          ...(transportSessionRef ? { transport_session_ref: transportSessionRef } : {}),
          server_session_ref: this.serverSessionRef,
          ...(requestFingerprint ? { request_fingerprint: requestFingerprint } : {}),
          status: outcome.status,
          duration_ms: Math.max(0, input.finishedAtMs - input.startedAtMs),
          targets,
          changed_paths: changed.values,
          changed_path_count: changed.count,
          changed_paths_truncated: changed.truncated,
          ...(input.before?.git ? { git_before: input.before.git } : {}),
          ...(input.after?.git ? { git_after: input.after.git } : {}),
          ...(input.before?.paths.length ? { path_evidence_before: input.before.paths } : {}),
          ...(input.after?.paths.length ? { path_evidence_after: input.after.paths } : {}),
          request_metadata: requestMetadata,
          result_metadata: resultMetadata,
          result_ref: `${JOURNAL_REF}/${actionId}`,
          ...(outcome.errorCode ? { error_code: outcome.errorCode } : {}),
          summary: actionSummary(input.toolName, outcome.status, changed.count, resultMetadata)
        };

        const serialized = `${JSON.stringify(event)}\n`;
        const serializedBytes = Buffer.byteLength(serialized, "utf8");
        if (serializedBytes > MAX_EVENT_BYTES) {
          throw new Error(`Action record exceeded ${MAX_EVENT_BYTES} bytes`);
        }

        const descriptorFd = fs.openSync(this.config.auditLogPath, "a", 0o600);
        let start = 0;
        try {
          start = fs.fstatSync(descriptorFd).size;
          fs.writeSync(descriptorFd, serialized, null, "utf8");
          fs.fsyncSync(descriptorFd);
        } finally {
          fs.closeSync(descriptorFd);
        }
        try {
          fs.chmodSync(this.config.auditLogPath, 0o600);
        } catch {
          // Best effort on platforms without POSIX permissions.
        }
        const indexed: IndexedAction = {
          sequence,
          actionId,
          ...(requestFingerprint ? { requestFingerprint } : {}),
          start,
          end: start + serializedBytes
        };
        this.indexAction(indexed);
        this.indexedOffset = indexed.end;
        let compacted = false;
        try {
          compacted = this.maybeCompact();
        } catch (error) {
          this.warn(`failed to compact action journal: ${error instanceof Error ? error.message : String(error)}`);
        }
        if (!compacted) {
          try {
            this.persistJournalIndex();
          } catch (error) {
            this.warn(`failed to persist action-journal index: ${error instanceof Error ? error.message : String(error)}`);
          }
        }
        return {
          enabled: true,
          recorded: true,
          duplicate: false,
          status: outcome.status,
          action_id: actionId,
          sequence
        };
      });
    } catch (error) {
      this.warn(`failed to append action journal: ${error instanceof Error ? error.message : String(error)}`);
      return { enabled: true, recorded: false, duplicate: false, status: outcome.status };
    }
  }

  list(options: ActionListOptions = {}): ActionListResult {
    if (!this.enabled) return this.emptyList(false);
    return this.withJournalLock(() => this.listUnlocked(options));
  }

  get(actionId: string): CodexProActionV1 | undefined {
    if (!this.enabled) return undefined;
    return this.withJournalLock(() => {
      this.refreshIndex();
      const entry = this.byActionId.get(actionId);
      if (!entry) return undefined;
      const file = fs.openSync(this.config.auditLogPath, "r");
      try {
        return this.readIndexedAction(entry, file);
      } finally {
        fs.closeSync(file);
      }
    });
  }

  status(): ActionStatusResult {
    if (!this.enabled) return this.statusUnlocked();
    return this.withJournalLock(() => {
      this.refreshIndex();
      return this.statusUnlocked();
    });
  }

  private listUnlocked(options: ActionListOptions): ActionListResult {
    this.refreshIndex();
    if (options.afterSequence !== undefined && this.gapDetected) {
      throw new Error("Action-journal gap detected; forward cursor reads are disabled until the source is reconciled.");
    }
    if (!this.entries.length) return this.emptyList(true);

    const limit = Math.max(1, Math.min(MAX_LIST_LIMIT, Math.floor(options.limit ?? DEFAULT_LIST_LIMIT)));
    const earliest = this.entries[0]?.sequence ?? 0;
    const latestAvailable = this.entries.at(-1)?.sequence ?? 0;
    const latest = Math.max(latestAvailable, this.highestSequenceObserved);
    const actions: CodexProActionV1[] = [];
    let nextSequence = options.afterSequence === undefined ? latestAvailable : Math.max(0, Math.floor(options.afterSequence));
    let hasMore = false;

    if (options.afterSequence !== undefined) {
      const requested = Math.max(0, Math.floor(options.afterSequence));
      if (requested > latest) throw new Error(`after_sequence ${requested} is beyond the latest action sequence ${latest}`);
      if (earliest > 0 && requested < earliest - 1) {
        const reason = this.retentionIndex?.dropped_through_sequence && requested <= this.retentionIndex.dropped_through_sequence
          ? ` expired because retention dropped actions through sequence ${this.retentionIndex.dropped_through_sequence};`
          : " is no longer available;";
        throw new Error(`after_sequence ${requested}${reason} the earliest retained action sequence is ${earliest}`);
      }
      let index = this.firstIndexAfter(requested);
      const file = fs.openSync(this.config.auditLogPath, "r");
      try {
        for (; index < this.entries.length; index += 1) {
          const entry = this.entries[index];
          nextSequence = entry.sequence;
          const action = this.readIndexedAction(entry, file);
          if (action && eventMatches(action, options)) actions.push(action);
          if (actions.length >= limit) {
            hasMore = index < this.entries.length - 1;
            break;
          }
        }
      } finally {
        fs.closeSync(file);
      }
    } else {
      const file = fs.openSync(this.config.auditLogPath, "r");
      try {
        for (let index = this.entries.length - 1; index >= 0 && actions.length < limit; index -= 1) {
          const action = this.readIndexedAction(this.entries[index], file);
          if (action && eventMatches(action, options)) actions.push(action);
        }
      } finally {
        fs.closeSync(file);
      }
      actions.reverse();
    }

    return {
      enabled: true,
      mode: this.config.auditMode,
      schema_version: ACTION_SCHEMA_VERSION,
      actions,
      next_sequence: nextSequence,
      earliest_sequence: earliest,
      latest_sequence: latest,
      has_more: hasMore,
      malformed_records: this.malformedRecords,
      gap_detected: this.gapDetected
    };
  }

  private statusUnlocked(): ActionStatusResult {
    const earliest = this.entries[0]?.sequence ?? 0;
    const latest = Math.max(this.entries.at(-1)?.sequence ?? 0, this.highestSequenceObserved);
    let storageBytes = 0;
    if (this.enabled) {
      try {
        storageBytes = fs.statSync(this.config.auditLogPath).size;
      } catch {}
    }
    return {
      enabled: this.enabled,
      mode: this.config.auditMode,
      schema_version: ACTION_SCHEMA_VERSION,
      storage_format: "jsonl",
      journal_ref: JOURNAL_REF,
      retained_from_sequence: earliest,
      latest_sequence: latest,
      next_sequence: latest + 1,
      action_count: this.entries.length,
      malformed_records: this.malformedRecords,
      gap_detected: this.gapDetected,
      storage_bytes: storageBytes,
      deduplicated_requests: this.deduplicatedRequests,
      retention: {
        max_bytes: this.config.auditMaxBytes,
        retain_actions: this.config.auditRetainActions,
        rotation_count: this.retentionIndex?.rotation_count ?? 0,
        dropped_through_sequence: this.retentionIndex?.dropped_through_sequence ?? 0,
        ...(this.retentionIndex?.compacted_at ? { compacted_at: this.retentionIndex.compacted_at } : {})
      }
    };
  }

  private emptyList(enabled: boolean): ActionListResult {
    const latest = enabled ? this.highestSequenceObserved : 0;
    return {
      enabled,
      mode: this.config.auditMode,
      schema_version: ACTION_SCHEMA_VERSION,
      actions: [],
      next_sequence: latest,
      earliest_sequence: 0,
      latest_sequence: latest,
      has_more: false,
      malformed_records: this.malformedRecords,
      gap_detected: this.gapDetected
    };
  }

  private withJournalLock<T>(operation: () => T): T {
    const directory = path.dirname(this.config.auditLogPath);
    fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
    try {
      fs.chmodSync(directory, 0o700);
    } catch {}
    const lockPath = `${this.config.auditLogPath}.lock`;
    const deadline = Date.now() + LOCK_WAIT_MS;
    let lockFd: number | undefined;
    while (lockFd === undefined) {
      try {
        lockFd = fs.openSync(lockPath, "wx", 0o600);
        fs.writeSync(lockFd, `${process.pid}\n${Date.now()}\n`, null, "utf8");
      } catch (error) {
        const code = error && typeof error === "object" && "code" in error ? String(error.code) : "";
        if (code !== "EEXIST") throw error;
        try {
          const stat = fs.statSync(lockPath);
          if (Date.now() - stat.mtimeMs > LOCK_STALE_MS && !lockOwnerIsAlive(lockPath)) {
            fs.unlinkSync(lockPath);
            continue;
          }
        } catch {}
        if (Date.now() >= deadline) throw new Error(`Timed out waiting for action journal lock: ${path.basename(lockPath)}`);
        sleepSync(LOCK_POLL_MS);
      }
    }

    try {
      return operation();
    } finally {
      try {
        fs.closeSync(lockFd);
      } catch {}
      try {
        fs.unlinkSync(lockPath);
      } catch {}
    }
  }

  private maybeCompact(): boolean {
    if (this.gapDetected) return false;
    let storageBytes = 0;
    try {
      storageBytes = fs.statSync(this.config.auditLogPath).size;
    } catch {
      return false;
    }
    if (
      storageBytes <= this.config.auditMaxBytes &&
      this.entries.length <= this.config.auditRetainActions
    ) {
      return false;
    }
    if (this.entries.length <= 1) return false;

    let retainFromIndex = Math.max(0, this.entries.length - this.config.auditRetainActions);
    const targetBytes = Math.max(1, Math.floor(this.config.auditMaxBytes * 0.8));
    const journalEnd = this.entries.at(-1)?.end ?? 0;
    while (
      retainFromIndex < this.entries.length - 1 &&
      journalEnd - this.entries[retainFromIndex].start > targetBytes
    ) {
      retainFromIndex += 1;
    }
    if (retainFromIndex <= 0) return false;

    const firstRetained = this.entries[retainFromIndex];
    const latestRetained = this.entries.at(-1);
    if (!firstRetained || !latestRetained) return false;

    const tempPath = `${this.config.auditLogPath}.compact-${process.pid}-${randomUUID()}`;
    let source: number | undefined;
    let destination: number | undefined;
    try {
      source = fs.openSync(this.config.auditLogPath, "r");
      destination = fs.openSync(tempPath, "wx", 0o600);
      for (let index = retainFromIndex; index < this.entries.length; index += 1) {
        const action = this.readIndexedAction(this.entries[index], source);
        if (!action) throw new Error(`Cannot compact unreadable action at sequence ${this.entries[index].sequence}`);
        fs.writeSync(destination, `${JSON.stringify(action)}\n`, null, "utf8");
      }
      fs.fsyncSync(destination);
    } catch (error) {
      try {
        fs.unlinkSync(tempPath);
      } catch {}
      throw error;
    } finally {
      if (source !== undefined) fs.closeSync(source);
      if (destination !== undefined) fs.closeSync(destination);
    }

    const now = new Date().toISOString();
    const journalIndex: AuditJournalIndexV1 = {
      schema_version: ACTION_SCHEMA_VERSION,
      journal_ref: JOURNAL_REF,
      rotation_count: (this.retentionIndex?.rotation_count ?? 0) + 1,
      retained_from_sequence: firstRetained.sequence,
      dropped_through_sequence: firstRetained.sequence - 1,
      latest_sequence: Math.max(latestRetained.sequence, this.highestSequenceObserved),
      updated_at: now,
      compacted_at: now
    };
    let journalReplaced = false;

    try {
      this.replacePrivateFile(tempPath, this.config.auditLogPath);
      journalReplaced = true;
      this.retentionIndex = journalIndex;
      this.writePrivateFile(`${this.config.auditLogPath}.index.json`, `${JSON.stringify(journalIndex, null, 2)}\n`);
      this.resetIndex();
      this.indexInitialized = false;
      this.journalIdentity = undefined;
      this.refreshIndex();
      return true;
    } catch (error) {
      try {
        fs.unlinkSync(tempPath);
      } catch {}
      if (journalReplaced) {
        this.retentionIndex = journalIndex;
        this.resetIndex();
        this.indexInitialized = false;
        this.journalIdentity = undefined;
      }
      throw error;
    }
  }

  private persistJournalIndex(): void {
    const firstActual = this.entries[0]?.sequence ?? 0;
    const latestActual = this.entries.at(-1)?.sequence ?? 0;
    const previous = this.retentionIndex;
    const previousHasActions = (previous?.latest_sequence ?? 0) > 0;
    const retainedFromSequence = previousHasActions
      ? previous?.retained_from_sequence ?? 1
      : firstActual === 1
        ? 1
        : firstActual > 1
          ? 1
          : 0;
    const now = new Date().toISOString();
    const journalIndex: AuditJournalIndexV1 = {
      schema_version: ACTION_SCHEMA_VERSION,
      journal_ref: JOURNAL_REF,
      rotation_count: previous?.rotation_count ?? 0,
      retained_from_sequence: retainedFromSequence,
      dropped_through_sequence: previous?.dropped_through_sequence ?? 0,
      latest_sequence: Math.max(previous?.latest_sequence ?? 0, latestActual, this.highestSequenceObserved),
      updated_at: now,
      ...(previous?.compacted_at ? { compacted_at: previous.compacted_at } : {})
    };
    const needsRefresh = !this.indexInitialized;
    this.writePrivateFile(`${this.config.auditLogPath}.index.json`, `${JSON.stringify(journalIndex, null, 2)}\n`);
    this.retentionIndex = journalIndex;
    this.highestSequenceObserved = Math.max(this.highestSequenceObserved, journalIndex.latest_sequence);
    if (needsRefresh) this.refreshIndex();
  }

  private readRetentionIndex(): AuditJournalIndexV1 | undefined {
    const indexPath = `${this.config.auditLogPath}.index.json`;
    if (!fs.existsSync(indexPath)) return undefined;
    try {
      const parsed = JSON.parse(fs.readFileSync(indexPath, "utf8")) as AuditJournalIndexV1;
      const hasActions = Number.isSafeInteger(parsed.latest_sequence) && parsed.latest_sequence > 0;
      const hasRetention = Number.isSafeInteger(parsed.dropped_through_sequence) && parsed.dropped_through_sequence > 0;
      if (
        parsed?.schema_version !== ACTION_SCHEMA_VERSION ||
        parsed.journal_ref !== JOURNAL_REF ||
        !Number.isSafeInteger(parsed.rotation_count) || parsed.rotation_count < 0 ||
        !Number.isSafeInteger(parsed.retained_from_sequence) || parsed.retained_from_sequence < 0 ||
        !Number.isSafeInteger(parsed.dropped_through_sequence) || parsed.dropped_through_sequence < 0 ||
        !Number.isSafeInteger(parsed.latest_sequence) || parsed.latest_sequence < 0 ||
        (!hasActions && (parsed.retained_from_sequence !== 0 || parsed.dropped_through_sequence !== 0)) ||
        (hasActions && parsed.retained_from_sequence < 1) ||
        (hasActions && parsed.latest_sequence < parsed.retained_from_sequence) ||
        (!hasRetention && hasActions && parsed.retained_from_sequence !== 1) ||
        (hasRetention && parsed.dropped_through_sequence !== parsed.retained_from_sequence - 1) ||
        (hasRetention && parsed.rotation_count < 1) ||
        typeof parsed.updated_at !== "string" ||
        Number.isNaN(Date.parse(parsed.updated_at)) ||
        (parsed.compacted_at !== undefined && (
          typeof parsed.compacted_at !== "string" || Number.isNaN(Date.parse(parsed.compacted_at))
        )) ||
        (hasRetention && !parsed.compacted_at)
      ) {
        throw new Error("invalid journal index shape");
      }
      return parsed;
    } catch (error) {
      this.gapDetected = true;
      this.warn(`invalid action-journal index: ${error instanceof Error ? error.message : String(error)}`);
      return undefined;
    }
  }

  private writePrivateFile(targetPath: string, content: string): void {
    const tempPath = `${targetPath}.tmp-${process.pid}-${randomUUID()}`;
    let descriptor: number | undefined;
    try {
      descriptor = fs.openSync(tempPath, "wx", 0o600);
      fs.writeSync(descriptor, content, null, "utf8");
      fs.fsyncSync(descriptor);
      fs.closeSync(descriptor);
      descriptor = undefined;
      this.replacePrivateFile(tempPath, targetPath);
    } catch (error) {
      try {
        fs.unlinkSync(tempPath);
      } catch {}
      throw error;
    } finally {
      if (descriptor !== undefined) fs.closeSync(descriptor);
    }
  }

  private replacePrivateFile(sourcePath: string, targetPath: string): void {
    try {
      fs.renameSync(sourcePath, targetPath);
    } catch (error) {
      const code = error && typeof error === "object" && "code" in error ? String(error.code) : "";
      if (code !== "EEXIST" && code !== "EPERM" && code !== "EACCES") throw error;
      const backupPath = `${targetPath}.backup-${process.pid}-${randomUUID()}`;
      let movedExisting = false;
      try {
        if (fs.existsSync(targetPath)) {
          fs.renameSync(targetPath, backupPath);
          movedExisting = true;
        }
        fs.renameSync(sourcePath, targetPath);
        if (movedExisting) fs.unlinkSync(backupPath);
      } catch (replacementError) {
        try {
          if (movedExisting && !fs.existsSync(targetPath) && fs.existsSync(backupPath)) {
            fs.renameSync(backupPath, targetPath);
          }
        } catch {}
        throw replacementError;
      }
    }
    try {
      fs.chmodSync(targetPath, 0o600);
    } catch {}
  }

  private validateRetentionBoundary(): void {
    const first = this.entries[0];
    const latestActual = this.entries.at(-1)?.sequence ?? 0;
    if (!first) {
      if ((this.retentionIndex?.latest_sequence ?? 0) > 0) this.gapDetected = true;
      return;
    }
    if (!this.retentionIndex) {
      if (first.sequence !== 1) this.gapDetected = true;
      return;
    }

    const droppedThrough = this.retentionIndex.dropped_through_sequence;
    if (
      this.retentionIndex.retained_from_sequence !== first.sequence ||
      (droppedThrough === 0 && first.sequence !== 1) ||
      (droppedThrough > 0 && droppedThrough !== first.sequence - 1) ||
      latestActual < this.retentionIndex.latest_sequence
    ) {
      this.gapDetected = true;
    }
    this.highestSequenceObserved = Math.max(
      this.highestSequenceObserved,
      latestActual,
      this.retentionIndex.latest_sequence
    );
  }

  private refreshIndex(): void {
    if (!this.enabled) return;
    const previousRetention = this.retentionIndex;
    const loadedRetention = this.readRetentionIndex();
    const expectedCompaction = Boolean(
      loadedRetention &&
      loadedRetention.rotation_count > (previousRetention?.rotation_count ?? 0)
    );
    if (previousRetention && !loadedRetention) this.gapDetected = true;
    this.retentionIndex = loadedRetention;
    if (loadedRetention) {
      this.highestSequenceObserved = Math.max(this.highestSequenceObserved, loadedRetention.latest_sequence);
    }

    if (!fs.existsSync(this.config.auditLogPath)) {
      if (
        this.indexInitialized && (this.entries.length > 0 || this.indexedOffset > 0) ||
        (loadedRetention?.latest_sequence ?? 0) > 0
      ) {
        this.gapDetected = true;
      }
      this.resetIndex();
      this.indexInitialized = true;
      this.journalIdentity = undefined;
      return;
    }

    const stat = fs.statSync(this.config.auditLogPath);
    const identity = `${stat.dev}:${stat.ino}:${stat.birthtimeMs}`;
    if (!this.indexInitialized) {
      this.resetIndex();
      this.indexInitialized = true;
      this.journalIdentity = identity;
    } else if (this.journalIdentity === undefined) {
      // This instance created the journal after first observing that it did not exist.
      this.journalIdentity = identity;
    } else if (this.journalIdentity !== identity || stat.size < this.indexedOffset) {
      if (!expectedCompaction) this.gapDetected = true;
      this.resetIndex();
      this.journalIdentity = identity;
    }
    if (stat.size <= this.indexedOffset) {
      this.validateRetentionBoundary();
      return;
    }

    const file = fs.openSync(this.config.auditLogPath, "r");
    let position = this.indexedOffset;
    let carry = Buffer.alloc(0);
    let carryStart = position;
    try {
      while (position < stat.size) {
        const requested = Math.min(JOURNAL_READ_CHUNK_BYTES, stat.size - position);
        const chunk = Buffer.alloc(requested);
        const bytesRead = fs.readSync(file, chunk, 0, requested, position);
        if (bytesRead <= 0) break;
        position += bytesRead;
        const combined = carry.length
          ? Buffer.concat([carry, chunk.subarray(0, bytesRead)])
          : chunk.subarray(0, bytesRead);
        let cursor = 0;
        let lineStart = carryStart;
        while (cursor < combined.length) {
          const newline = combined.indexOf(0x0a, cursor);
          if (newline < 0) break;
          const lineBuffer = combined.subarray(cursor, newline);
          const lineEnd = lineStart + lineBuffer.length + 1;
          this.indexLine(lineBuffer.toString("utf8"), lineStart, lineEnd);
          cursor = newline + 1;
          lineStart = lineEnd;
        }
        carry = combined.subarray(cursor);
        carryStart = lineStart;
      }
    } finally {
      fs.closeSync(file);
    }
    this.indexedOffset = carryStart;
    this.validateRetentionBoundary();
  }

  private terminatePartialRecordIfNeeded(): void {
    if (!fs.existsSync(this.config.auditLogPath)) return;
    const size = fs.statSync(this.config.auditLogPath).size;
    if (size <= this.indexedOffset) return;
    const file = fs.openSync(this.config.auditLogPath, "a", 0o600);
    try {
      fs.writeSync(file, "\n", null, "utf8");
      fs.fsyncSync(file);
    } finally {
      fs.closeSync(file);
    }
  }

  private indexLine(line: string, start: number, end: number): void {
    if (!line) return;
    const action = parseAction(line);
    const lastSequence = this.entries.at(-1)?.sequence ?? 0;
    if (!action || action.sequence <= lastSequence || this.byActionId.has(action.action_id)) {
      this.malformedRecords += 1;
      this.gapDetected = true;
      return;
    }
    if (lastSequence === 0) {
      const retainedBoundary = Boolean(
        this.retentionIndex &&
        action.sequence === this.retentionIndex.retained_from_sequence &&
        this.retentionIndex.dropped_through_sequence === action.sequence - 1
      );
      if (action.sequence !== 1 && !retainedBoundary) this.gapDetected = true;
    } else if (action.sequence !== lastSequence + 1) {
      this.gapDetected = true;
    }
    this.indexAction({
      sequence: action.sequence,
      actionId: action.action_id,
      ...(action.request_fingerprint ? { requestFingerprint: action.request_fingerprint } : {}),
      start,
      end
    });
  }

  private indexAction(entry: IndexedAction): void {
    this.entries.push(entry);
    this.highestSequenceObserved = Math.max(this.highestSequenceObserved, entry.sequence);
    this.byActionId.set(entry.actionId, entry);
    if (entry.requestFingerprint) this.byRequestFingerprint.set(entry.requestFingerprint, entry);
  }

  private resetIndex(): void {
    this.indexedOffset = 0;
    this.entries = [];
    this.byActionId.clear();
    this.byRequestFingerprint.clear();
    this.malformedRecords = 0;
  }

  private firstIndexAfter(sequence: number): number {
    let low = 0;
    let high = this.entries.length;
    while (low < high) {
      const middle = Math.floor((low + high) / 2);
      if (this.entries[middle].sequence <= sequence) low = middle + 1;
      else high = middle;
    }
    return low;
  }

  private readIndexedAction(entry: IndexedAction, file: number): CodexProActionV1 | undefined {
    const length = entry.end - entry.start;
    if (length <= 1 || length > MAX_EVENT_BYTES) return undefined;
    const buffer = Buffer.alloc(length);
    const bytesRead = fs.readSync(file, buffer, 0, length, entry.start);
    if (bytesRead !== length) return undefined;
    return parseAction(buffer.toString("utf8").replace(/\n$/, ""));
  }

  private warn(message: string): void {
    if (this.warned) return;
    this.warned = true;
    console.error(`[CodexProAudit] ${message}`);
  }
}
