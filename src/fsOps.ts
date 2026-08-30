import fs from "node:fs";
import fsp from "node:fs/promises";
import path from "node:path";
import { createHash, randomBytes } from "node:crypto";
import { minimatch } from "minimatch";
import type { CodexProConfig } from "./config.js";
import type { Workspace } from "./guard.js";
import { CodexProError, displayPath, normalizeRelPath, PathGuard } from "./guard.js";
import { hasSecretValue, introducesSecretValue, redactSensitiveText } from "./redact.js";

export interface TreeOptions {
  path?: string;
  maxDepth: number;
  includeHidden: boolean;
  maxEntries: number;
}

export interface TreeResult {
  text: string;
  entries: number;
  truncated: boolean;
}

export interface ReadFileResult {
  path: string;
  text: string;
  startLine: number;
  endLine: number;
  totalLines: number;
  bytes: number;
  sha256: string;
  editTag: string;
  truncated: boolean;
}

export interface DiffResult {
  diff: string;
  additions: number;
  deletions: number;
  changed: boolean;
}

export type AnchoredLineEdit =
  | { op: "replace"; startLine: number; endLine?: number; content: string }
  | { op: "delete"; startLine: number; endLine?: number }
  | { op: "insert_before" | "insert_after"; line: number; content: string };

export interface AnchoredEditResult {
  path: string;
  edits: number;
  bytes: number;
  baseTag: string;
  editTag: string;
  diff: DiffResult;
}

export function sha256(text: string): string {
  return createHash("sha256").update(text).digest("hex");
}

export function computeEditTag(text: string): string {
  return sha256(text).slice(0, 4).toUpperCase();
}

interface SeenLineRange {
  start: number;
  end: number;
}

interface EditSnapshot {
  tag: string;
  text: string;
  bytes: number;
  seenRanges: SeenLineRange[];
  recordedAt: number;
}

const EDIT_TAG_PATTERN = /^[0-9A-F]{4}$/;
const MAX_EDIT_SNAPSHOT_PATHS = 256;
const MAX_EDIT_SNAPSHOT_VERSIONS = 4;
const MAX_EDIT_SNAPSHOT_BYTES = 64 * 1024 * 1024;

function editSnapshotKey(absPath: string): string {
  try {
    return normalizeLockKey(fs.realpathSync.native(absPath));
  } catch {
    return normalizeLockKey(path.resolve(absPath));
  }
}

function editHistoryBytes(history: EditSnapshot[]): number {
  return history.reduce((total, snapshot) => total + snapshot.bytes, 0);
}

function mergeSeenRange(ranges: SeenLineRange[], start: number, end: number): SeenLineRange[] {
  const ordered = [...ranges, { start, end }].sort((left, right) => left.start - right.start || left.end - right.end);
  const merged: SeenLineRange[] = [];
  for (const range of ordered) {
    const previous = merged[merged.length - 1];
    if (!previous || range.start > previous.end + 1) {
      merged.push({ ...range });
      continue;
    }
    previous.end = Math.max(previous.end, range.end);
  }
  return merged;
}

export class EditSnapshotStore {
  private readonly histories = new Map<string, EditSnapshot[]>();
  private retainedBytes = 0;

  private replaceHistory(key: string, history: EditSnapshot[]): void {
    const previous = this.histories.get(key);
    if (previous) this.retainedBytes -= editHistoryBytes(previous);
    this.histories.delete(key);
    if (history.length) {
      this.histories.set(key, history);
      this.retainedBytes += editHistoryBytes(history);
    }

    while (
      this.histories.size > MAX_EDIT_SNAPSHOT_PATHS ||
      this.retainedBytes > MAX_EDIT_SNAPSHOT_BYTES
    ) {
      const oldestKey = this.histories.keys().next().value as string | undefined;
      if (!oldestKey) break;
      const oldest = this.histories.get(oldestKey) ?? [];
      this.retainedBytes -= editHistoryBytes(oldest);
      this.histories.delete(oldestKey);
    }
  }

  record(absPath: string, text: string, seenRange?: SeenLineRange): EditSnapshot {
    const key = editSnapshotKey(absPath);
    const history = this.histories.get(key) ?? [];
    const existing = history.find((snapshot) => snapshot.text === text);
    if (existing) {
      existing.recordedAt = Date.now();
      if (seenRange) existing.seenRanges = mergeSeenRange(existing.seenRanges, seenRange.start, seenRange.end);
      this.replaceHistory(key, [
        existing,
        ...history.filter((snapshot) => snapshot !== existing && snapshot.tag !== existing.tag)
      ]);
      return existing;
    }

    const snapshot: EditSnapshot = {
      tag: computeEditTag(text),
      text,
      bytes: Buffer.byteLength(text, "utf8"),
      seenRanges: seenRange ? [{ ...seenRange }] : [],
      recordedAt: Date.now()
    };
    const nonCollidingHistory = history.filter((version) => version.tag !== snapshot.tag);
    this.replaceHistory(key, [snapshot, ...nonCollidingHistory].slice(0, MAX_EDIT_SNAPSHOT_VERSIONS));
    return snapshot;
  }

  resolve(absPath: string, suppliedTag: string, liveText: string, relPath: string): EditSnapshot {
    const tag = suppliedTag.trim().toUpperCase();
    if (!EDIT_TAG_PATTERN.test(tag)) {
      throw new CodexProError("edit_tag must be exactly four hexadecimal characters from the latest read result.");
    }

    const key = editSnapshotKey(absPath);
    const history = this.histories.get(key) ?? [];
    const tagged = history.filter((snapshot) => snapshot.tag === tag);
    if (!tagged.length) {
      throw new CodexProError(
        `Edit tag ${tag} for ${relPath} is not retained by this MCP session. Read the file again and use the returned edit_tag.`
      );
    }

    const exact = tagged.find((snapshot) => snapshot.text === liveText);
    if (!exact) {
      throw new CodexProError(
        `File changed since edit tag ${tag} was read: ${relPath}. Read the file again before editing.`
      );
    }

    this.replaceHistory(key, [exact, ...history.filter((snapshot) => snapshot !== exact)]);
    return exact;
  }
}

function assertSnapshotRangeSeen(
  snapshot: EditSnapshot,
  start: number,
  end: number,
  operation: number,
  relPath: string
): void {
  if (snapshot.seenRanges.some((range) => start >= range.start && end <= range.end)) return;
  const target = start === end ? `line ${start}` : `lines ${start}-${end}`;
  throw new CodexProError(
    `edits[${operation}] targets ${target} in ${relPath}, but that range was not displayed for edit tag ${snapshot.tag}. Read that range and retry.`
  );
}

const fileWriteLocks = new Map<string, Promise<void>>();

function normalizeLockKey(absPath: string): string {
  const normalized = path.normalize(absPath);
  return process.platform === "win32" ? normalized.toLowerCase() : normalized;
}

async function canonicalWriteKey(absPath: string): Promise<string> {
  try {
    return normalizeLockKey(await fsp.realpath(absPath));
  } catch {}

  let current = path.dirname(absPath);
  const suffix = [path.basename(absPath)];
  while (path.dirname(current) !== current) {
    try {
      return normalizeLockKey(path.join(await fsp.realpath(current), ...suffix));
    } catch {
      suffix.unshift(path.basename(current));
      current = path.dirname(current);
    }
  }
  return normalizeLockKey(path.resolve(absPath));
}

async function acquireFileWriteLock(absPath: string): Promise<() => void> {
  const key = await canonicalWriteKey(absPath);
  const previous = fileWriteLocks.get(key) ?? Promise.resolve();
  let releaseCurrent!: () => void;
  const current = new Promise<void>((resolve) => {
    releaseCurrent = resolve;
  });
  fileWriteLocks.set(key, current);
  await previous;
  return () => {
    releaseCurrent();
    if (fileWriteLocks.get(key) === current) fileWriteLocks.delete(key);
  };
}

export async function withFileWriteLocks<T>(absPaths: string[], task: () => Promise<T> | T): Promise<T> {
  const releases: Array<() => void> = [];
  const orderedPaths = [...new Set(absPaths)].sort((left, right) => left.localeCompare(right));
  try {
    for (const absPath of orderedPaths) {
      releases.push(await acquireFileWriteLock(absPath));
    }
    return await task();
  } finally {
    for (const release of releases.reverse()) release();
  }
}

async function writeText(absPath: string, content: string, existingText?: string, relPath = path.basename(absPath)): Promise<void> {
  if (existingText !== undefined) {
    const handle = await fsp.open(absPath, "r+");
    try {
      const currentText = await handle.readFile("utf8");
      if (currentText !== existingText) {
        throw new CodexProError(`File changed during write: ${relPath}. Read the file again before writing.`);
      }
      const buffer = Buffer.from(content, "utf8");
      await handle.truncate(0);
      let offset = 0;
      while (offset < buffer.length) {
        const { bytesWritten } = await handle.write(buffer, offset, buffer.length - offset, offset);
        if (bytesWritten === 0) {
          throw new CodexProError(`Write made no progress: ${relPath}.`);
        }
        offset += bytesWritten;
      }
      await handle.sync();
    } finally {
      await handle.close();
    }
    return;
  }

  const parent = path.dirname(absPath);
  const basename = path.basename(absPath);
  const tempPath = path.join(parent, `.${basename}.codexpro-${process.pid}-${randomBytes(6).toString("hex")}.tmp`);
  let handle: fsp.FileHandle | undefined;
  try {
    handle = await fsp.open(tempPath, "wx", 0o666);
    await handle.writeFile(content, "utf8");
    await handle.sync();
    await handle.close();
    handle = undefined;
    await fsp.rename(tempPath, absPath);
  } catch (error) {
    try {
      await handle?.close();
    } catch {}
    try {
      await fsp.unlink(tempPath);
    } catch {}
    throw error;
  }
}

function assertExpectedSha(expectedSha256: string | undefined, actualText: string, relPath: string): void {
  if (!expectedSha256) return;
  const actualSha256 = sha256(actualText);
  if (actualSha256.toLowerCase() !== expectedSha256.toLowerCase()) {
    throw new CodexProError(
      `File changed since it was read: ${relPath}. Expected SHA-256 ${expectedSha256}, found ${actualSha256}. Read the file again before writing.`
    );
  }
}

// ponytail: bounded scan window covers normal source files over the read cap; add a separate knob only if real repos need larger files.
export function textScanByteLimit(config: CodexProConfig): number {
  return Math.min(2_000_000, config.maxReadBytes * 4);
}

function splitLines(text: string): string[] {
  return text.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n");
}

function withLineNumbers(lines: string[], startLine: number): string {
  const width = String(startLine + lines.length - 1).length;
  return lines.map((line, idx) => `${String(startLine + idx).padStart(width, " ")} | ${line}`).join("\n");
}

export function makeUnifiedDiff(oldText: string, newText: string, relPath: string, maxChars = 60_000): DiffResult {
  if (oldText === newText) {
    return { diff: `No changes in ${relPath}.`, additions: 0, deletions: 0, changed: false };
  }

  const oldLines = splitLines(oldText);
  const newLines = splitLines(newText);
  let prefix = 0;
  while (prefix < oldLines.length && prefix < newLines.length && oldLines[prefix] === newLines[prefix]) {
    prefix += 1;
  }

  let suffix = 0;
  while (
    suffix < oldLines.length - prefix &&
    suffix < newLines.length - prefix &&
    oldLines[oldLines.length - 1 - suffix] === newLines[newLines.length - 1 - suffix]
  ) {
    suffix += 1;
  }

  const coreOldStart = prefix;
  const coreOldEnd = oldLines.length - suffix;
  const coreNewStart = prefix;
  const coreNewEnd = newLines.length - suffix;
  const context = 3;
  const oldStart = Math.max(0, coreOldStart - context);
  const oldEnd = Math.min(oldLines.length, coreOldEnd + context);
  const newStart = Math.max(0, coreNewStart - context);
  const newEnd = Math.min(newLines.length, coreNewEnd + context);

  const additions = Math.max(0, coreNewEnd - coreNewStart);
  const deletions = Math.max(0, coreOldEnd - coreOldStart);

  const out: string[] = [`--- a/${relPath}`, `+++ b/${relPath}`, `@@ -${oldStart + 1},${oldEnd - oldStart} +${newStart + 1},${newEnd - newStart} @@`];

  for (let i = oldStart; i < coreOldStart; i += 1) out.push(` ${oldLines[i]}`);
  for (let i = coreOldStart; i < coreOldEnd; i += 1) out.push(`-${oldLines[i]}`);
  for (let i = coreNewStart; i < coreNewEnd; i += 1) out.push(`+${newLines[i]}`);
  for (let i = coreOldEnd; i < oldEnd; i += 1) out.push(` ${oldLines[i]}`);

  let diff = out.join("\n");
  if (diff.length > maxChars) {
    diff = diff.slice(0, maxChars) + `\n...[diff truncated to ${maxChars} chars]`;
  }
  return { diff: redactSensitiveText(diff), additions, deletions, changed: true };
}

function isHiddenName(name: string): boolean {
  return name.startsWith(".") && name !== "." && name !== "..";
}

export async function repoTree(config: CodexProConfig, guard: PathGuard, workspace: Workspace, options: TreeOptions): Promise<TreeResult> {
  const target = guard.resolve(workspace, options.path ?? ".");
  const stat = await fsp.stat(target.absPath);
  if (!stat.isDirectory()) {
    throw new CodexProError(`Not a directory: ${target.relPath}`);
  }

  const lines: string[] = [target.relPath === "." ? "." : `${target.relPath}/`];
  let entries = 0;
  let truncated = false;

  async function walk(absDir: string, relDir: string, depth: number, prefix: string): Promise<void> {
    if (depth >= options.maxDepth || truncated) return;
    let dirents = await fsp.readdir(absDir, { withFileTypes: true });
    dirents = dirents
      .filter((entry) => options.includeHidden || !isHiddenName(entry.name))
      .filter((entry) => !guard.isBlockedRelativePath(normalizeRelPath(path.join(relDir, entry.name))))
      .sort((a, b) => {
        if (a.isDirectory() && !b.isDirectory()) return -1;
        if (!a.isDirectory() && b.isDirectory()) return 1;
        return a.name.localeCompare(b.name);
      });

    for (let i = 0; i < dirents.length; i += 1) {
      if (entries >= options.maxEntries) {
        truncated = true;
        return;
      }
      const entry = dirents[i];
      const isLast = i === dirents.length - 1;
      const branch = isLast ? "└── " : "├── ";
      const childPrefix = prefix + (isLast ? "    " : "│   ");
      const childAbs = path.join(absDir, entry.name);
      const childRel = normalizeRelPath(path.join(relDir, entry.name));
      const displayName = entry.isDirectory() ? `${entry.name}/` : entry.name;
      lines.push(`${prefix}${branch}${displayName}`);
      entries += 1;
      if (entry.isDirectory()) {
        await walk(childAbs, childRel, depth + 1, childPrefix);
      }
      if (truncated) return;
    }
  }

  await walk(target.absPath, target.relPath === "." ? "" : target.relPath, 0, "");
  if (truncated) lines.push(`...[tree truncated after ${entries} entries]`);
  return { text: lines.join("\n"), entries, truncated };
}

export async function listFiles(
  guard: PathGuard,
  workspace: Workspace,
  options: { root?: string; glob?: string; includeHidden?: boolean; maxFiles: number }
): Promise<string[]> {
  const target = guard.resolve(workspace, options.root ?? ".");
  const stat = await fsp.stat(target.absPath);
  const files: string[] = [];

  async function addFile(absFile: string): Promise<void> {
    const rel = displayPath(absFile, workspace.root);
    if (guard.isBlockedRelativePath(rel)) return;
    if (!options.includeHidden && rel.split("/").some(isHiddenName)) return;
    if (options.glob && !minimatch(rel, options.glob, { dot: true })) return;
    files.push(rel);
  }

  async function walk(absDir: string): Promise<void> {
    if (files.length >= options.maxFiles) return;
    let entries: fs.Dirent[];
    try {
      entries = await fsp.readdir(absDir, { withFileTypes: true });
    } catch {
      return;
    }
    entries.sort((a, b) => a.name.localeCompare(b.name));
    for (const entry of entries) {
      if (files.length >= options.maxFiles) return;
      const abs = path.join(absDir, entry.name);
      const rel = displayPath(abs, workspace.root);
      if (guard.isBlockedRelativePath(rel)) continue;
      if (!options.includeHidden && rel.split("/").some(isHiddenName)) continue;
      if (entry.isDirectory()) await walk(abs);
      else if (entry.isFile()) await addFile(abs);
    }
  }

  if (stat.isFile()) await addFile(target.absPath);
  else await walk(target.absPath);
  return files;
}

export async function readTextFile(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  filePath: string,
  options: { startLine?: number; endLine?: number; maxBytes?: number; editSnapshots?: EditSnapshotStore } = {}
): Promise<ReadFileResult> {
  const resolved = guard.resolve(workspace, filePath);
  const maxBytes = Math.min(options.maxBytes ?? config.maxReadBytes, config.maxReadBytes);
  const hasRange = options.startLine !== undefined || options.endLine !== undefined;
  await guard.assertTextFile(resolved.absPath, hasRange ? textScanByteLimit(config) : maxBytes);
  const buffer = await fsp.readFile(resolved.absPath);
  const text = buffer.toString("utf8");
  const allLines = splitLines(text);
  const totalLines = allLines.length;
  const startLine = Math.max(1, Math.floor(options.startLine ?? 1));
  const endLine = Math.min(totalLines, Math.floor(options.endLine ?? totalLines));
  if (endLine < startLine) {
    throw new CodexProError(`end_line (${endLine}) must be >= start_line (${startLine}).`);
  }
  const selected = allLines.slice(startLine - 1, endLine);
  const numbered = withLineNumbers(selected, startLine);
  if (hasRange && Buffer.byteLength(numbered, "utf8") > maxBytes) {
    throw new CodexProError(`Selected line range is too large. Limit: ${maxBytes} bytes.`);
  }
  const truncated = startLine > 1 || endLine < totalLines;
  const editTag = options.editSnapshots
    ? options.editSnapshots.record(resolved.absPath, text, { start: startLine, end: endLine }).tag
    : computeEditTag(text);
  return {
    path: resolved.relPath,
    text: numbered,
    startLine,
    endLine,
    totalLines,
    bytes: buffer.byteLength,
    sha256: sha256(text),
    editTag,
    truncated
  };
}

export async function writeTextFile(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  filePath: string,
  content: string,
  options: { createDirs?: boolean; overwrite?: boolean; expectedSha256?: string } = {}
): Promise<{ path: string; bytes: number; sha256: string; existed: boolean; diff: DiffResult }> {
  const resolved = guard.resolve(workspace, filePath, { forWrite: true });
  const contentBytes = Buffer.byteLength(content, "utf8");
  if (contentBytes > config.maxWriteBytes) {
    throw new CodexProError(`Write content is too large (${contentBytes} bytes). Limit: ${config.maxWriteBytes} bytes.`);
  }
  if (hasSecretValue(content)) {
    throw new CodexProError("Secret-looking content is blocked from write. Use placeholders such as [REDACTED_SECRET] in handoff files.");
  }

  const releaseWriteLock = await acquireFileWriteLock(resolved.absPath);
  try {
    let oldText = "";
    let existed = false;
    try {
      await guard.assertTextFile(resolved.absPath, Math.max(config.maxWriteBytes, config.maxReadBytes));
      oldText = await fsp.readFile(resolved.absPath, "utf8");
      existed = true;
    } catch (error) {
      if (error instanceof CodexProError && error.message.startsWith("Not a file")) throw error;
      if (fs.existsSync(resolved.absPath)) throw error;
    }

    if (existed && options.overwrite === false) {
      throw new CodexProError(`File already exists and overwrite=false: ${resolved.relPath}`);
    }
    if (options.expectedSha256 && !existed) {
      throw new CodexProError(`File does not exist, so expected_sha256 cannot be verified: ${resolved.relPath}`);
    }
    if (existed) assertExpectedSha(options.expectedSha256, oldText, resolved.relPath);
    if (options.createDirs) {
      await fsp.mkdir(path.dirname(resolved.absPath), { recursive: true });
    }

    const diff = makeUnifiedDiff(oldText, content, resolved.relPath);
    await writeText(resolved.absPath, content, existed ? oldText : undefined, resolved.relPath);
    return { path: resolved.relPath, bytes: contentBytes, sha256: sha256(content), existed, diff };
  } finally {
    releaseWriteLock();
  }
}

export async function editTextFileByLines(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  filePath: string,
  edits: AnchoredLineEdit[],
  editSnapshots: EditSnapshotStore,
  editTag: string
): Promise<AnchoredEditResult> {
  if (!editTag) {
    throw new CodexProError("edit_tag is required. Read the file first and copy its four-character edit tag.");
  }
  if (!edits.length) throw new CodexProError("edits must contain at least one operation.");
  if (edits.length > 100) throw new CodexProError("A single edit call supports at most 100 tagged line operations.");

  const resolved = guard.resolve(workspace, filePath, { forWrite: true });
  const releaseWriteLock = await acquireFileWriteLock(resolved.absPath);
  try {
    await guard.assertTextFile(resolved.absPath, Math.max(config.maxWriteBytes, config.maxReadBytes));
    const before = await fsp.readFile(resolved.absPath, "utf8");
    const snapshot = editSnapshots.resolve(resolved.absPath, editTag, before, resolved.relPath);

    const hasBom = before.startsWith("\uFEFF");
    const body = hasBom ? before.slice(1) : before;
    const lineEndings = body.match(/\r\n|\r|\n/g) ?? [];
    const distinctLineEndings = new Set(lineEndings);
    if (distinctLineEndings.size > 1) {
      throw new CodexProError(
        `Tagged line edit does not normalize mixed line endings in ${resolved.relPath}. Use apply_patch for this file.`
      );
    }
    const lineEnding = lineEndings[0] ?? "\n";
    const normalizedBody = body.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
    const originalLines = normalizedBody.split("\n");
    const lineCount = originalLines.length;

    type PreparedRange = { start: number; end: number; lines: string[]; operation: number };
    type PreparedInsertion = { lines: string[]; operation: number };
    const ranges: PreparedRange[] = [];
    const insertions = new Map<number, PreparedInsertion>();

    const assertLine = (value: number, field: string, operation: number): void => {
      if (!Number.isInteger(value) || value < 1 || value > lineCount) {
        throw new CodexProError(
          `edits[${operation}].${field} must be an original line between 1 and ${lineCount}; received ${value}.`
        );
      }
    };
    const contentLines = (content: string): string[] => {
      const normalized = content.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
      const lines = normalized.split("\n");
      if (lines.length > 1 && lines[lines.length - 1] === "") lines.pop();
      return lines;
    };

    for (let operation = 0; operation < edits.length; operation += 1) {
      const edit = edits[operation];
      if (edit.op === "replace" || edit.op === "delete") {
        assertLine(edit.startLine, "start_line", operation);
        const endLine = edit.endLine ?? edit.startLine;
        assertLine(endLine, "end_line", operation);
        if (endLine < edit.startLine) {
          throw new CodexProError(
            `edits[${operation}].end_line (${endLine}) must be greater than or equal to start_line (${edit.startLine}).`
          );
        }
        assertSnapshotRangeSeen(snapshot, edit.startLine, endLine, operation, resolved.relPath);
        ranges.push({
          start: edit.startLine - 1,
          end: endLine,
          lines: edit.op === "replace" ? contentLines(edit.content) : [],
          operation
        });
        continue;
      }

      assertLine(edit.line, "line", operation);
      assertSnapshotRangeSeen(snapshot, edit.line, edit.line, operation, resolved.relPath);
      const gap = edit.op === "insert_before" ? edit.line - 1 : edit.line;
      const previous = insertions.get(gap);
      if (previous) {
        throw new CodexProError(
          `edits[${operation}] and edits[${previous.operation}] both insert at the same original gap. Combine their content into one operation.`
        );
      }
      insertions.set(gap, { lines: contentLines(edit.content), operation });
    }

    ranges.sort((left, right) => left.start - right.start || left.end - right.end);
    for (let index = 1; index < ranges.length; index += 1) {
      const previous = ranges[index - 1];
      const current = ranges[index];
      if (current.start < previous.end) {
        throw new CodexProError(
          `edits[${current.operation}] overlaps edits[${previous.operation}]. All ranges must name disjoint lines in the original edit-tag snapshot.`
        );
      }
    }
    for (const [gap, insertion] of insertions) {
      const containing = ranges.find((range) => gap > range.start && gap < range.end);
      if (containing) {
        throw new CodexProError(
          `edits[${insertion.operation}] inserts inside the original range owned by edits[${containing.operation}]. Insert at a range boundary or combine the change.`
        );
      }
    }

    const rangeAt = new Map(ranges.map((range) => [range.start, range]));
    const outputLines: string[] = [];
    let cursor = 0;
    while (cursor <= lineCount) {
      const insertion = insertions.get(cursor);
      if (insertion) outputLines.push(...insertion.lines);
      if (cursor === lineCount) break;

      const range = rangeAt.get(cursor);
      if (range) {
        outputLines.push(...range.lines);
        cursor = range.end;
      } else {
        outputLines.push(originalLines[cursor]);
        cursor += 1;
      }
    }

    const afterBody = outputLines.join(lineEnding);
    const after = `${hasBom ? "\uFEFF" : ""}${afterBody}`;
    const afterBytes = Buffer.byteLength(after, "utf8");
    if (afterBytes > config.maxWriteBytes) {
      throw new CodexProError(`Edited file would be too large (${afterBytes} bytes). Limit: ${config.maxWriteBytes} bytes.`);
    }
    if (introducesSecretValue(before, after)) {
      throw new CodexProError("Secret-looking content is blocked from edit. Use placeholders such as [REDACTED_SECRET] in handoff files.");
    }

    const diff = makeUnifiedDiff(before, after, resolved.relPath);
    if (!diff.changed) {
      throw new CodexProError(`Tagged edit would not change ${resolved.relPath}. Re-read the file and remove no-op operations.`);
    }
    await writeText(resolved.absPath, after, before, resolved.relPath);
    const nextSnapshot = editSnapshots.record(resolved.absPath, after);
    return {
      path: resolved.relPath,
      edits: edits.length,
      bytes: afterBytes,
      baseTag: snapshot.tag,
      editTag: nextSnapshot.tag,
      diff
    };
  } finally {
    releaseWriteLock();
  }
}

export async function ensureAiBridge(config: CodexProConfig, guard: PathGuard, workspace: Workspace): Promise<string[]> {
  const files: Record<string, string> = {
    "README.md": `# AI Bridge\n\nShared planning context for ChatGPT, other planning models, Codex, OpenCode, Pi, or another local implementation agent.\n\n- current-plan.md: plan produced by ChatGPT or another planning model for the implementation agent.\n- agent-status.md: generic implementation notes, touched files, test results, blockers, and review notes.\n- implementation-diff.patch: final review diff from the implementation agent when practical.\n- codex-status.md: legacy Codex-specific status file, kept for existing workflows.\n- decisions.md: architectural decisions that should remain stable.\n- open-questions.md: unresolved questions.\n- execution-log.jsonl: append-only generic agent handoff and execution events.\n- handoff-run-state.json: machine-readable run lifecycle (running/completed/failed/timed_out) written by execute-handoff/watch-handoff/loop-handoff and polled by the read-only wait_for_handoff tool.\n- session-log.jsonl: append-only legacy session events.\n`,
    "current-plan.md": "# Current Plan\n\nNo plan written yet.\n",
    "agent-status.md": "# Agent Status\n\nNo implementation agent status written yet.\n",
    "implementation-diff.patch": "",
    "codex-status.md": "# Codex Status\n\nNo Codex status written yet.\n",
    "decisions.md": "# Decisions\n\n",
    "open-questions.md": "# Open Questions\n\n",
    "execution-log.jsonl": "",
    "session-log.jsonl": ""
  };
  const created: string[] = [];
  for (const [name, content] of Object.entries(files)) {
    const rel = `${config.contextDir}/${name}`;
    const resolved = guard.resolve(workspace, rel, { forWrite: true });
    if (!fs.existsSync(resolved.absPath)) {
      await fsp.mkdir(path.dirname(resolved.absPath), { recursive: true });
      await fsp.writeFile(resolved.absPath, content, "utf8");
      created.push(rel);
    }
  }
  return created;
}
