import fs from "node:fs";
import { createHash } from "node:crypto";
import fsp from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { Minimatch } from "minimatch";
import type { CodexProConfig } from "./config.js";
import { expandHome } from "./config.js";
import type { ProjectDefinition, ProjectSummary } from "./projects/types.js";

export interface Workspace {
  id: string;
  root: string;
  openedAt: string;
  kind?: "direct" | "worktree";
  branch?: string;
  baseCommit?: string;
  projectId?: string;
}

export interface CodexProRecoveryHint {
  tool?: "read" | "search" | "ast_grep" | "edit" | "apply_patch" | "batch" | "show_changes" | "list_projects" | "open_workspace" | "write" | "bash";
  message: string;
  args?: Record<string, string | number | boolean>;
}

export interface CodexProErrorOptions {
  /** Stable machine-readable code, surfaced as error_code. See docs/ERROR_CODES.md. */
  code?: string;
  recovery?: CodexProRecoveryHint;
  retryUnchanged?: boolean;
  /** Extra structured fields merged into the error result (e.g. known_project_ids). */
  details?: Record<string, unknown>;
}

export class CodexProError extends Error {
  readonly code?: string;
  readonly recovery?: CodexProRecoveryHint;
  readonly retryUnchanged?: boolean;
  readonly details?: Record<string, unknown>;

  constructor(message: string, options: CodexProErrorOptions = {}) {
    super(message);
    this.name = "CodexProError";
    this.code = options.code;
    this.recovery = options.recovery;
    this.retryUnchanged = options.retryUnchanged;
    this.details = options.details;
  }
}

const SECRET_GLOB_HINT = /(secret|credential|\.env|token|\.pem|\.key|password|private)/i;

/** Blocked-path error that tells the caller *why* (secret-like vs. artifact) so it can decide whether bash is appropriate. */
export function blockedPathError(relPath: string, glob: string): CodexProError {
  const secretLike = SECRET_GLOB_HINT.test(glob);
  return new CodexProError(
    secretLike
      ? `Path is blocked because it matches the secret-like pattern ${glob}: ${relPath}. Its contents are never exposed to the model.`
      : `Path is blocked by the artifact/dependency pattern ${glob}: ${relPath}. Build outputs, dependencies and caches are not readable or writable through file tools; verify them with bash instead.`,
    { code: "path_blocked", retryUnchanged: false, details: { blocked_glob: glob, blocked_reason: secretLike ? "secret" : "artifact" } }
  );
}

/** Unknown catalog project id(s): lists the valid ids so the caller stops guessing. */
export function unknownProjectError(config: CodexProConfig, unknownIds: string[]): CodexProError {
  const known = config.projects.map((project) => project.id);
  const plural = unknownIds.length > 1;
  return new CodexProError(
    `Unknown project_id${plural ? "s" : ""}: ${unknownIds.join(", ")}. Configured project ids: ${known.join(", ") || "none"}. ` +
      "If the project you need is not in that list it is not configured on this server; ask the user to add it to the catalog (or use create_project when it is available). Do not guess other ids.",
    {
      code: "project_unknown",
      retryUnchanged: false,
      recovery: { tool: "list_projects", message: "Pick one of the configured project ids." },
      details: { known_project_ids: known, unknown_project_ids: unknownIds }
    }
  );
}

export function isSubpath(child: string, parent: string): boolean {
  const relative = path.relative(parent, child);
  return relative === "" || (!relative.startsWith("..") && !path.isAbsolute(relative));
}

export function normalizeRelPath(relPath: string): string {
  const normalized = relPath.split(path.sep).join("/");
  if (normalized === "") return ".";
  return normalized;
}

export function displayPath(absPath: string, root: string): string {
  const rel = path.relative(root, absPath) || ".";
  return normalizeRelPath(rel);
}

export function workspaceIdForRoot(realRoot: string): string {
  return `ws_${createHash("sha256").update(realRoot).digest("hex").slice(0, 24)}`;
}

function maybeRealpath(existingPath: string): string | undefined {
  try {
    return fs.realpathSync.native(existingPath);
  } catch {
    return undefined;
  }
}

function closestExistingParent(absPath: string): string {
  let current = path.resolve(absPath);
  while (!fs.existsSync(current)) {
    const parent = path.dirname(current);
    if (parent === current) break;
    current = parent;
  }
  return current;
}

export class WorkspaceManager {
  private readonly workspaces = new Map<string, Workspace>();
  private selectedWorkspaceId?: string;

  constructor(private readonly config: CodexProConfig) {}

  defaultWorkspace(): Workspace {
    const existing = [...this.workspaces.values()].find((workspace) => workspace.root === this.config.defaultRoot);
    return existing ?? this.openWorkspace(this.config.defaultRoot, { select: false });
  }

  openProject(projectId: string): Workspace {
    const project = this.config.projects.find((candidate) => candidate.id === projectId);
    if (!project) throw unknownProjectError(this.config, [projectId]);
    return this.openWorkspace(project.root);
  }

  /** Deterministic workspace id for a catalog project; getWorkspace() opens it on first use. */
  workspaceIdForProject(projectId: string): string | undefined {
    const project = this.config.projects.find((candidate) => candidate.id === projectId);
    return project ? workspaceIdForRoot(project.root) : undefined;
  }

  selectDefaultWorkspace(): Workspace {
    const workspace = this.defaultWorkspace();
    this.selectedWorkspaceId = workspace.id;
    return workspace;
  }

  openWorkspace(rootInput?: string, options: { select?: boolean } = {}): Workspace {
    const requested = rootInput?.trim() ? expandHome(rootInput.trim()) : this.config.defaultRoot;
    const resolved = path.resolve(requested);
    if (!fs.existsSync(resolved)) {
      throw new CodexProError(`Workspace root does not exist: ${resolved}`, { code: "workspace_root_invalid", retryUnchanged: false });
    }
    const stat = fs.statSync(resolved);
    if (!stat.isDirectory()) {
      throw new CodexProError(`Workspace root is not a directory: ${resolved}`, { code: "workspace_root_invalid", retryUnchanged: false });
    }
    const realRoot = fs.realpathSync.native(resolved);
    const allowed = this.config.allowedRoots.some((allowedRoot) => isSubpath(realRoot, allowedRoot));
    if (!allowed) {
      throw new CodexProError(
        `Workspace root is outside allowed roots: ${realRoot}\nAllowed roots:\n${this.config.allowedRoots.map((r) => `- ${r}`).join("\n")}`,
        { code: "workspace_root_not_allowed", retryUnchanged: false, recovery: { tool: "list_projects", message: "Open a configured project by project_id instead of a path." } }
      );
    }

    const existing = [...this.workspaces.values()].find((workspace) => workspace.root === realRoot);
    if (existing) {
      if (options.select !== false) this.selectedWorkspaceId = existing.id;
      return existing;
    }

    const id = workspaceIdForRoot(realRoot);
    const project = [...this.config.projects]
      .filter((candidate) => isSubpath(realRoot, candidate.root))
      .sort((a, b) => b.root.length - a.root.length)[0];
    const workspace = {
      id,
      root: realRoot,
      openedAt: new Date().toISOString(),
      kind: "direct" as const,
      projectId: project?.id
    };
    this.workspaces.set(id, workspace);
    if (options.select !== false) this.selectedWorkspaceId = id;
    return workspace;
  }

  getWorkspace(id?: string): Workspace {
    if (!id) {
      if (this.selectedWorkspaceId) {
        const selected = this.workspaces.get(this.selectedWorkspaceId);
        if (selected) return selected;
      }
      return this.selectDefaultWorkspace();
    }
    const workspace = this.workspaces.get(id);
    if (!workspace) {
      const configuredRoot = this.config.allowedRoots.find((allowedRoot) => workspaceIdForRoot(allowedRoot) === id);
      if (configuredRoot) return this.openWorkspace(configuredRoot, { select: false });
    }
    if (!workspace) {
      const known = [...new Set([...this.workspaces.keys(), ...this.config.projects.map((project) => workspaceIdForRoot(project.root))])];
      throw new CodexProError(
        `Unknown workspace_id: ${id}. Known workspace ids: ${known.join(", ") || "none"}. Use the workspace_id from list_projects or open_workspace.`,
        {
          code: "workspace_unknown",
          retryUnchanged: false,
          recovery: { tool: "list_projects", message: "list_projects returns each project's workspace_id; open_workspace(project_id) also returns it." },
          details: { known_workspace_ids: known }
        }
      );
    }
    return workspace;
  }

  listWorkspaces(): Workspace[] {
    return [...this.workspaces.values()];
  }

  currentWorkspaceId(): string {
    return this.getWorkspace().id;
  }

  listProjects(): ProjectSummary[] {
    return this.config.projects.map((project) => ({
      id: project.id,
      label: project.label,
      default: project.id === this.config.defaultProjectId,
      baseRef: project.baseRef ?? this.config.worktreeBaseRef,
      maxWorktrees: Math.min(project.maxWorktrees ?? this.config.maxWorktrees, this.config.maxWorktrees)
    }));
  }

  addProject(project: ProjectDefinition): ProjectSummary {
    if (this.config.projects.some((candidate) => candidate.id === project.id)) {
      throw new CodexProError(`Project id already exists: ${project.id}`, { code: "project_exists", retryUnchanged: false });
    }
    if (this.config.projects.some((candidate) => candidate.root === project.root)) {
      throw new CodexProError(`Project root already exists: ${project.root}`, { code: "project_exists", retryUnchanged: false });
    }
    const creationParents = this.config.projectCreationRoots.map((creationRoot) => creationRoot.root);
    if (![...this.config.allowedRoots, ...creationParents].some((allowedRoot) => isSubpath(project.root, allowedRoot))) {
      throw new CodexProError("New project root must stay inside an allowed project or creation root.", { code: "project_root_not_allowed", retryUnchanged: false });
    }
    this.config.projects.push(project);
    if (!this.config.allowedRoots.includes(project.root)) this.config.allowedRoots.push(project.root);
    return {
      id: project.id,
      label: project.label,
      default: false,
      baseRef: project.baseRef ?? this.config.worktreeBaseRef,
      maxWorktrees: Math.min(project.maxWorktrees ?? this.config.maxWorktrees, this.config.maxWorktrees)
    };
  }
}

interface BlockedGlobMatcher {
  glob: string;
  full: Minimatch;
  base: Minimatch;
}

export class PathGuard {
  // Compiled once per guard: the functional minimatch() re-parses every glob on
  // every call, which dominated dashboard rendering (thousands of paths per page).
  private readonly blockedMatchers: BlockedGlobMatcher[];

  constructor(private readonly config: CodexProConfig) {
    this.blockedMatchers = config.blockedGlobs.map((glob) => ({
      glob,
      full: new Minimatch(glob, { dot: true, nocase: false, matchBase: false }),
      base: new Minimatch(glob, { dot: true, nocase: false, matchBase: true })
    }));
  }

  isBlockedRelativePath(relPath: string): boolean {
    return this.blockedGlob(relPath) !== undefined;
  }

  /** The first blocked glob that matches, or undefined when the path is allowed. */
  blockedGlob(relPath: string): string | undefined {
    const rel = normalizeRelPath(relPath).replace(/^\.\//, "");
    if (!rel || rel === ".") return undefined;
    const base = path.basename(rel);
    return this.blockedMatchers.find((matcher) => matcher.full.match(rel) || matcher.base.match(base))?.glob;
  }

  assertNotBlocked(relPath: string): void {
    const glob = this.blockedGlob(relPath);
    if (glob !== undefined) throw blockedPathError(relPath, glob);
  }

  resolve(workspace: Workspace, inputPath = ".", options: { forWrite?: boolean } = {}): { absPath: string; relPath: string } {
    const expanded = expandHome(inputPath || ".");
    const candidate = path.isAbsolute(expanded) ? expanded : path.join(workspace.root, expanded);
    let absPath = path.resolve(candidate);
    const realTarget = maybeRealpath(absPath);
    let relPath = displayPath(absPath, workspace.root);

    if (!isSubpath(absPath, workspace.root)) {
      if (realTarget && isSubpath(realTarget, workspace.root)) {
        absPath = realTarget;
        relPath = displayPath(realTarget, workspace.root);
      } else if (options.forWrite) {
        const parent = closestExistingParent(path.dirname(absPath));
        const realParent = maybeRealpath(parent);
        if (!realParent || !isSubpath(realParent, workspace.root)) {
          throw new CodexProError(`Path escapes workspace root: ${inputPath}`, { code: "path_outside_workspace", retryUnchanged: false });
        }
        absPath = path.resolve(realParent, path.relative(parent, absPath));
        relPath = displayPath(absPath, workspace.root);
      } else {
        throw new CodexProError(`Path escapes workspace root: ${inputPath}`, { code: "path_outside_workspace", retryUnchanged: false });
      }
    }

    this.assertNotBlocked(relPath);

    if (realTarget) {
      if (!isSubpath(realTarget, workspace.root)) {
        throw new CodexProError(`Path resolves outside workspace root through a symlink: ${inputPath}`, { code: "path_outside_workspace", retryUnchanged: false });
      }
      const realRel = displayPath(realTarget, workspace.root);
      this.assertNotBlocked(realRel);
    }

    if (options.forWrite) {
      try {
        if (fs.lstatSync(absPath).isSymbolicLink()) {
          throw new CodexProError(`Refusing to write through a symlink: ${inputPath}`, { code: "path_symlink_refused", retryUnchanged: false });
        }
      } catch (error) {
        if (error instanceof CodexProError) throw error;
      }
      const parent = closestExistingParent(path.dirname(absPath));
      const realParent = maybeRealpath(parent);
      if (realParent && !isSubpath(realParent, workspace.root)) {
        throw new CodexProError(`Write path resolves through a parent outside the workspace: ${inputPath}`, { code: "path_outside_workspace", retryUnchanged: false });
      }
      if (realParent) {
        const realParentRel = displayPath(realParent, workspace.root);
        this.assertNotBlocked(realParentRel);
      }
    }

    return { absPath, relPath };
  }

  async assertTextFile(absPath: string, maxBytes: number): Promise<void> {
    const stat = await fsp.stat(absPath);
    if (!stat.isFile()) {
      throw new CodexProError(`Not a file: ${absPath}`, { code: "path_not_file", retryUnchanged: false });
    }
    if (stat.size > maxBytes) {
      throw new CodexProError(`File is too large (${stat.size} bytes). Limit: ${maxBytes} bytes.`, { code: "file_too_large", retryUnchanged: false, recovery: { tool: "search", message: "Search for the relevant lines, or read a line range with start_line/end_line." } });
    }
    if (stat.size === 0) return;
    const handle = await fsp.open(absPath, "r");
    try {
      const sample = Buffer.alloc(Math.min(64 * 1024, stat.size));
      let offset = 0;
      while (offset < stat.size) {
        const { bytesRead } = await handle.read(sample, 0, sample.length, offset);
        if (bytesRead === 0) break;
        if (sample.subarray(0, bytesRead).includes(0)) {
          throw new CodexProError("Refusing to read binary file.", { code: "file_binary", retryUnchanged: false });
        }
        offset += bytesRead;
      }
    } finally {
      await handle.close();
    }
  }
}

export function userHome(): string {
  return os.homedir();
}
