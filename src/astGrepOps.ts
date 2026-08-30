import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import fsp from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import type { CodexProConfig } from "./config.js";
import { expandHome } from "./config.js";
import { EditSnapshotStore, textScanByteLimit } from "./fsOps.js";
import { CodexProError, PathGuard, type Workspace } from "./guard.js";
import { terminateProcessTree } from "./processOps.js";
import { redactSensitiveText } from "./redact.js";
import { currentToolContext } from "./toolContext.js";

export type AstGrepStrictness = "cst" | "smart" | "ast" | "relaxed" | "signature" | "template";
export type AstGrepMode = "pattern" | "kind";

export interface AstGrepOptions {
  pattern?: string;
  kind?: string;
  language?: string;
  selector?: string;
  strictness?: AstGrepStrictness;
  root?: string;
  globs?: string[];
  includeHidden: boolean;
  maxResults: number;
  contextBefore: number;
  contextAfter: number;
  groupByFile: boolean;
  cursor?: string;
  timeoutMs: number;
  editSnapshots?: EditSnapshotStore;
}

export interface AstGrepSourceRange {
  start_line: number;
  start_column: number;
  end_line: number;
  end_column: number;
  start_byte?: number;
  end_byte?: number;
}

export interface AstGrepCapture {
  name: string;
  capture_type: "single" | "multi" | "transformed";
  text: string;
  range?: AstGrepSourceRange;
  truncated?: boolean;
}

export interface AstGrepMatch {
  path: string;
  language: string;
  text: string;
  start_line: number;
  start_column: number;
  end_line: number;
  end_column: number;
  start_byte: number;
  end_byte: number;
  captures: AstGrepCapture[];
  editable: boolean;
  edit_tag?: string;
}

export interface AstGrepContextBlock {
  path: string;
  start_line: number;
  end_line: number;
  total_lines?: number;
  match_indices: number[];
  match_lines: number[];
  text: string;
  editable: boolean;
  edit_tag?: string;
  truncated?: boolean;
}

export interface AstGrepResult {
  text: string;
  mode: AstGrepMode;
  provider: "ast-grep-cli";
  providerVersion: string;
  matches: AstGrepMatch[];
  contexts: AstGrepContextBlock[];
  truncated: boolean;
  hasMore: boolean;
  nextCursor?: string;
  queryFingerprint: string;
  warnings: string[];
}

interface AstGrepCursorPayload {
  v: 1;
  signature: string;
  offset: number;
  checksum: string;
}

interface NativePoint {
  line: number;
  column: number;
}

interface NativeRange {
  byteOffset?: { start?: number; end?: number };
  start?: NativePoint;
  end?: NativePoint;
}

interface RawAstGrepHit {
  path: string;
  language: string;
  startLine: number;
  startColumn: number;
  endLine: number;
  endColumn: number;
  startByte: number;
  endByte: number;
  matchBytes: number;
  matchDigest: string;
  preview: string;
  captures: AstGrepCapture[];
}

interface AstGrepProcessPage {
  hits: RawAstGrepHit[];
  hasMore: boolean;
  warnings: string[];
}

interface FileState {
  absPath: string;
  buffer: Buffer;
  text: string;
  lines: string[];
  totalLines: number;
}

interface ContextCandidate {
  hit: RawAstGrepHit;
  state?: FileState;
  verified: boolean;
  start: number;
  end: number;
  full: boolean;
  text: string;
  warning?: string;
}

const require = createRequire(import.meta.url);
const AST_GREP_CURSOR_VERSION = 1;
const AST_GREP_PROVIDER = "ast-grep-cli" as const;
const MAX_CURSOR_CHARS = 4_096;
const MAX_GLOBS = 64;
const MAX_GLOB_CHARS = 512;
const MAX_WARNINGS = 30;
const MAX_CAPTURE_COUNT = 64;
const MAX_CAPTURE_TEXT_CHARS = 800;
const MAX_MATCH_PREVIEW_CHARS = 800;
const MAX_MATCH_SPAN_LINES = 200;
const MAX_CONTEXT_BLOCK_BYTES = 128 * 1024;
const MAX_JSON_EVENT_BYTES = 8 * 1024 * 1024;
const MAX_STDERR_BYTES = 64 * 1024;
const MIN_TIMEOUT_MS = 1_000;
const MAX_TIMEOUT_MS = 60_000;
const versionCache = new Map<string, string>();

function sha256(value: string | Buffer): string {
  return createHash("sha256").update(value).digest("hex");
}

function truncateText(value: string, maxChars: number): { value: string; truncated: boolean } {
  if (value.length <= maxChars) return { value, truncated: false };
  return { value: `${value.slice(0, Math.max(0, maxChars - 1))}…`, truncated: true };
}

function boundedWarnings(values: string[]): string[] {
  return [...new Set(values.map((value) => redactSensitiveText(value.trim())).filter(Boolean))].slice(0, MAX_WARNINGS);
}

function splitLines(text: string): string[] {
  return text.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n");
}

function numberedLines(lines: string[], startLine: number): string {
  const width = String(startLine + Math.max(0, lines.length - 1)).length;
  return lines.map((line, index) => `${String(startLine + index).padStart(width, " ")} | ${line}`).join("\n");
}

function objectValue(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {};
}

function finiteInteger(value: unknown): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) ? value : undefined;
}

function nativeRange(value: unknown): NativeRange | undefined {
  const source = objectValue(value);
  const byteOffset = objectValue(source.byteOffset);
  const start = objectValue(source.start);
  const end = objectValue(source.end);
  const startLine = finiteInteger(start.line);
  const startColumn = finiteInteger(start.column);
  const endLine = finiteInteger(end.line);
  const endColumn = finiteInteger(end.column);
  if (startLine === undefined || startColumn === undefined || endLine === undefined || endColumn === undefined) {
    return undefined;
  }
  return {
    byteOffset: {
      start: finiteInteger(byteOffset.start),
      end: finiteInteger(byteOffset.end)
    },
    start: { line: startLine, column: startColumn },
    end: { line: endLine, column: endColumn }
  };
}

function publicRange(value: unknown): AstGrepSourceRange | undefined {
  const range = nativeRange(value);
  if (!range?.start || !range.end) return undefined;
  return {
    start_line: range.start.line + 1,
    start_column: range.start.column,
    end_line: range.end.line + 1,
    end_column: range.end.column,
    ...(range.byteOffset?.start !== undefined ? { start_byte: range.byteOffset.start } : {}),
    ...(range.byteOffset?.end !== undefined ? { end_byte: range.byteOffset.end } : {})
  };
}

function parseCaptures(value: unknown): AstGrepCapture[] {
  const meta = objectValue(value);
  const captures: AstGrepCapture[] = [];
  const add = (name: string, captureType: AstGrepCapture["capture_type"], raw: unknown) => {
    if (captures.length >= MAX_CAPTURE_COUNT) return;
    const node = objectValue(raw);
    const sourceText = typeof node.text === "string"
      ? node.text
      : typeof raw === "string"
        ? raw
        : "";
    if (!sourceText) return;
    const bounded = truncateText(sourceText, MAX_CAPTURE_TEXT_CHARS);
    captures.push({
      name,
      capture_type: captureType,
      text: bounded.value,
      ...(publicRange(node.range) ? { range: publicRange(node.range) } : {}),
      ...(bounded.truncated ? { truncated: true } : {})
    });
  };

  for (const [name, raw] of Object.entries(objectValue(meta.single))) add(name, "single", raw);
  for (const [name, raw] of Object.entries(objectValue(meta.multi))) {
    if (!Array.isArray(raw)) continue;
    for (const item of raw) add(name, "multi", item);
  }
  for (const [name, raw] of Object.entries(objectValue(meta.transformed))) add(name, "transformed", raw);
  return captures;
}

function validateQuery(options: AstGrepOptions): AstGrepMode {
  const pattern = options.pattern?.trim() ?? "";
  const kind = options.kind?.trim() ?? "";
  if (Boolean(pattern) === Boolean(kind)) {
    throw new CodexProError("Provide exactly one of pattern or kind for ast_grep.", {
      code: "ast_grep_query_invalid",
      retryUnchanged: false
    });
  }
  if (options.selector && !pattern) {
    throw new CodexProError("selector requires pattern mode.", {
      code: "ast_grep_query_invalid",
      retryUnchanged: false
    });
  }
  if (options.strictness && !pattern) {
    throw new CodexProError("strictness requires pattern mode.", {
      code: "ast_grep_query_invalid",
      retryUnchanged: false
    });
  }
  if ((options.globs?.length ?? 0) > MAX_GLOBS) {
    throw new CodexProError(`ast_grep accepts at most ${MAX_GLOBS} globs.`);
  }
  for (const glob of options.globs ?? []) {
    if (!glob || glob.length > MAX_GLOB_CHARS || glob.includes("\0") || /[\r\n]/.test(glob)) {
      throw new CodexProError(`Invalid ast_grep glob. Each glob must be 1-${MAX_GLOB_CHARS} characters without NUL or newlines.`);
    }
  }
  return pattern ? "pattern" : "kind";
}

function explicitExecutable(): string | undefined {
  const raw = process.env.CODEXPRO_AST_GREP_PATH ?? process.env.AST_GREP_PATH;
  if (!raw?.trim()) return undefined;
  const value = raw.trim();
  if (value.includes("\0") || /[\r\n]/.test(value)) {
    throw new CodexProError("CODEXPRO_AST_GREP_PATH must be one executable path or command name.");
  }
  if (value.includes(path.sep) || value.includes("/") || value.includes("\\") || value.startsWith("~")) {
    return path.resolve(expandHome(value));
  }
  return value;
}

async function packagedExecutable(): Promise<string | undefined> {
  try {
    const packageJson = require.resolve("@ast-grep/cli/package.json");
    const executable = path.join(path.dirname(packageJson), process.platform === "win32" ? "ast-grep.exe" : "ast-grep");
    await fsp.access(executable);
    return executable;
  } catch {
    return undefined;
  }
}

export async function resolveAstGrepExecutable(): Promise<string> {
  return explicitExecutable() ?? await packagedExecutable() ?? "ast-grep";
}

async function astGrepVersion(executable: string): Promise<string> {
  const cached = versionCache.get(executable);
  if (cached) return cached;
  const result = spawnSync(executable, ["--version"], {
    encoding: "utf8",
    timeout: 5_000,
    windowsHide: true,
    shell: false,
    env: { ...process.env, NO_COLOR: "1" }
  });
  if (result.error) {
    const code = (result.error as NodeJS.ErrnoException).code;
    if (code === "ENOENT") {
      throw new CodexProError(
        "ast-grep is unavailable. Reinstall CodexPro dependencies, install @ast-grep/cli, or set CODEXPRO_AST_GREP_PATH to the ast-grep executable.",
        {
          code: "ast_grep_unavailable",
          retryUnchanged: false,
          recovery: {
            tool: "ast_grep",
            message: "Install @ast-grep/cli or configure CODEXPRO_AST_GREP_PATH, then retry the structural search."
          }
        }
      );
    }
    throw new CodexProError(`Unable to start ast-grep: ${redactSensitiveText(result.error.message)}`, {
      code: "ast_grep_unavailable",
      retryUnchanged: false
    });
  }
  if (result.status !== 0) {
    throw new CodexProError(
      `ast-grep version check failed: ${redactSensitiveText(String(result.stderr || result.stdout || `exit ${result.status}`)).trim()}`,
      { code: "ast_grep_unavailable", retryUnchanged: false }
    );
  }
  const output = String(result.stdout || "").trim();
  const version = output.match(/\b(\d+\.\d+\.\d+(?:[-+][A-Za-z0-9.-]+)?)\b/)?.[1] ?? (output || "unknown");
  versionCache.set(executable, version);
  return version;
}

function signatureFor(workspace: Workspace, options: AstGrepOptions, mode: AstGrepMode, version: string): string {
  return sha256(JSON.stringify({
    cursor_version: AST_GREP_CURSOR_VERSION,
    workspace_id: workspace.id,
    provider: AST_GREP_PROVIDER,
    provider_version: version,
    mode,
    pattern: options.pattern ?? "",
    kind: options.kind ?? "",
    language: options.language ?? "",
    selector: options.selector ?? "",
    strictness: options.strictness ?? "",
    root: options.root ?? ".",
    globs: options.globs ?? [],
    include_hidden: options.includeHidden,
    context_before: options.contextBefore,
    context_after: options.contextAfter,
    group_by_file: options.groupByFile
  }));
}

function cursorChecksum(signature: string, offset: number): string {
  return sha256(`codexpro-ast-grep-cursor-v1\0${signature}\0${offset}`).slice(0, 20);
}

function encodeCursor(signature: string, offset: number): string {
  const payload: AstGrepCursorPayload = {
    v: AST_GREP_CURSOR_VERSION,
    signature,
    offset,
    checksum: cursorChecksum(signature, offset)
  };
  return Buffer.from(JSON.stringify(payload), "utf8").toString("base64url");
}

function decodeCursor(value: string | undefined, signature: string): number {
  if (!value) return 0;
  if (value.length > MAX_CURSOR_CHARS) {
    throw new CodexProError("ast_grep cursor is too large. Restart without cursor.", {
      code: "ast_grep_cursor_invalid",
      retryUnchanged: false,
      recovery: { tool: "ast_grep", message: "Restart this structural search without cursor." }
    });
  }
  try {
    const parsed = JSON.parse(Buffer.from(value, "base64url").toString("utf8")) as Partial<AstGrepCursorPayload>;
    if (
      parsed.v !== AST_GREP_CURSOR_VERSION ||
      parsed.signature !== signature ||
      !Number.isSafeInteger(parsed.offset) ||
      Number(parsed.offset) < 0 ||
      parsed.checksum !== cursorChecksum(signature, Number(parsed.offset))
    ) {
      throw new Error("cursor mismatch");
    }
    return Number(parsed.offset);
  } catch {
    throw new CodexProError("ast_grep cursor does not belong to this exact structural query. Restart without cursor.", {
      code: "ast_grep_cursor_mismatch",
      retryUnchanged: false,
      recovery: {
        tool: "ast_grep",
        message: "Restart this structural search without cursor and reuse only the next_cursor it returns."
      }
    });
  }
}

function safeRelativePath(workspace: Workspace, guard: PathGuard, value: string): string | undefined {
  const absolute = path.isAbsolute(value) ? path.resolve(value) : path.resolve(workspace.root, value);
  const relative = path.relative(workspace.root, absolute).split(path.sep).join("/") || ".";
  if (relative === ".." || relative.startsWith("../") || path.isAbsolute(relative)) return undefined;
  if (guard.isBlockedRelativePath(relative)) return undefined;
  try {
    return guard.resolve(workspace, relative).relPath;
  } catch {
    return undefined;
  }
}

function parseRawHit(workspace: Workspace, guard: PathGuard, raw: unknown): RawAstGrepHit | undefined {
  const value = objectValue(raw);
  const file = typeof value.file === "string" ? value.file : "";
  const text = typeof value.text === "string" ? value.text : "";
  const range = nativeRange(value.range);
  const relative = file ? safeRelativePath(workspace, guard, file) : undefined;
  const startByte = range?.byteOffset?.start;
  const endByte = range?.byteOffset?.end;
  if (
    !relative ||
    !range?.start ||
    !range.end ||
    startByte === undefined ||
    endByte === undefined ||
    startByte < 0 ||
    endByte < startByte
  ) {
    return undefined;
  }
  const preview = truncateText(text, MAX_MATCH_PREVIEW_CHARS).value;
  return {
    path: relative,
    language: typeof value.language === "string" && value.language.trim() ? value.language.trim() : "unknown",
    startLine: range.start.line + 1,
    startColumn: range.start.column,
    endLine: range.end.line + 1,
    endColumn: range.end.column,
    startByte,
    endByte,
    matchBytes: Buffer.byteLength(text, "utf8"),
    matchDigest: sha256(text),
    preview,
    captures: parseCaptures(value.metaVariables)
  };
}

function buildArguments(config: CodexProConfig, options: AstGrepOptions, mode: AstGrepMode, targetPath: string): string[] {
  const args = ["run", "--json=stream", "--color", "never", "--threads", "1"];
  if (mode === "pattern") args.push("--pattern", String(options.pattern));
  else args.push("--kind", String(options.kind));
  if (options.language?.trim()) args.push("--lang", options.language.trim());
  if (options.selector?.trim()) args.push("--selector", options.selector.trim());
  if (options.strictness) args.push("--strictness", options.strictness);
  if (options.includeHidden) args.push("--no-ignore", "hidden");
  for (const glob of options.globs ?? []) args.push("--globs", glob);
  // Safety exclusions are appended last so a user include cannot override them.
  for (const glob of config.blockedGlobs) args.push("--globs", glob.startsWith("!") ? glob : `!${glob}`);
  args.push(targetPath || ".");
  return args;
}

async function runAstGrepPage(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  options: AstGrepOptions,
  mode: AstGrepMode,
  executable: string,
  offset: number
): Promise<AstGrepProcessPage> {
  const target = guard.resolve(workspace, options.root ?? ".");
  const args = buildArguments(config, options, mode, target.relPath);
  const wanted = options.maxResults + 1;
  const timeoutMs = Math.max(MIN_TIMEOUT_MS, Math.min(options.timeoutMs, MAX_TIMEOUT_MS));
  const outputLimit = Math.max(8 * 1024 * 1024, Math.min(64 * 1024 * 1024, config.maxOutputBytes * 64));
  const signal = currentToolContext()?.signal;

  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {
      cwd: workspace.root,
      env: { ...process.env, NO_COLOR: "1" },
      shell: false,
      windowsHide: true,
      detached: process.platform !== "win32"
    });
    const hits: RawAstGrepHit[] = [];
    const warnings: string[] = [];
    let carry = "";
    let stderr = "";
    let observedBytes = 0;
    let eligibleSeen = 0;
    let stoppedForPage = false;
    let outputLimited = false;
    let timedOut = false;
    let cancelled = false;
    let settled = false;
    let parseError: Error | undefined;
    let timer: NodeJS.Timeout | undefined;
    let killTimer: NodeJS.Timeout | undefined;

    const clear = () => {
      if (timer) clearTimeout(timer);
      if (killTimer) clearTimeout(killTimer);
      signal?.removeEventListener("abort", onAbort);
    };
    const terminate = () => {
      if (child.exitCode !== null || child.signalCode !== null) return;
      terminateProcessTree(child, "SIGTERM");
      if (!killTimer) {
        killTimer = setTimeout(() => terminateProcessTree(child, "SIGKILL"), 1_000);
        killTimer.unref();
      }
    };
    const onAbort = () => {
      cancelled = true;
      terminate();
    };
    const fail = (error: Error) => {
      if (settled) return;
      settled = true;
      clear();
      terminate();
      reject(error);
    };
    const processLine = (line: string) => {
      if (!line || stoppedForPage || parseError) return;
      if (Buffer.byteLength(line, "utf8") > MAX_JSON_EVENT_BYTES) {
        parseError = new CodexProError(
          `ast-grep returned one match larger than the ${MAX_JSON_EVENT_BYTES}-byte event limit. Narrow the pattern or path.`,
          { code: "ast_grep_output_too_large", retryUnchanged: false }
        );
        terminate();
        return;
      }
      let parsed: unknown;
      try {
        parsed = JSON.parse(line);
      } catch (error) {
        parseError = new CodexProError(
          `ast-grep returned malformed JSON: ${error instanceof Error ? error.message : String(error)}`,
          { code: "ast_grep_invalid_output", retryUnchanged: false }
        );
        terminate();
        return;
      }
      const hit = parseRawHit(workspace, guard, parsed);
      if (!hit) return;
      if (eligibleSeen < offset) {
        eligibleSeen += 1;
        return;
      }
      eligibleSeen += 1;
      hits.push(hit);
      if (hits.length >= wanted) {
        stoppedForPage = true;
        terminate();
      }
    };

    timer = setTimeout(() => {
      timedOut = true;
      terminate();
    }, timeoutMs);
    timer.unref();
    if (signal) {
      if (signal.aborted) onAbort();
      else signal.addEventListener("abort", onAbort, { once: true });
    }

    child.stdout.on("data", (chunk) => {
      const text = String(chunk);
      observedBytes += Buffer.byteLength(text, "utf8");
      if (observedBytes > outputLimit) {
        outputLimited = true;
        warnings.push(`ast-grep output exceeded the ${outputLimit}-byte discovery limit.`);
        terminate();
        return;
      }
      carry += text;
      if (Buffer.byteLength(carry, "utf8") > MAX_JSON_EVENT_BYTES) {
        parseError = new CodexProError(
          `ast-grep returned an unterminated JSON event larger than ${MAX_JSON_EVENT_BYTES} bytes.`,
          { code: "ast_grep_invalid_output", retryUnchanged: false }
        );
        terminate();
        return;
      }
      let newline = carry.indexOf("\n");
      while (newline >= 0) {
        processLine(carry.slice(0, newline));
        carry = carry.slice(newline + 1);
        newline = carry.indexOf("\n");
      }
    });
    child.stderr.on("data", (chunk) => {
      if (Buffer.byteLength(stderr, "utf8") >= MAX_STDERR_BYTES) return;
      const next = stderr + String(chunk);
      stderr = Buffer.byteLength(next, "utf8") <= MAX_STDERR_BYTES
        ? next
        : `${next.slice(0, MAX_STDERR_BYTES)}\n...[stderr truncated]`;
    });
    child.on("error", (error) => {
      const code = (error as NodeJS.ErrnoException).code;
      if (code === "ENOENT") {
        fail(new CodexProError(
          "ast-grep is unavailable. Reinstall CodexPro dependencies, install @ast-grep/cli, or set CODEXPRO_AST_GREP_PATH.",
          { code: "ast_grep_unavailable", retryUnchanged: false }
        ));
        return;
      }
      fail(new CodexProError(`Unable to start ast-grep: ${redactSensitiveText(error.message)}`, {
        code: "ast_grep_unavailable",
        retryUnchanged: false
      }));
    });
    child.on("close", (code, closeSignal) => {
      if (settled) return;
      settled = true;
      clear();
      if (carry && !stoppedForPage && !parseError && !outputLimited) processLine(carry);
      if (parseError) {
        reject(parseError);
        return;
      }
      if (cancelled) {
        reject(new CodexProError("ast_grep search was cancelled.", {
          code: "ast_grep_cancelled",
          retryUnchanged: false
        }));
        return;
      }
      if (timedOut) {
        reject(new CodexProError(`ast_grep timed out after ${timeoutMs} ms. Narrow the path or pattern.`, {
          code: "ast_grep_timeout",
          retryUnchanged: false
        }));
        return;
      }
      if (code !== 0 && !stoppedForPage && !outputLimited) {
        const detail = redactSensitiveText(stderr.trim() || `exit ${code ?? "null"}${closeSignal ? `, signal ${closeSignal}` : ""}`);
        reject(new CodexProError(`ast-grep failed: ${detail}`, {
          code: "ast_grep_failed",
          retryUnchanged: false
        }));
        return;
      }
      resolve({
        hits,
        hasMore: hits.length > options.maxResults || stoppedForPage || outputLimited,
        warnings: boundedWarnings(warnings)
      });
    });
  });
}

async function loadFileStates(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  hits: RawAstGrepHit[]
): Promise<{ states: Map<string, FileState>; warnings: string[] }> {
  const states = new Map<string, FileState>();
  const warnings: string[] = [];
  for (const relative of [...new Set(hits.map((hit) => hit.path))]) {
    try {
      const resolved = guard.resolve(workspace, relative);
      await guard.assertTextFile(resolved.absPath, textScanByteLimit(config));
      const buffer = await fsp.readFile(resolved.absPath);
      if (buffer.includes(0)) throw new Error("binary file");
      const text = buffer.toString("utf8");
      const lines = splitLines(text);
      states.set(relative, {
        absPath: resolved.absPath,
        buffer,
        text,
        lines,
        totalLines: lines.length
      });
    } catch (error) {
      warnings.push(`${relative}: current file could not be loaded for structural context (${error instanceof Error ? error.message : String(error)}).`);
    }
  }
  return { states, warnings: boundedWarnings(warnings) };
}

function verifyHit(hit: RawAstGrepHit, state: FileState): boolean {
  if (hit.startByte < 0 || hit.endByte > state.buffer.byteLength || hit.endByte < hit.startByte) return false;
  const slice = state.buffer.subarray(hit.startByte, hit.endByte);
  return slice.byteLength === hit.matchBytes && sha256(slice) === hit.matchDigest;
}

function contextCandidate(hit: RawAstGrepHit, state: FileState | undefined, options: AstGrepOptions): ContextCandidate {
  if (!state || !verifyHit(hit, state)) {
    return {
      hit,
      state,
      verified: false,
      start: hit.startLine,
      end: hit.endLine,
      full: false,
      text: `${hit.startLine} | ${hit.preview}`,
      warning: `${hit.path}:${hit.startLine}: file changed during ast_grep search; this match has no edit provenance.`
    };
  }
  const requestedSpan = Math.max(1, hit.endLine - hit.startLine + 1);
  const fullSpan = requestedSpan <= MAX_MATCH_SPAN_LINES;
  const matchEnd = fullSpan ? hit.endLine : hit.startLine + MAX_MATCH_SPAN_LINES - 1;
  const start = Math.max(1, hit.startLine - options.contextBefore);
  const end = Math.min(state.totalLines, matchEnd + options.contextAfter);
  const text = numberedLines(state.lines.slice(start - 1, end), start);
  const byteComplete = Buffer.byteLength(text, "utf8") <= MAX_CONTEXT_BLOCK_BYTES;
  return {
    hit,
    state,
    verified: true,
    start,
    end,
    full: fullSpan && byteComplete,
    text: byteComplete ? text : truncateText(text, 4_000).value,
    ...(!fullSpan
      ? { warning: `${hit.path}:${hit.startLine}: structural match spans more than ${MAX_MATCH_SPAN_LINES} lines; context is read-only and truncated.` }
      : !byteComplete
        ? { warning: `${hit.path}:${start}-${end}: context exceeded ${MAX_CONTEXT_BLOCK_BYTES} bytes; edit provenance was not granted.` }
        : {})
  };
}

function candidateCost(candidate: ContextCandidate): number {
  return Math.min(MAX_CONTEXT_BLOCK_BYTES, Buffer.byteLength(candidate.text, "utf8") + 384);
}

function matchKey(hit: RawAstGrepHit): string {
  return `${hit.path}\0${hit.startByte}\0${hit.endByte}`;
}

async function buildContextPage(
  config: CodexProConfig,
  options: AstGrepOptions,
  hits: RawAstGrepHit[],
  states: Map<string, FileState>
): Promise<{
  selectedHits: RawAstGrepHit[];
  matches: AstGrepMatch[];
  contexts: AstGrepContextBlock[];
  outputLimited: boolean;
  warnings: string[];
}> {
  const warnings: string[] = [];
  const candidates = hits.map((hit) => contextCandidate(hit, states.get(hit.path), options));
  const outputBudget = Math.max(8 * 1024, Math.floor(config.maxOutputBytes * 0.65));
  const selected: ContextCandidate[] = [];
  let consumed = 0;
  let outputLimited = false;
  for (const candidate of candidates) {
    const cost = candidateCost(candidate);
    if (selected.length && consumed + cost > outputBudget) {
      outputLimited = true;
      break;
    }
    selected.push(candidate);
    consumed += cost;
    if (candidate.warning) warnings.push(candidate.warning);
  }

  type Range = { start: number; end: number; candidates: ContextCandidate[] };
  const byFile = new Map<string, Range[]>();
  for (const candidate of selected.filter((item) => item.verified && item.state)) {
    const ranges = byFile.get(candidate.hit.path) ?? [];
    ranges.push({ start: candidate.start, end: candidate.end, candidates: [candidate] });
    byFile.set(candidate.hit.path, ranges);
  }

  const firstOrder = new Map(selected.map((candidate, index) => [candidate.hit.path, index]));
  const contexts: AstGrepContextBlock[] = [];
  const editableMatches = new Set<string>();
  const tags = new Map<string, string>();
  const matchIndex = new Map(selected.map((candidate, index) => [matchKey(candidate.hit), index]));

  for (const [relative, rawRanges] of [...byFile.entries()].sort((left, right) =>
    (firstOrder.get(left[0]) ?? Number.MAX_SAFE_INTEGER) - (firstOrder.get(right[0]) ?? Number.MAX_SAFE_INTEGER)
  )) {
    const state = states.get(relative);
    if (!state) continue;
    const ordered = rawRanges.sort((left, right) => left.start - right.start || left.end - right.end);
    const ranges: Range[] = [];
    for (const range of ordered) {
      const previous = ranges[ranges.length - 1];
      if (options.groupByFile && previous && range.start <= previous.end + 1) {
        previous.end = Math.max(previous.end, range.end);
        previous.candidates.push(...range.candidates);
      } else {
        ranges.push({ start: range.start, end: range.end, candidates: [...range.candidates] });
      }
    }
    for (const range of ranges) {
      const text = numberedLines(state.lines.slice(range.start - 1, range.end), range.start);
      const full = range.candidates.every((candidate) => candidate.full) && Buffer.byteLength(text, "utf8") <= MAX_CONTEXT_BLOCK_BYTES;
      let editTag: string | undefined;
      if (full && options.editSnapshots) {
        editTag = options.editSnapshots.record(state.absPath, state.text, { start: range.start, end: range.end }).tag;
        tags.set(relative, editTag);
        for (const candidate of range.candidates) editableMatches.add(matchKey(candidate.hit));
      }
      contexts.push({
        path: relative,
        start_line: range.start,
        end_line: range.end,
        total_lines: state.totalLines,
        match_indices: range.candidates
          .map((candidate) => matchIndex.get(matchKey(candidate.hit)))
          .filter((index): index is number => index !== undefined),
        match_lines: [...new Set(range.candidates.map((candidate) => candidate.hit.startLine))].sort((a, b) => a - b),
        text: full || Buffer.byteLength(text, "utf8") <= MAX_CONTEXT_BLOCK_BYTES ? text : truncateText(text, 4_000).value,
        editable: Boolean(editTag),
        ...(editTag ? { edit_tag: editTag } : {}),
        ...(!full ? { truncated: true } : {})
      });
    }
  }

  for (const candidate of selected.filter((item) => !item.verified || !item.state)) {
    contexts.push({
      path: candidate.hit.path,
      start_line: candidate.hit.startLine,
      end_line: candidate.hit.endLine,
      match_indices: [matchIndex.get(matchKey(candidate.hit)) ?? 0],
      match_lines: [candidate.hit.startLine],
      text: candidate.text,
      editable: false,
      truncated: true
    });
  }
  contexts.sort((left, right) => {
    const leftOrder = firstOrder.get(left.path) ?? Number.MAX_SAFE_INTEGER;
    const rightOrder = firstOrder.get(right.path) ?? Number.MAX_SAFE_INTEGER;
    return leftOrder - rightOrder || left.start_line - right.start_line;
  });

  const matches: AstGrepMatch[] = selected.map((candidate) => {
    const state = candidate.state;
    const verifiedText = state && candidate.verified
      ? state.buffer.subarray(candidate.hit.startByte, candidate.hit.endByte).toString("utf8")
      : candidate.hit.preview;
    const text = truncateText(verifiedText, MAX_MATCH_PREVIEW_CHARS).value;
    const editable = editableMatches.has(matchKey(candidate.hit));
    return {
      path: candidate.hit.path,
      language: candidate.hit.language,
      text,
      start_line: candidate.hit.startLine,
      start_column: candidate.hit.startColumn,
      end_line: candidate.hit.endLine,
      end_column: candidate.hit.endColumn,
      start_byte: candidate.hit.startByte,
      end_byte: candidate.hit.endByte,
      captures: candidate.hit.captures,
      editable,
      ...(editable && tags.get(candidate.hit.path) ? { edit_tag: tags.get(candidate.hit.path) } : {})
    };
  });

  return {
    selectedHits: selected.map((candidate) => candidate.hit),
    matches,
    contexts,
    outputLimited,
    warnings: boundedWarnings(warnings)
  };
}

function captureSummary(captures: AstGrepCapture[]): string | undefined {
  const useful = captures.filter((capture) => !/^[,;]$/.test(capture.text.trim())).slice(0, 8);
  if (!useful.length) return undefined;
  return useful.map((capture) => `$${capture.name}=${capture.text.replace(/\s+/g, " ").trim()}`).join(", ");
}

function renderAstGrepText(result: {
  mode: AstGrepMode;
  version: string;
  options: AstGrepOptions;
  matches: AstGrepMatch[];
  contexts: AstGrepContextBlock[];
  hasMore: boolean;
  nextCursor?: string;
  warnings: string[];
}): string {
  const lines = [
    "# AST Grep",
    "",
    `Provider: ast-grep ${result.version}`,
    `Mode: ${result.mode}`,
    `Language: ${result.options.language?.trim() || "inferred per file"}`,
    `Returned matches: ${result.matches.length}`,
    `Context: ${result.options.contextBefore} before / ${result.options.contextAfter} after`,
    ""
  ];
  if (!result.matches.length) lines.push("No structural matches.", "");
  if (result.matches.length) lines.push("## Matches", "");
  result.matches.forEach((match, index) => {
    const tag = match.edit_tag ? ` · edit_tag ${match.edit_tag}` : " · read-only";
    lines.push(
      `${index + 1}. ${match.path}:${match.start_line}:${match.start_column}-${match.end_line}:${match.end_column} · ${match.language}${tag}`
    );
    const captures = captureSummary(match.captures);
    if (captures) lines.push(`   Captures: ${captures}`);
  });
  if (result.matches.length) lines.push("");
  for (const block of result.contexts) {
    const tag = block.edit_tag ? ` · edit_tag ${block.edit_tag}` : " · read-only context";
    lines.push(
      `## ${block.path}:${block.start_line}-${block.end_line}${tag}`,
      "",
      "```text",
      block.text,
      "```",
      ""
    );
  }
  if (result.warnings.length) {
    lines.push("## Warnings", "", ...result.warnings.map((warning) => `- ${warning}`), "");
  }
  if (result.hasMore && result.nextCursor) {
    lines.push("More structural matches are available. Reuse the same arguments with:", "", `cursor: ${result.nextCursor}`);
  }
  return lines.join("\n").trim();
}

export async function astGrepWorkspace(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  rawOptions: Partial<AstGrepOptions>
): Promise<AstGrepResult> {
  const options: AstGrepOptions = {
    pattern: rawOptions.pattern,
    kind: rawOptions.kind,
    language: rawOptions.language,
    selector: rawOptions.selector,
    strictness: rawOptions.strictness,
    root: rawOptions.root ?? ".",
    globs: rawOptions.globs ?? [],
    includeHidden: Boolean(rawOptions.includeHidden),
    maxResults: Math.max(1, Math.min(rawOptions.maxResults ?? config.maxSearchResults, config.maxSearchResults)),
    contextBefore: Math.max(0, Math.min(rawOptions.contextBefore ?? 2, 20)),
    contextAfter: Math.max(0, Math.min(rawOptions.contextAfter ?? 2, 20)),
    groupByFile: rawOptions.groupByFile !== false,
    cursor: rawOptions.cursor,
    timeoutMs: Math.max(MIN_TIMEOUT_MS, Math.min(rawOptions.timeoutMs ?? 15_000, MAX_TIMEOUT_MS)),
    editSnapshots: rawOptions.editSnapshots
  };
  const mode = validateQuery(options);
  // Resolve the requested path through the guard before launching the native process.
  guard.resolve(workspace, options.root ?? ".");
  const executable = await resolveAstGrepExecutable();
  const providerVersion = await astGrepVersion(executable);
  const signature = signatureFor(workspace, options, mode, providerVersion);
  const offset = decodeCursor(options.cursor, signature);
  const processPage = await runAstGrepPage(config, guard, workspace, options, mode, executable, offset);
  const pageHits = processPage.hits.slice(0, options.maxResults);
  const loaded = await loadFileStates(config, guard, workspace, pageHits);
  const contextPage = await buildContextPage(config, options, pageHits, loaded.states);
  const hasMore = processPage.hasMore || contextPage.outputLimited || contextPage.selectedHits.length < pageHits.length;
  const nextOffset = offset + contextPage.selectedHits.length;
  const nextCursor = hasMore && contextPage.selectedHits.length ? encodeCursor(signature, nextOffset) : undefined;
  const warnings = boundedWarnings([
    ...processPage.warnings,
    ...loaded.warnings,
    ...contextPage.warnings,
    ...(contextPage.outputLimited ? ["The page stopped early to keep complete structural context within the output budget."] : [])
  ]);
  const text = renderAstGrepText({
    mode,
    version: providerVersion,
    options,
    matches: contextPage.matches,
    contexts: contextPage.contexts,
    hasMore,
    nextCursor,
    warnings
  });
  return {
    text,
    mode,
    provider: AST_GREP_PROVIDER,
    providerVersion,
    matches: contextPage.matches,
    contexts: contextPage.contexts,
    truncated: hasMore,
    hasMore,
    nextCursor,
    queryFingerprint: signature,
    warnings
  };
}
