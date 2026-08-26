import { randomBytes } from "node:crypto";
import { spawn } from "node:child_process";
import fs from "node:fs";
import fsp from "node:fs/promises";
import type { FileHandle } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import type { CodexProConfig } from "../config.js";
import { expandHome } from "../config.js";
import { CodexProError, isSubpath } from "../guard.js";
import { terminateProcessTree } from "../processOps.js";
import { KeyedMutex } from "../worktrees/keyedMutex.js";
import {
  MAX_CATALOG_BYTES,
  MAX_PROJECTS,
  loadProjectCatalog,
  projectBaseRefFrom,
  projectIdFrom,
  projectLabelFrom,
  projectMaxWorktreesFrom
} from "./catalog.js";
import type { ProjectCreationRoot, ProjectDefinition, ProjectSummary } from "./types.js";

export type ProjectSource = "empty" | "git";

export interface CreateProjectOptions {
  projectId: string;
  parentId: string;
  label?: string;
  directory?: string;
  source: ProjectSource;
  repository?: string;
  initialBranch?: string;
  baseRef?: string;
  maxWorktrees?: number;
}

export interface CreateProjectResult {
  project: ProjectDefinition;
  summary: ProjectSummary;
  source: ProjectSource;
  cloned: boolean;
  gitInitialized: boolean;
  initialCommitCreated: boolean;
}

interface GitResult {
  stdout: string;
  stderr: string;
}

const projectCreationMutex = new KeyedMutex();
const WINDOWS_RESERVED_NAME = /^(?:con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)/i;
const SCP_REPOSITORY = /^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._~/-]+$/;

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function directoryNameFrom(value: unknown, projectId: string): string {
  const name = value === undefined ? projectId : String(value).trim();
  if (
    !name ||
    name.length > 120 ||
    name === "." ||
    name === ".." ||
    WINDOWS_RESERVED_NAME.test(name) ||
    /[<>:"/\\|?*\0-\x1f\x7f]/.test(name) ||
    /[. ]$/.test(name)
  ) {
    throw new CodexProError(
      "directory must be one portable child-directory name of 1-120 characters without path separators, control characters, reserved device names, or a trailing dot/space."
    );
  }
  return name;
}

function initialBranchFrom(value: unknown): string {
  const branch = value === undefined ? "main" : String(value).trim();
  if (!branch || branch.length > 255 || branch.startsWith("-") || /[\0-\x20\x7f]/.test(branch)) {
    throw new CodexProError(
      "initial_branch must be a non-empty Git branch name without whitespace, control characters, or a leading dash."
    );
  }
  return branch;
}

function isPathLikeRepository(value: string): boolean {
  return (
    value === "~" ||
    value.startsWith("~/") ||
    value.startsWith("./") ||
    value.startsWith("../") ||
    path.isAbsolute(value) ||
    path.win32.isAbsolute(value)
  );
}

function localRepository(value: string, parentRoot: string, config: CodexProConfig): string {
  const expanded = expandHome(value);
  const resolved = path.resolve(path.isAbsolute(expanded) || path.win32.isAbsolute(expanded) ? expanded : path.join(parentRoot, expanded));
  if (!fs.existsSync(resolved)) throw new CodexProError(`Local Git repository does not exist: ${resolved}`);
  if (!fs.statSync(resolved).isDirectory()) throw new CodexProError(`Local Git repository is not a directory: ${resolved}`);
  const canonical = fs.realpathSync.native(resolved);
  const creationParents = config.projectCreationRoots.map((creationRoot) => creationRoot.root);
  if (![...config.allowedRoots, ...creationParents].some((allowedRoot) => isSubpath(canonical, allowedRoot))) {
    throw new CodexProError("Local Git repositories must stay inside an allowed project or creation root.");
  }
  return canonical;
}

function repositoryFrom(value: unknown, parentRoot: string, config: CodexProConfig): string | undefined {
  if (value === undefined) return undefined;
  if (typeof value !== "string") throw new CodexProError("repository must be a string.");
  const repository = value.trim();
  if (!repository || repository.length > 2_048 || /[\0-\x1f\x7f]/.test(repository)) {
    throw new CodexProError(
      "repository must be a non-empty Git HTTPS/SSH URL or allowed local path without control characters."
    );
  }
  if (isPathLikeRepository(repository)) return localRepository(repository, parentRoot, config);
  if (repository.startsWith("-") || /\s/.test(repository)) {
    throw new CodexProError("Remote repository URLs must not contain whitespace or begin with a dash.");
  }
  if (SCP_REPOSITORY.test(repository)) return repository;

  let parsed: URL;
  try {
    parsed = new URL(repository);
  } catch {
    throw new CodexProError("repository must use https://, ssh://, Git scp syntax, or an allowed local path.");
  }
  if (parsed.protocol !== "https:" && parsed.protocol !== "ssh:") {
    throw new CodexProError("repository URLs must use HTTPS or SSH.");
  }
  if (parsed.password || (parsed.protocol === "https:" && parsed.username)) {
    throw new CodexProError("repository URLs must not embed credentials.");
  }
  if (parsed.search || parsed.hash) {
    throw new CodexProError("repository URLs must not contain query parameters or fragments.");
  }
  return repository;
}

function canonicalTarget(parentRoot: string, directory: string): string {
  const target = path.resolve(parentRoot, directory);
  if (path.dirname(target) !== parentRoot) {
    throw new CodexProError("Project directory must be a direct child of the selected parent project.");
  }
  return target;
}

function sameProject(left: ProjectDefinition, right: ProjectDefinition): boolean {
  return (
    left.id === right.id &&
    left.label === right.label &&
    left.root === right.root &&
    left.baseRef === right.baseRef &&
    left.maxWorktrees === right.maxWorktrees
  );
}

function sameCreationRoot(left: ProjectCreationRoot, right: ProjectCreationRoot): boolean {
  return left.id === right.id && left.label === right.label && left.root === right.root;
}

function assertCatalogMatchesRuntime(config: CodexProConfig): string {
  if (!config.projectsFile) throw new CodexProError("create_project requires a persistent projects file.");
  const before = fs.readFileSync(config.projectsFile, "utf8");
  const catalog = loadProjectCatalog(config.projectsFile);
  const after = fs.readFileSync(config.projectsFile, "utf8");
  if (
    before !== after ||
    catalog.defaultProjectId !== config.defaultProjectId ||
    catalog.projects.length !== config.projects.length ||
    catalog.projects.some((project, index) => !sameProject(project, config.projects[index])) ||
    catalog.creationRoots.length !== config.projectCreationRoots.length ||
    catalog.creationRoots.some((creationRoot, index) => !sameCreationRoot(creationRoot, config.projectCreationRoots[index]))
  ) {
    throw new CodexProError(
      "The projects file changed after this CodexPro server started. Restart CodexPro before creating another project so runtime permissions cannot overwrite external catalog edits."
    );
  }
  if (catalog.projects.length >= MAX_PROJECTS) {
    throw new CodexProError(`The projects file already contains the maximum of ${MAX_PROJECTS} projects.`);
  }
  return after;
}

async function pathExists(input: string): Promise<boolean> {
  try {
    await fsp.lstat(input);
    return true;
  } catch (error) {
    if (error && typeof error === "object" && "code" in error && error.code === "ENOENT") return false;
    throw error;
  }
}

async function syncDirectory(directory: string): Promise<void> {
  let handle: FileHandle | undefined;
  try {
    handle = await fsp.open(directory, "r");
    await handle.sync();
  } catch (error) {
    const code = error && typeof error === "object" && "code" in error ? String(error.code) : "";
    if (!["EINVAL", "ENOTSUP", "EPERM", "EISDIR"].includes(code)) throw error;
  } finally {
    await handle?.close().catch(() => undefined);
  }
}

async function atomicReplaceCatalog(filePath: string, content: string, expectedCurrent?: string): Promise<void> {
  if (Buffer.byteLength(content, "utf8") > MAX_CATALOG_BYTES) {
    throw new CodexProError(`Projects file would exceed ${MAX_CATALOG_BYTES} bytes.`);
  }
  if (expectedCurrent !== undefined && await fsp.readFile(filePath, "utf8") !== expectedCurrent) {
    throw new CodexProError("The projects file changed concurrently; no catalog update was applied.");
  }
  const directory = path.dirname(filePath);
  const stat = await fsp.stat(filePath);
  const temporary = path.join(
    directory,
    `.${path.basename(filePath)}.codexpro-${process.pid}-${randomBytes(8).toString("hex")}.tmp`
  );
  let handle: FileHandle | undefined;
  try {
    handle = await fsp.open(temporary, "wx", stat.mode & 0o777);
    await handle.writeFile(content, "utf8");
    await handle.sync();
    await handle.close();
    handle = undefined;
    loadProjectCatalog(temporary);
    if (expectedCurrent !== undefined && await fsp.readFile(filePath, "utf8") !== expectedCurrent) {
      throw new CodexProError("The projects file changed concurrently; no catalog update was applied.");
    }
    await fsp.rename(temporary, filePath);
    // The rename is the commit point. Directory fsync is best effort on platforms/filesystems
    // that support it; failure must not make callers delete a project the catalog now names.
    await syncDirectory(directory).catch(() => undefined);
  } catch (error) {
    await handle?.close().catch(() => undefined);
    await fsp.rm(temporary, { force: true }).catch(() => undefined);
    throw error;
  }
}

function projectDocumentEntry(project: ProjectDefinition): Record<string, unknown> {
  return {
    id: project.id,
    label: project.label,
    root: project.root,
    ...(project.baseRef === undefined ? {} : { baseRef: project.baseRef }),
    ...(project.maxWorktrees === undefined ? {} : { maxWorktrees: project.maxWorktrees })
  };
}

async function appendCatalogProject(
  filePath: string,
  project: ProjectDefinition,
  expectedPrevious: string
): Promise<{ previous: string; written: string }> {
  const previous = await fsp.readFile(filePath, "utf8");
  if (previous !== expectedPrevious) {
    throw new CodexProError("The projects file changed concurrently; no project was registered.");
  }
  const parsed = JSON.parse(previous) as { projects?: unknown[] };
  if (!Array.isArray(parsed.projects)) throw new CodexProError("Projects file has no projects array.");
  parsed.projects.push(projectDocumentEntry(project));
  const written = `${JSON.stringify(parsed, null, 2)}\n`;
  await atomicReplaceCatalog(filePath, written, previous);
  return { previous, written };
}

async function restoreCatalog(filePath: string, expected: string, previous: string): Promise<void> {
  const current = await fsp.readFile(filePath, "utf8");
  if (current !== expected) {
    throw new CodexProError(
      "The projects file changed while create_project was registering the runtime project; automatic rollback refused to overwrite the newer catalog."
    );
  }
  await atomicReplaceCatalog(filePath, previous, expected);
}

async function runGit(config: CodexProConfig, cwd: string, args: string[]): Promise<GitResult> {
  return new Promise((resolve, reject) => {
    const child = spawn("git", args, {
      cwd,
      shell: false,
      env: { ...process.env, NO_COLOR: "1", GIT_TERMINAL_PROMPT: "0" },
      stdio: ["ignore", "pipe", "pipe"],
      detached: process.platform !== "win32",
      windowsHide: true
    });
    let stdout = "";
    let stderr = "";
    let observedOutputBytes = 0;
    let overflow = false;
    let timedOut = false;
    let settled = false;
    let closed = false;
    let terminationStarted = false;
    let timer: NodeJS.Timeout | undefined;
    let killTimer: NodeJS.Timeout | undefined;
    const clearTimers = () => {
      if (timer) clearTimeout(timer);
      if (killTimer) clearTimeout(killTimer);
    };
    const finishReject = (error: Error) => {
      if (settled) return;
      settled = true;
      clearTimers();
      reject(error);
    };
    const terminateWithEscalation = () => {
      if (terminationStarted || closed) return;
      terminationStarted = true;
      terminateProcessTree(child, "SIGTERM");
      killTimer = setTimeout(() => {
        if (!closed) terminateProcessTree(child, "SIGKILL");
      }, 1_500);
      killTimer.unref();
    };
    timer = setTimeout(() => {
      timedOut = true;
      terminateWithEscalation();
    }, config.maxBashTimeoutMs);
    timer.unref();
    const collect = (target: "stdout" | "stderr", chunk: unknown) => {
      const text = String(chunk);
      observedOutputBytes += Buffer.byteLength(text, "utf8");
      if (observedOutputBytes > config.maxOutputBytes) {
        overflow = true;
        terminateWithEscalation();
        return;
      }
      if (target === "stdout") stdout += text;
      else stderr += text;
    };
    child.stdout.on("data", (chunk) => collect("stdout", chunk));
    child.stderr.on("data", (chunk) => collect("stderr", chunk));
    child.on("error", (error) => finishReject(new CodexProError(`Could not run git: ${error.message}`)));
    child.on("close", (code) => {
      closed = true;
      clearTimers();
      if (settled) return;
      settled = true;
      if (timedOut) {
        reject(new CodexProError(`Git command timed out after ${config.maxBashTimeoutMs} ms.`));
        return;
      }
      if (overflow) {
        reject(new CodexProError(`Git output exceeded ${config.maxOutputBytes} bytes.`));
        return;
      }
      if (code !== 0) {
        reject(new CodexProError((stderr || stdout || `git exited with status ${code}`).trim()));
        return;
      }
      resolve({ stdout, stderr });
    });
  });
}

async function withEmptyGitTemplate<T>(operation: (directory: string) => Promise<T>): Promise<T> {
  const directory = await fsp.mkdtemp(path.join(os.tmpdir(), "codexpro-git-template-"));
  try {
    return await operation(directory);
  } finally {
    await fsp.rm(directory, { recursive: true, force: true }).catch(() => undefined);
  }
}

async function initializeGitProject(
  config: CodexProConfig,
  parentRoot: string,
  target: string,
  branch: string
): Promise<void> {
  await runGit(config, parentRoot, ["check-ref-format", "--branch", branch]);
  await withEmptyGitTemplate(async (templateDirectory) => {
    const hooksConfig = `core.hooksPath=${templateDirectory}`;
    await runGit(config, parentRoot, [
      "-c",
      hooksConfig,
      "init",
      `--template=${templateDirectory}`,
      `--initial-branch=${branch}`,
      "--",
      target
    ]);
    await runGit(config, target, [
      "-c",
      hooksConfig,
      "-c",
      "commit.gpgSign=false",
      "-c",
      "user.name=CodexPro",
      "-c",
      "user.email=codexpro@localhost",
      "commit",
      "--allow-empty",
      "--no-verify",
      "-m",
      "Initial commit"
    ]);
  });
}

async function cloneGitProject(
  config: CodexProConfig,
  parentRoot: string,
  repository: string,
  target: string
): Promise<void> {
  await withEmptyGitTemplate(async (templateDirectory) => {
    await runGit(config, parentRoot, [
      "-c",
      `core.hooksPath=${templateDirectory}`,
      "clone",
      `--template=${templateDirectory}`,
      "--no-hardlinks",
      "--",
      repository,
      target
    ]);
  });
}

export async function createCatalogProject(
  config: CodexProConfig,
  options: CreateProjectOptions,
  registerProject: (project: ProjectDefinition) => Promise<ProjectSummary>
): Promise<CreateProjectResult> {
  const projectsFile = config.projectsFile;
  if (!projectsFile) {
    throw new CodexProError(
      "create_project is available only when CodexPro was started with --projects-file or CODEXPRO_PROJECTS_FILE."
    );
  }

  return projectCreationMutex.runExclusive(projectsFile, async () => {
    const catalogSnapshot = assertCatalogMatchesRuntime(config);
    const projectId = projectIdFrom(options.projectId);
    const parent = config.projectCreationRoots.find((creationRoot) => creationRoot.id === options.parentId)
      ?? config.projects.find((project) => project.id === options.parentId);
    if (!parent) {
      throw new CodexProError(`Unknown parent_id: ${options.parentId}. Call list_projects first.`);
    }
    if (
      config.projects.some((project) => project.id === projectId) ||
      config.projectCreationRoots.some((creationRoot) => creationRoot.id === projectId)
    ) {
      throw new CodexProError(`Project or creation-root id already exists: ${projectId}`);
    }
    if (options.source !== "empty" && options.source !== "git") {
      throw new CodexProError("source must be empty or git.");
    }
    if (config.worktreeMode === "mcp" && options.source === "empty") {
      throw new CodexProError(
        "Raw empty projects cannot be added while MCP worktree mode is enabled. Use source=git so CodexPro can create isolated worktrees."
      );
    }
    if (options.source === "empty" && options.repository !== undefined) {
      throw new CodexProError("repository is valid only when source=git.");
    }
    if (options.source === "empty" && options.initialBranch !== undefined) {
      throw new CodexProError("initial_branch is valid only when source=git and repository is omitted.");
    }
    if (options.source === "empty" && options.baseRef !== undefined) {
      throw new CodexProError("base_ref is valid only when source=git.");
    }

    const directory = directoryNameFrom(options.directory, projectId);
    const canonicalParent = fs.realpathSync.native(parent.root);
    if (canonicalParent !== parent.root) {
      throw new CodexProError("The selected parent root changed after server startup; restart CodexPro.");
    }
    const target = canonicalTarget(canonicalParent, directory);
    if (await pathExists(target)) throw new CodexProError(`Project directory already exists: ${target}`);
    if (config.projects.some((project) => path.resolve(project.root) === target)) {
      throw new CodexProError("Another configured project already uses that directory.");
    }

    const baseRef = projectBaseRefFrom(options.baseRef);
    const maxWorktrees = projectMaxWorktreesFrom(options.maxWorktrees);
    const label = projectLabelFrom(options.label, projectId);
    const repository = options.source === "git"
      ? repositoryFrom(options.repository, canonicalParent, config)
      : undefined;
    const branch = repository === undefined && options.source === "git"
      ? initialBranchFrom(options.initialBranch)
      : undefined;
    if (repository !== undefined && options.initialBranch !== undefined) {
      throw new CodexProError("initial_branch cannot be combined with repository; the cloned repository selects its own checkout.");
    }

    let targetOwned = false;
    let catalogMutation: { previous: string; written: string } | undefined;
    try {
      await fsp.mkdir(target, { mode: 0o755 });
      targetOwned = true;
      const createdRoot = fs.realpathSync.native(target);
      if (createdRoot !== target) throw new CodexProError("Created project directory resolved through an unexpected symbolic link.");
      if (options.source === "git") {
        if (repository) await cloneGitProject(config, canonicalParent, repository, target);
        else await initializeGitProject(config, canonicalParent, target, branch ?? "main");
      }

      const root = fs.realpathSync.native(target);
      if (root !== createdRoot) throw new CodexProError("Created project directory changed identity during initialization.");
      const project: ProjectDefinition = { id: projectId, label, root, baseRef, maxWorktrees };
      if (options.source === "git") {
        const effectiveRef = baseRef ?? (config.worktreeMode === "mcp" ? config.worktreeBaseRef : "HEAD");
        await runGit(config, root, ["rev-parse", "--verify", `${effectiveRef}^{commit}`]);
      }

      catalogMutation = await appendCatalogProject(projectsFile, project, catalogSnapshot);
      let summary: ProjectSummary;
      try {
        summary = await registerProject(project);
      } catch (registrationError) {
        try {
          await restoreCatalog(projectsFile, catalogMutation.written, catalogMutation.previous);
          catalogMutation = undefined;
        } catch (rollbackError) {
          targetOwned = false;
          throw new CodexProError(
            `Project runtime registration failed (${errorMessage(registrationError)}), and catalog rollback also failed (${errorMessage(rollbackError)}). The project directory was preserved so the catalog does not point at missing data.`
          );
        }
        throw registrationError;
      }

      targetOwned = false;
      return {
        project,
        summary,
        source: options.source,
        cloned: Boolean(repository),
        gitInitialized: options.source === "git",
        initialCommitCreated: options.source === "git" && !repository
      };
    } catch (error) {
      if (catalogMutation) {
        try {
          await restoreCatalog(projectsFile, catalogMutation.written, catalogMutation.previous);
          catalogMutation = undefined;
        } catch (rollbackError) {
          targetOwned = false;
          throw new CodexProError(
            `create_project failed (${errorMessage(error)}), and catalog rollback also failed (${errorMessage(rollbackError)}). The project directory was preserved for manual recovery.`
          );
        }
      }
      if (targetOwned) await fsp.rm(target, { recursive: true, force: true }).catch(() => undefined);
      throw error;
    }
  });
}
