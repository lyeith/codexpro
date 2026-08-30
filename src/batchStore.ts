import { spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import type { Dirent } from "node:fs";
import fsp from "node:fs/promises";
import path from "node:path";
import type { CodexProConfig } from "./config.js";
import { withFileWriteLocks, writeTextFile } from "./fsOps.js";
import { CodexProError, PathGuard, type Workspace } from "./guard.js";

export const BATCH_STORE_DIR = ".codexpro-batches";
export const BATCH_STORE_LIMIT = 20;
export const BATCH_DEFINITION_VERSION = 1 as const;

const AUTO_BATCH_FILE_PATTERN = /^[0-9A-F]{4}\.json$/i;

export interface StoredBatchOperation {
  id: string;
  tool: string;
  args: Record<string, unknown>;
}

export interface StoredBatchDefinition {
  version: typeof BATCH_DEFINITION_VERSION;
  mode: "serial" | "parallel";
  continue_on_error: boolean;
  operations: StoredBatchOperation[];
}

export interface MaterializedBatch {
  batchTag: string;
  path: string;
  definition: StoredBatchDefinition;
  prunedPaths: string[];
  gitExcluded: boolean;
}

export interface LoadedBatch {
  path: string;
  definition: unknown;
  autoStored: boolean;
}

export interface MaintainedBatch {
  prunedPaths: string[];
  gitExcluded: boolean;
}

interface StoredBatchFile {
  path: string;
  absPath: string;
  mtimeMs: number;
}

function normalizeSlash(value: string): string {
  return value.split(path.sep).join("/");
}

function parseGitPath(workspaceRoot: string, output: string): string {
  const value = output.trim();
  return path.isAbsolute(value) ? value : path.resolve(workspaceRoot, value);
}

async function ensureGitExclude(workspace: Workspace): Promise<boolean> {
  const topLevelResult = spawnSync("git", ["rev-parse", "--show-toplevel"], {
    cwd: workspace.root,
    encoding: "utf8",
    env: { ...process.env, GIT_TERMINAL_PROMPT: "0", NO_COLOR: "1" }
  });
  if (topLevelResult.error || topLevelResult.status !== 0) return false;

  const excludeResult = spawnSync("git", ["rev-parse", "--git-path", "info/exclude"], {
    cwd: workspace.root,
    encoding: "utf8",
    env: { ...process.env, GIT_TERMINAL_PROMPT: "0", NO_COLOR: "1" }
  });
  if (excludeResult.error || excludeResult.status !== 0) {
    throw new CodexProError(
      excludeResult.stderr?.trim() || excludeResult.stdout?.trim() || "Unable to locate Git's local exclude file."
    );
  }

  const gitTopLevel = path.resolve(topLevelResult.stdout.trim());
  const workspaceRel = normalizeSlash(path.relative(gitTopLevel, workspace.root));
  if (workspaceRel.startsWith("../") || workspaceRel === "..") {
    throw new CodexProError("Workspace root is outside the Git top-level directory while preparing batch-file exclusion.");
  }
  const excludeLine = `/${workspaceRel ? `${workspaceRel}/` : ""}${BATCH_STORE_DIR}/`;
  const excludePath = parseGitPath(workspace.root, excludeResult.stdout);

  await withFileWriteLocks([excludePath], async () => {
    await fsp.mkdir(path.dirname(excludePath), { recursive: true });
    let before = "";
    try {
      before = await fsp.readFile(excludePath, "utf8");
    } catch (error: any) {
      if (error?.code !== "ENOENT") throw error;
    }
    const lines = before.replace(/\r\n/g, "\n").split("\n");
    if (lines.includes(excludeLine)) return;
    const prefix = before.length && !before.endsWith("\n") ? "\n" : "";
    await fsp.appendFile(excludePath, `${prefix}${excludeLine}\n`, { encoding: "utf8", mode: 0o600 });
  });
  return true;
}

function isAutoStoredPath(relPath: string): boolean {
  const normalized = normalizeSlash(relPath).replace(/^\.\//, "");
  if (!normalized.startsWith(`${BATCH_STORE_DIR}/`)) return false;
  const fileName = normalized.slice(BATCH_STORE_DIR.length + 1);
  return AUTO_BATCH_FILE_PATTERN.test(fileName);
}

async function batchFiles(guard: PathGuard, workspace: Workspace): Promise<StoredBatchFile[]> {
  const directory = guard.resolve(workspace, BATCH_STORE_DIR, { forWrite: true });
  let entries: Dirent[];
  try {
    entries = await fsp.readdir(directory.absPath, { withFileTypes: true });
  } catch (error: any) {
    if (error?.code === "ENOENT") return [];
    throw error;
  }

  const files: StoredBatchFile[] = [];
  for (const entry of entries) {
    if (!entry.isFile() || !AUTO_BATCH_FILE_PATTERN.test(entry.name)) continue;
    const relPath = `${BATCH_STORE_DIR}/${entry.name}`;
    const resolved = guard.resolve(workspace, relPath, { forWrite: true });
    const stat = await fsp.stat(resolved.absPath);
    files.push({ path: resolved.relPath, absPath: resolved.absPath, mtimeMs: stat.mtimeMs });
  }
  return files;
}

function retainedBatchPaths(files: StoredBatchFile[], keepPath?: string): Set<string> {
  const ordered = [...files].sort(
    (left, right) => right.mtimeMs - left.mtimeMs || right.path.localeCompare(left.path)
  );
  const retained = new Set<string>();
  if (keepPath) retained.add(normalizeSlash(keepPath));
  for (const file of ordered) {
    if (retained.size >= BATCH_STORE_LIMIT) break;
    retained.add(normalizeSlash(file.path));
  }
  return retained;
}

async function pruneStoredBatches(
  guard: PathGuard,
  workspace: Workspace,
  keepPath?: string
): Promise<string[]> {
  const files = await batchFiles(guard, workspace);
  const retained = retainedBatchPaths(files, keepPath);
  const candidates = files.filter((file) => !retained.has(normalizeSlash(file.path)));
  if (!candidates.length) return [];

  return withFileWriteLocks(candidates.map((file) => file.absPath), async () => {
    // A normal tagged edit can update an older definition while retention waits
    // for its file lock. Recompute under those locks and delete only candidates
    // that are still outside the newest set. Newly stale unlocked files wait for
    // the next retention pass rather than being removed without their lock.
    const currentFiles = await batchFiles(guard, workspace);
    const currentPaths = new Set(currentFiles.map((file) => normalizeSlash(file.path)));
    const currentRetained = retainedBatchPaths(currentFiles, keepPath);
    const removed: string[] = [];
    for (const file of candidates) {
      const normalized = normalizeSlash(file.path);
      if (!currentPaths.has(normalized) || currentRetained.has(normalized)) continue;
      try {
        await fsp.unlink(file.absPath);
        removed.push(file.path);
      } catch (error: any) {
        if (error?.code !== "ENOENT") throw error;
      }
    }
    return removed;
  });
}

function serializeDefinition(definition: StoredBatchDefinition): string {
  return `${JSON.stringify(definition, null, 2)}\n`;
}

async function generateBatchPath(guard: PathGuard, workspace: Workspace): Promise<{ batchTag: string; path: string }> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const batchTag = randomBytes(2).toString("hex").toUpperCase();
    const relPath = `${BATCH_STORE_DIR}/${batchTag}.json`;
    const resolved = guard.resolve(workspace, relPath, { forWrite: true });
    try {
      await fsp.access(resolved.absPath);
    } catch (error: any) {
      if (error?.code === "ENOENT") return { batchTag, path: resolved.relPath };
      throw error;
    }
  }
  throw new CodexProError("Unable to allocate a unique four-hex batch file name. Remove old batch files and retry.");
}

export async function materializeBatchDefinition(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  definition: StoredBatchDefinition
): Promise<MaterializedBatch> {
  const directory = guard.resolve(workspace, BATCH_STORE_DIR, { forWrite: true });
  return withFileWriteLocks([directory.absPath], async () => {
    const gitExcluded = await ensureGitExclude(workspace);
    const allocated = await generateBatchPath(guard, workspace);
    await writeTextFile(config, guard, workspace, allocated.path, serializeDefinition(definition), {
      createDirs: true,
      overwrite: false
    });
    const resolved = guard.resolve(workspace, allocated.path, { forWrite: true });
    await fsp.chmod(resolved.absPath, 0o600);
    const prunedPaths = await pruneStoredBatches(guard, workspace, allocated.path);
    return {
      batchTag: allocated.batchTag,
      path: allocated.path,
      definition,
      prunedPaths,
      gitExcluded
    };
  });
}

export async function loadBatchDefinition(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  filePath: string
): Promise<LoadedBatch> {
  if (!filePath?.trim()) throw new CodexProError("batch path must not be empty.");
  const resolved = guard.resolve(workspace, filePath);
  if (!resolved.relPath.toLowerCase().endsWith(".json")) {
    throw new CodexProError(`Batch definition must be a .json file: ${resolved.relPath}`);
  }
  await guard.assertTextFile(resolved.absPath, Math.max(config.maxReadBytes, config.maxWriteBytes));
  const text = await fsp.readFile(resolved.absPath, "utf8");
  let definition: unknown;
  try {
    definition = JSON.parse(text);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    throw new CodexProError(`Invalid batch JSON in ${resolved.relPath}: ${message}`);
  }

  return {
    path: resolved.relPath,
    definition,
    autoStored: isAutoStoredPath(resolved.relPath)
  };
}

export async function maintainLoadedBatchDefinition(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  filePath: string
): Promise<MaintainedBatch> {
  if (config.writeMode !== "workspace" || config.connectionTest) {
    return { prunedPaths: [], gitExcluded: false };
  }
  const resolved = guard.resolve(workspace, filePath, { forWrite: true });
  if (!isAutoStoredPath(resolved.relPath)) {
    return { prunedPaths: [], gitExcluded: false };
  }
  const directory = guard.resolve(workspace, BATCH_STORE_DIR, { forWrite: true });
  let gitExcluded = false;
  let prunedPaths: string[] = [];
  await withFileWriteLocks([directory.absPath, resolved.absPath], async () => {
    gitExcluded = await ensureGitExclude(workspace);
    const now = new Date();
    await fsp.utimes(resolved.absPath, now, now);
    prunedPaths = await pruneStoredBatches(guard, workspace, resolved.relPath);
  });
  return { prunedPaths, gitExcluded };
}
