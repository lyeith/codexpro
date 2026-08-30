import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import fsp from "node:fs/promises";
import path from "node:path";
import { minimatch } from "minimatch";
import type { CodexProConfig } from "./config.js";
import {
  inferConfigQueryFormat,
  queryConfigText,
  type ConfigQueryFormat
} from "./configQuery.js";
import type { Workspace } from "./guard.js";
import { CodexProError, PathGuard } from "./guard.js";
import { EditSnapshotStore, listFiles, textScanByteLimit } from "./fsOps.js";
import { redactSensitiveText } from "./redact.js";
import { searchWorkspaceStructured, type AnalysisSearchIntent, type StructuredSearchResult } from "./analysis/index.js";

export type SearchKind = "text" | "config";
export type SearchScope = "workspace" | "changed_files" | "diff_added" | "diff_removed";
export type SearchDiffTarget = "worktree" | "staged" | "head";

export interface SearchOptions {
  query: string;
  regex: boolean;
  root?: string;
  glob?: string;
  includeHidden: boolean;
  maxResults: number;
  intent?: AnalysisSearchIntent;
  symbol?: string;
  includeTests?: boolean;
  contextBefore: number;
  contextAfter: number;
  groupByFile: boolean;
  cursor?: string;
  kind: SearchKind;
  configFormat: ConfigQueryFormat;
  scope: SearchScope;
  baseRef?: string;
  diffTarget: SearchDiffTarget;
  includeUntracked: boolean;
  editSnapshots?: EditSnapshotStore;
}

export interface SearchMatch {
  path: string;
  line: number;
  end_line?: number;
  text: string;
  source: "text" | "config" | "diff_added" | "diff_removed";
  address?: string;
  editable: boolean;
  edit_tag?: string;
}

export interface SearchContextBlock {
  path: string;
  start_line: number;
  end_line: number;
  total_lines?: number;
  match_lines: number[];
  text: string;
  source: SearchMatch["source"];
  editable: boolean;
  edit_tag?: string;
  truncated?: boolean;
}

export interface SearchResult {
  text: string;
  matches: SearchMatch[];
  contexts: SearchContextBlock[];
  truncated: boolean;
  hasMore: boolean;
  nextCursor?: string;
  queryFingerprint: string;
  used: "ripgrep" | "node" | "config" | "git-diff";
  scope: SearchScope;
  kind: SearchKind;
  warnings: string[];
  analysis?: StructuredSearchResult;
}

interface RawSearchHit {
  path: string;
  line: number;
  endLine: number;
  text: string;
  source: SearchMatch["source"];
  editable: boolean;
  address?: string;
  configFormat?: Exclude<ConfigQueryFormat, "auto">;
}

interface RawSearchPage {
  hits: RawSearchHit[];
  hasMore: boolean;
  used: SearchResult["used"];
  warnings: string[];
}

interface SearchCursorPayload {
  v: 1;
  signature: string;
  after: string;
  checksum: string;
}

interface FileContextState {
  absPath: string;
  text: string;
  lines: string[];
  totalLines: number;
}

const SEARCH_CURSOR_VERSION = 1;
const MAX_CURSOR_CHARS = 4096;
const MAX_CONTEXT_LINES_PER_MATCH = 80;
const MAX_CONTEXT_BLOCK_BYTES = 128 * 1024;
const MAX_CONFIG_FILES = 2_000;
const MAX_DIFF_MATCHES = 100_000;
const MAX_WARNING_COUNT = 30;

function digest(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

function truncateLine(line: string, max = 400): string {
  if (line.length <= max) return line;
  return `${line.slice(0, max)}…`;
}

function splitLines(text: string): string[] {
  return text.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n");
}

function numberedLines(lines: string[], startLine: number): string {
  const width = String(startLine + Math.max(0, lines.length - 1)).length;
  return lines.map((line, index) => `${String(startLine + index).padStart(width, " ")} | ${line}`).join("\n");
}

function commandExists(command: string): Promise<boolean> {
  return new Promise((resolve) => {
    const child = process.platform === "win32"
      ? spawn("where", [command], { stdio: "ignore", shell: false })
      : spawn("/bin/sh", ["-lc", `command -v ${command} >/dev/null 2>&1`], { stdio: "ignore" });
    child.on("close", (code) => resolve(code === 0));
    child.on("error", () => resolve(false));
  });
}

function hitKey(hit: RawSearchHit): string {
  const line = String(hit.line).padStart(12, "0");
  const endLine = String(hit.endLine).padStart(12, "0");
  const address = hit.address ?? "";
  return `${hit.path}\0${line}\0${endLine}\0${hit.source}\0${address}`;
}

function sortHits(hits: RawSearchHit[]): RawSearchHit[] {
  return hits.sort((left, right) => hitKey(left).localeCompare(hitKey(right)));
}

function searchSignature(workspace: Workspace, options: SearchOptions): string {
  return digest(JSON.stringify({
    version: SEARCH_CURSOR_VERSION,
    workspace_id: workspace.id,
    query: options.query,
    regex: options.regex,
    root: options.root ?? ".",
    glob: options.glob ?? "",
    include_hidden: options.includeHidden,
    kind: options.kind,
    config_format: options.configFormat,
    scope: options.scope,
    base_ref: options.baseRef ?? "HEAD",
    diff_target: options.diffTarget,
    include_untracked: options.includeUntracked,
    context_before: options.contextBefore,
    context_after: options.contextAfter,
    group_by_file: options.groupByFile
  }));
}

function cursorChecksum(signature: string, after: string): string {
  return digest(`codexpro-search-cursor-v1\0${signature}\0${after}`).slice(0, 20);
}

function encodeCursor(signature: string, after: string): string {
  const payload: SearchCursorPayload = {
    v: SEARCH_CURSOR_VERSION,
    signature,
    after,
    checksum: cursorChecksum(signature, after)
  };
  return Buffer.from(JSON.stringify(payload), "utf8").toString("base64url");
}

function decodeCursor(value: string | undefined, signature: string): string | undefined {
  if (!value) return undefined;
  if (value.length > MAX_CURSOR_CHARS) {
    throw new CodexProError("Search cursor is too large. Restart the search without cursor.", {
      code: "search_cursor_invalid",
      retryUnchanged: false,
      recovery: { tool: "search", message: "Restart this search without cursor." }
    });
  }
  try {
    const parsed = JSON.parse(Buffer.from(value, "base64url").toString("utf8")) as Partial<SearchCursorPayload>;
    if (
      parsed.v !== SEARCH_CURSOR_VERSION ||
      parsed.signature !== signature ||
      typeof parsed.after !== "string" ||
      parsed.checksum !== cursorChecksum(signature, parsed.after)
    ) {
      throw new Error("cursor mismatch");
    }
    return parsed.after;
  } catch {
    throw new CodexProError("Search cursor does not belong to this exact query and scope. Restart without cursor.", {
      code: "search_cursor_mismatch",
      retryUnchanged: false,
      recovery: { tool: "search", message: "Restart this search without cursor and reuse only the next_cursor it returns." }
    });
  }
}

function boundedWarnings(values: string[]): string[] {
  return [...new Set(values.map((value) => redactSensitiveText(value)).filter(Boolean))].slice(0, MAX_WARNING_COUNT);
}

function makeMatcher(query: string, regex: boolean): (line: string) => boolean {
  if (!regex) return (line) => line.includes(query);
  let expression: RegExp;
  try {
    let source = query;
    const flags = new Set<string>();
    let inlineFlags = /^\(\?([ims]+)\)/.exec(source);
    while (inlineFlags) {
      for (const flag of inlineFlags[1]) flags.add(flag);
      source = source.slice(inlineFlags[0].length);
      inlineFlags = /^\(\?([ims]+)\)/.exec(source);
    }
    expression = new RegExp(source, [...flags].join(""));
  } catch (error) {
    throw new CodexProError(`Invalid regular expression: ${error instanceof Error ? error.message : String(error)}`);
  }
  return (line) => {
    expression.lastIndex = 0;
    return expression.test(line);
  };
}

function relativeSearchPath(workspace: Workspace, absolutePath: string): string | undefined {
  const rel = path.relative(workspace.root, path.resolve(absolutePath)).split(path.sep).join("/");
  if (!rel || rel === ".") return ".";
  if (rel === ".." || rel.startsWith("../") || path.isAbsolute(rel)) return undefined;
  return rel;
}

async function runRipgrep(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  options: SearchOptions,
  afterKey: string | undefined,
  allowedPaths?: Set<string>
): Promise<RawSearchPage> {
  if (allowedPaths && allowedPaths.size === 0) {
    return { hits: [], hasMore: false, used: "ripgrep", warnings: [] };
  }
  const target = guard.resolve(workspace, options.root ?? ".");
  const args = [
    "--json",
    "--line-number",
    "--with-filename",
    "--no-heading",
    "--color=never",
    "--sort",
    "path",
    "--max-columns",
    "4000",
    "--max-filesize",
    String(textScanByteLimit(config))
  ];
  if (!options.regex) args.push("--fixed-strings");
  if (options.includeHidden) args.push("--hidden");
  for (const glob of config.blockedGlobs) args.push("-g", `!${glob}`);
  if (options.glob) args.push("-g", options.glob);
  args.push("-e", options.query, "--", target.absPath);

  return new Promise((resolve, reject) => {
    const child = spawn("rg", args, {
      cwd: workspace.root,
      env: { ...process.env, NO_COLOR: "1" },
      windowsHide: true
    });
    const hits: RawSearchHit[] = [];
    const warnings: string[] = [];
    let stderr = "";
    let carry = "";
    let stoppedForPage = false;
    let outputLimited = false;
    const wanted = options.maxResults + 1;

    const processLine = (line: string) => {
      if (!line || stoppedForPage) return;
      let value: any;
      try {
        value = JSON.parse(line);
      } catch (error) {
        if (outputLimited) return;
        reject(new CodexProError(`ripgrep returned malformed JSON: ${error instanceof Error ? error.message : String(error)}`));
        child.kill("SIGTERM");
        return;
      }
      if (value.type !== "match") return;
      const absPath = path.resolve(String(value.data?.path?.text ?? ""));
      const rel = relativeSearchPath(workspace, absPath);
      if (!rel || guard.isBlockedRelativePath(rel)) return;
      if (allowedPaths && !allowedPaths.has(rel)) return;
      const lineNumber = Number(value.data?.line_number ?? 0);
      if (!Number.isSafeInteger(lineNumber) || lineNumber < 1) return;
      const lineText = String(value.data?.lines?.text ?? "").replace(/\r?\n$/, "");
      const hit: RawSearchHit = {
        path: rel,
        line: lineNumber,
        endLine: lineNumber,
        text: lineText,
        source: "text",
        editable: true
      };
      if (afterKey && hitKey(hit).localeCompare(afterKey) <= 0) return;
      hits.push(hit);
      if (hits.length >= wanted) {
        stoppedForPage = true;
        child.kill("SIGTERM");
      }
    };

    child.stdout.on("data", (chunk) => {
      carry += String(chunk);
      if (Buffer.byteLength(carry, "utf8") > Math.max(2_000_000, config.maxOutputBytes)) {
        outputLimited = true;
        warnings.push("Search stopped because one ripgrep event exceeded the output safety budget.");
        child.kill("SIGTERM");
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
      stderr += String(chunk);
    });
    child.on("error", reject);
    child.on("close", (code) => {
      if (carry && !stoppedForPage) processLine(carry);
      if (code && code > 1 && !stoppedForPage && !outputLimited) {
        reject(new CodexProError(stderr.trim() || `ripgrep failed with exit code ${code}`));
        return;
      }
      resolve({
        hits: hits.slice(0, wanted),
        hasMore: hits.length > options.maxResults || stoppedForPage || outputLimited,
        used: "ripgrep",
        warnings
      });
    });
  });
}

async function runNodeSearch(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  options: SearchOptions,
  afterKey: string | undefined,
  allowedPaths?: Set<string>
): Promise<RawSearchPage> {
  if (allowedPaths && allowedPaths.size === 0) {
    return { hits: [], hasMore: false, used: "node", warnings: [] };
  }
  const files = (await listFiles(guard, workspace, {
    root: options.root,
    glob: options.glob,
    includeHidden: options.includeHidden,
    maxFiles: 20_000
  })).sort();
  const matcher = makeMatcher(options.query, options.regex);
  const hits: RawSearchHit[] = [];
  const warnings: string[] = [];
  if (options.regex) warnings.push("ripgrep was unavailable; regex search used the bounded JavaScript fallback.");
  const scanBytes = textScanByteLimit(config);
  const wanted = options.maxResults + 1;

  outer: for (const rel of files) {
    if (allowedPaths && !allowedPaths.has(rel)) continue;
    const resolved = guard.resolve(workspace, rel);
    try {
      const stat = await fsp.stat(resolved.absPath);
      if (stat.size > scanBytes) continue;
      const buffer = await fsp.readFile(resolved.absPath);
      if (buffer.includes(0)) continue;
      const lines = splitLines(buffer.toString("utf8"));
      for (let index = 0; index < lines.length; index += 1) {
        const line = lines[index];
        if (!matcher(line)) continue;
        const hit: RawSearchHit = {
          path: rel,
          line: index + 1,
          endLine: index + 1,
          text: line,
          source: "text",
          editable: true
        };
        if (afterKey && hitKey(hit).localeCompare(afterKey) <= 0) continue;
        hits.push(hit);
        if (hits.length >= wanted) break outer;
      }
    } catch {
      // Skip unreadable files.
    }
  }
  return {
    hits,
    hasMore: hits.length > options.maxResults,
    used: "node",
    warnings
  };
}

async function runConfigSearch(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  options: SearchOptions,
  afterKey: string | undefined,
  allowedPaths?: Set<string>
): Promise<RawSearchPage> {
  if (allowedPaths && allowedPaths.size === 0) {
    return { hits: [], hasMore: false, used: "config", warnings: [] };
  }
  let files = (await listFiles(guard, workspace, {
    root: options.root,
    glob: options.glob,
    includeHidden: options.includeHidden,
    maxFiles: 20_000
  })).sort();
  if (allowedPaths) files = files.filter((file) => allowedPaths.has(file));
  if (options.configFormat === "auto") {
    files = files.filter((file) => inferConfigQueryFormat(file, "auto") !== undefined);
  }
  const warnings: string[] = [];
  if (files.length > MAX_CONFIG_FILES) {
    warnings.push(`Configuration query examined only the first ${MAX_CONFIG_FILES} matching files.`);
    files = files.slice(0, MAX_CONFIG_FILES);
  }
  const hits: RawSearchHit[] = [];
  const wanted = options.maxResults + 1;
  const scanBytes = textScanByteLimit(config);

  outer: for (const rel of files) {
    const format = inferConfigQueryFormat(rel, options.configFormat);
    if (!format) continue;
    const resolved = guard.resolve(workspace, rel);
    try {
      const stat = await fsp.stat(resolved.absPath);
      if (!stat.isFile() || stat.size > scanBytes) continue;
      const buffer = await fsp.readFile(resolved.absPath);
      if (buffer.includes(0)) continue;
      const result = queryConfigText(buffer.toString("utf8"), format, options.query);
      warnings.push(...result.warnings.map((warning) => `${rel}: ${warning}`));
      for (const match of result.matches) {
        const hit: RawSearchHit = {
          path: rel,
          line: match.line,
          endLine: match.endLine,
          text: match.text,
          source: "config",
          editable: true,
          address: match.address,
          configFormat: format
        };
        if (afterKey && hitKey(hit).localeCompare(afterKey) <= 0) continue;
        hits.push(hit);
        if (hits.length >= wanted) break outer;
      }
    } catch (error) {
      warnings.push(`${rel}: ${error instanceof Error ? error.message : String(error)}`);
    }
  }

  return {
    hits: sortHits(hits),
    hasMore: hits.length > options.maxResults,
    used: "config",
    warnings: boundedWarnings(warnings)
  };
}

function validateGitRef(value: string): string {
  const ref = value.trim();
  if (!ref || ref.startsWith("-") || /[\0-\x20\x7f]/.test(ref)) {
    throw new CodexProError("base_ref must be a non-empty Git ref without whitespace, control characters, or a leading dash.");
  }
  return ref;
}

function runGit(
  workspace: Workspace,
  args: string[],
  maxBuffer: number
): { ok: boolean; stdout: string; truncated: boolean; error: string } {
  const result = spawnSync("git", ["-c", "core.quotePath=false", ...args], {
    cwd: workspace.root,
    encoding: "utf8",
    maxBuffer,
    timeout: 10_000,
    env: {
      ...process.env,
      GIT_OPTIONAL_LOCKS: "0",
      GIT_TERMINAL_PROMPT: "0",
      LC_ALL: "C",
      NO_COLOR: "1"
    },
    windowsHide: true
  });
  const stdout = typeof result.stdout === "string" ? result.stdout : "";
  const stderr = typeof result.stderr === "string" ? result.stderr.trim() : "";
  const code = result.error && typeof result.error === "object" && "code" in result.error
    ? String(result.error.code)
    : "";
  return {
    ok: !result.error && result.status === 0,
    stdout,
    truncated: code === "ENOBUFS" || Buffer.byteLength(stdout, "utf8") >= maxBuffer,
    error: stderr || (result.error instanceof Error ? result.error.message : "")
  };
}

function assertGitDiffAvailable(workspace: Workspace, baseRef: string): void {
  const inside = runGit(workspace, ["rev-parse", "--is-inside-work-tree"], 16 * 1024);
  if (!inside.ok || inside.stdout.trim() !== "true") {
    throw new CodexProError("Diff-aware search requires a Git working tree.");
  }
  const verified = runGit(workspace, ["rev-parse", "--verify", `${baseRef}^{commit}`], 16 * 1024);
  if (!verified.ok) {
    throw new CodexProError(`base_ref does not resolve to a commit: ${baseRef}`);
  }
}

function gitDiffSelector(options: SearchOptions, baseRef: string): string[] {
  if (options.diffTarget === "staged") return ["diff", "--cached", baseRef];
  if (options.diffTarget === "head") return ["diff", `${baseRef}...HEAD`];
  return ["diff", baseRef];
}

function pathAllowedForSearch(
  guard: PathGuard,
  options: SearchOptions,
  relPath: string,
  rootRel: string
): boolean {
  const normalized = relPath.split(path.sep).join("/").replace(/^\.\//, "");
  if (!normalized || normalized === "." || normalized === ".." || normalized.startsWith("../")) return false;
  if (guard.isBlockedRelativePath(normalized)) return false;
  if (!options.includeHidden && normalized.split("/").some((part) => part.startsWith("."))) return false;
  if (rootRel !== "." && normalized !== rootRel && !normalized.startsWith(`${rootRel}/`)) return false;
  if (options.glob && !minimatch(normalized, options.glob, { dot: true })) return false;
  return true;
}

function splitNul(value: string): string[] {
  return value.split("\0").filter(Boolean);
}

async function changedGitPaths(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  options: SearchOptions
): Promise<{ paths: Set<string>; warnings: string[] }> {
  const baseRef = validateGitRef(options.baseRef ?? "HEAD");
  assertGitDiffAvailable(workspace, baseRef);
  const root = guard.resolve(workspace, options.root ?? ".");
  const rootRel = root.relPath;
  const selector = gitDiffSelector(options, baseRef);
  const result = runGit(workspace, [...selector, "--name-only", "-z", "--", rootRel], Math.max(4 * 1024 * 1024, config.maxOutputBytes * 4));
  if (!result.ok && !result.truncated) throw new CodexProError(result.error || "Git changed-file search failed.");
  const warnings = result.truncated ? ["Changed-file list was truncated by the Git output budget."] : [];
  const candidates = splitNul(result.stdout);
  if (options.diffTarget === "worktree" && options.includeUntracked) {
    const untracked = runGit(workspace, ["ls-files", "--others", "--exclude-standard", "-z", "--", rootRel], Math.max(4 * 1024 * 1024, config.maxOutputBytes * 4));
    if (untracked.ok || untracked.truncated) candidates.push(...splitNul(untracked.stdout));
    if (untracked.truncated) warnings.push("Untracked-file list was truncated by the Git output budget.");
  }
  return {
    paths: new Set(candidates.filter((candidate) => pathAllowedForSearch(guard, options, candidate, rootRel))),
    warnings
  };
}

function parseDiffPath(value: string): string {
  const clean = value.trim();
  if (clean === "/dev/null") return clean;
  if (clean.startsWith('"') && clean.endsWith('"')) {
    try {
      return (JSON.parse(clean) as string).replace(/^[ab]\//, "");
    } catch {
      return clean.slice(1, -1).replace(/^[ab]\//, "");
    }
  }
  return clean.replace(/^[ab]\//, "");
}

async function runGitDiffSearch(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  options: SearchOptions,
  afterKey: string | undefined
): Promise<RawSearchPage> {
  const baseRef = validateGitRef(options.baseRef ?? "HEAD");
  assertGitDiffAvailable(workspace, baseRef);
  const root = guard.resolve(workspace, options.root ?? ".");
  const rootRel = root.relPath;
  const matcher = makeMatcher(options.query, options.regex);
  const maxBuffer = Math.max(8 * 1024 * 1024, Math.min(64 * 1024 * 1024, config.maxOutputBytes * 16));
  const selector = gitDiffSelector(options, baseRef);
  const result = runGit(workspace, [...selector, "--no-color", "--no-ext-diff", "--unified=0", "--", rootRel], maxBuffer);
  if (!result.ok && !result.truncated) throw new CodexProError(result.error || "Git diff search failed.");
  const warnings = result.truncated ? ["Git diff was truncated by the diff-search byte budget."] : [];
  const hits: RawSearchHit[] = [];
  let oldPath = "";
  let newPath = "";
  let oldLine = 0;
  let newLine = 0;
  let inHunk = false;
  let matchCapReached = false;

  const add = (hit: RawSearchHit) => {
    if (!pathAllowedForSearch(guard, options, hit.path, rootRel)) return;
    if (!matcher(hit.text)) return;
    hits.push(hit);
    if (hits.length >= MAX_DIFF_MATCHES) matchCapReached = true;
  };

  for (const line of result.stdout.replace(/\r\n/g, "\n").split("\n")) {
    if (matchCapReached) break;
    if (line.startsWith("--- ")) {
      oldPath = parseDiffPath(line.slice(4));
      inHunk = false;
      continue;
    }
    if (line.startsWith("+++ ")) {
      newPath = parseDiffPath(line.slice(4));
      inHunk = false;
      continue;
    }
    const header = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line);
    if (header) {
      oldLine = Number(header[1]);
      newLine = Number(header[2]);
      inHunk = true;
      continue;
    }
    if (!inHunk || !line) continue;
    if (line.startsWith("-") && !line.startsWith("---")) {
      if (options.scope === "diff_removed" && oldPath && oldPath !== "/dev/null") {
        add({
          path: oldPath,
          line: oldLine,
          endLine: oldLine,
          text: line.slice(1),
          source: "diff_removed",
          editable: false
        });
      }
      oldLine += 1;
      continue;
    }
    if (line.startsWith("+") && !line.startsWith("+++")) {
      if (options.scope === "diff_added" && newPath && newPath !== "/dev/null") {
        add({
          path: newPath,
          line: newLine,
          endLine: newLine,
          text: line.slice(1),
          source: "diff_added",
          editable: true
        });
      }
      newLine += 1;
      continue;
    }
    if (line.startsWith(" ")) {
      oldLine += 1;
      newLine += 1;
    }
  }

  if (matchCapReached) warnings.push(`Diff search stopped after ${MAX_DIFF_MATCHES} matching lines.`);

  if (options.scope === "diff_added" && options.diffTarget === "worktree" && options.includeUntracked && !matchCapReached) {
    const untracked = runGit(workspace, ["ls-files", "--others", "--exclude-standard", "-z", "--", rootRel], maxBuffer);
    if (untracked.ok || untracked.truncated) {
      for (const rel of splitNul(untracked.stdout).sort()) {
        if (matchCapReached || !pathAllowedForSearch(guard, options, rel, rootRel)) continue;
        try {
          const resolved = guard.resolve(workspace, rel);
          const stat = await fsp.stat(resolved.absPath);
          if (!stat.isFile() || stat.size > textScanByteLimit(config)) continue;
          const buffer = await fsp.readFile(resolved.absPath);
          if (buffer.includes(0)) continue;
          const lines = splitLines(buffer.toString("utf8"));
          for (let index = 0; index < lines.length; index += 1) {
            if (!matcher(lines[index])) continue;
            hits.push({
              path: rel,
              line: index + 1,
              endLine: index + 1,
              text: lines[index],
              source: "diff_added",
              editable: true
            });
            if (hits.length >= MAX_DIFF_MATCHES) {
              matchCapReached = true;
              break;
            }
          }
        } catch {
          // Ignore an untracked file that disappeared during the search.
        }
      }
    }
    if (untracked.truncated) warnings.push("Untracked diff search was truncated by the Git output budget.");
  }

  const ordered = sortHits(hits).filter((hit) => !afterKey || hitKey(hit).localeCompare(afterKey) > 0);
  const page = ordered.slice(0, options.maxResults + 1);
  return {
    hits: page,
    hasMore: ordered.length > options.maxResults || result.truncated || matchCapReached,
    used: "git-diff",
    warnings: boundedWarnings(warnings)
  };
}

async function primarySearch(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  options: SearchOptions,
  afterKey: string | undefined
): Promise<RawSearchPage> {
  if (options.scope === "diff_added" || options.scope === "diff_removed") {
    if (options.kind !== "text") {
      throw new CodexProError("diff_added and diff_removed scopes support text search only. Use changed_files for configuration queries.");
    }
    return runGitDiffSearch(config, guard, workspace, options, afterKey);
  }

  let allowedPaths: Set<string> | undefined;
  const warnings: string[] = [];
  if (options.scope === "changed_files") {
    const changed = await changedGitPaths(config, guard, workspace, options);
    allowedPaths = changed.paths;
    warnings.push(...changed.warnings);
  }

  let page: RawSearchPage;
  if (options.kind === "config") {
    page = await runConfigSearch(config, guard, workspace, options, afterKey, allowedPaths);
  } else if (await commandExists("rg")) {
    page = await runRipgrep(config, guard, workspace, options, afterKey, allowedPaths);
  } else {
    page = await runNodeSearch(config, guard, workspace, options, afterKey, allowedPaths);
  }
  page.warnings = boundedWarnings([...warnings, ...page.warnings]);
  return page;
}

async function loadContextFiles(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  hits: RawSearchHit[]
): Promise<{ files: Map<string, FileContextState>; warnings: string[] }> {
  const files = new Map<string, FileContextState>();
  const warnings: string[] = [];
  for (const rel of [...new Set(hits.filter((hit) => hit.editable).map((hit) => hit.path))]) {
    try {
      const resolved = guard.resolve(workspace, rel);
      await guard.assertTextFile(resolved.absPath, textScanByteLimit(config));
      const buffer = await fsp.readFile(resolved.absPath);
      if (buffer.includes(0)) throw new Error("binary file");
      const text = buffer.toString("utf8");
      const lines = splitLines(text);
      files.set(rel, { absPath: resolved.absPath, text, lines, totalLines: lines.length });
    } catch (error) {
      warnings.push(`${rel}: current file could not establish edit provenance (${error instanceof Error ? error.message : String(error)}).`);
    }
  }
  return { files, warnings };
}

function hitStillMatches(hit: RawSearchHit, state: FileContextState, options: SearchOptions): boolean {
  if (hit.line < 1 || hit.line > state.totalLines) return false;
  if (hit.source === "config") {
    if (!hit.configFormat || !hit.address) return false;
    try {
      return queryConfigText(state.text, hit.configFormat, options.query).matches.some((match) =>
        match.address === hit.address && match.line === hit.line
      );
    } catch {
      return false;
    }
  }
  return makeMatcher(options.query, options.regex)(state.lines[hit.line - 1] ?? "");
}

function contextRange(hit: RawSearchHit, state: FileContextState, options: SearchOptions): { start: number; end: number; capped: boolean } {
  const requestedEnd = Math.max(hit.line, hit.endLine);
  const cappedMatchEnd = Math.min(requestedEnd, hit.line + MAX_CONTEXT_LINES_PER_MATCH - 1);
  return {
    start: Math.max(1, hit.line - options.contextBefore),
    end: Math.min(state.totalLines, cappedMatchEnd + options.contextAfter),
    capped: requestedEnd > cappedMatchEnd
  };
}

function previewBlock(hit: RawSearchHit): SearchContextBlock {
  return {
    path: hit.path,
    start_line: hit.line,
    end_line: hit.endLine,
    match_lines: [hit.line],
    text: `${hit.line} | ${truncateLine(hit.text, 2_000)}`,
    source: hit.source,
    editable: false,
    truncated: hit.text.length > 2_000 || undefined
  };
}

async function buildContexts(
  config: CodexProConfig,
  options: SearchOptions,
  rawHits: RawSearchHit[],
  files: Map<string, FileContextState>
): Promise<{
  hits: RawSearchHit[];
  matches: SearchMatch[];
  contexts: SearchContextBlock[];
  outputLimited: boolean;
  warnings: string[];
}> {
  const warnings: string[] = [];
  const contextBudget = Math.max(16 * 1024, Math.floor(config.maxOutputBytes * 0.65));
  let estimatedBytes = 0;
  const selected: RawSearchHit[] = [];
  let outputLimited = false;

  for (const hit of rawHits) {
    if (!hit.editable) {
      const cost = Buffer.byteLength(hit.text, "utf8") + 256;
      if (selected.length && estimatedBytes + cost > contextBudget) {
        outputLimited = true;
        break;
      }
      selected.push(hit);
      estimatedBytes += Math.min(cost, 4_096);
      continue;
    }
    const state = files.get(hit.path);
    if (!state || !hitStillMatches(hit, state, options)) {
      hit.editable = false;
      warnings.push(`${hit.path}:${hit.line}: file changed during search; this match has no edit tag.`);
      selected.push(hit);
      estimatedBytes += Math.min(Buffer.byteLength(hit.text, "utf8") + 256, 4_096);
      continue;
    }
    const range = contextRange(hit, state, options);
    if (range.capped) warnings.push(`${hit.path}:${hit.line}: matched configuration span was capped to ${MAX_CONTEXT_LINES_PER_MATCH} lines for display.`);
    const text = numberedLines(state.lines.slice(range.start - 1, range.end), range.start);
    const bytes = Buffer.byteLength(text, "utf8") + 256;
    if (selected.length && estimatedBytes + Math.min(bytes, MAX_CONTEXT_BLOCK_BYTES) > contextBudget) {
      outputLimited = true;
      break;
    }
    selected.push(hit);
    estimatedBytes += Math.min(bytes, MAX_CONTEXT_BLOCK_BYTES);
  }

  type Range = { start: number; end: number; hits: RawSearchHit[] };
  const rangesByFile = new Map<string, Range[]>();
  for (const hit of selected.filter((candidate) => candidate.editable)) {
    const state = files.get(hit.path);
    if (!state) continue;
    const range = contextRange(hit, state, options);
    const ranges = rangesByFile.get(hit.path) ?? [];
    ranges.push({ start: range.start, end: range.end, hits: [hit] });
    rangesByFile.set(hit.path, ranges);
  }

  const contexts: SearchContextBlock[] = [];
  const tags = new Map<string, string>();
  const editableHitKeys = new Set<string>();
  const firstHitOrder = new Map(selected.map((hit, index) => [hit.path, index]));
  const fileEntries = [...rangesByFile.entries()].sort((left, right) =>
    (firstHitOrder.get(left[0]) ?? 0) - (firstHitOrder.get(right[0]) ?? 0)
  );

  for (const [rel, rawRanges] of fileEntries) {
    const state = files.get(rel);
    if (!state) continue;
    const ordered = rawRanges.sort((left, right) => left.start - right.start || left.end - right.end);
    const ranges: Range[] = [];
    for (const range of ordered) {
      const previous = ranges[ranges.length - 1];
      if (options.groupByFile && previous && range.start <= previous.end + 1) {
        previous.end = Math.max(previous.end, range.end);
        previous.hits.push(...range.hits);
      } else {
        ranges.push({ start: range.start, end: range.end, hits: [...range.hits] });
      }
    }
    for (const range of ranges) {
      const text = numberedLines(state.lines.slice(range.start - 1, range.end), range.start);
      const bytes = Buffer.byteLength(text, "utf8");
      const full = bytes <= MAX_CONTEXT_BLOCK_BYTES;
      let editTag: string | undefined;
      if (full && options.editSnapshots) {
        editTag = options.editSnapshots.record(state.absPath, state.text, { start: range.start, end: range.end }).tag;
        tags.set(rel, editTag);
        for (const hit of range.hits) editableHitKeys.add(hitKey(hit));
      } else if (!full) {
        warnings.push(`${rel}:${range.start}-${range.end}: context exceeded ${MAX_CONTEXT_BLOCK_BYTES} bytes, so edit provenance was not granted.`);
      }
      contexts.push({
        path: rel,
        start_line: range.start,
        end_line: range.end,
        total_lines: state.totalLines,
        match_lines: [...new Set(range.hits.map((hit) => hit.line))].sort((a, b) => a - b),
        text: full ? text : truncateLine(text, 4_000),
        source: range.hits[0].source,
        editable: Boolean(editTag),
        edit_tag: editTag,
        truncated: !full || undefined
      });
    }
  }

  for (const hit of selected.filter((candidate) => !candidate.editable)) contexts.push(previewBlock(hit));
  contexts.sort((left, right) => {
    const leftOrder = firstHitOrder.get(left.path) ?? Number.MAX_SAFE_INTEGER;
    const rightOrder = firstHitOrder.get(right.path) ?? Number.MAX_SAFE_INTEGER;
    return leftOrder - rightOrder || left.start_line - right.start_line;
  });

  const matches: SearchMatch[] = selected.map((hit) => {
    const editable = hit.editable && editableHitKeys.has(hitKey(hit));
    return {
      path: hit.path,
      line: hit.line,
      ...(hit.endLine !== hit.line ? { end_line: hit.endLine } : {}),
      text: truncateLine(hit.text),
      source: hit.source,
      ...(hit.address ? { address: hit.address } : {}),
      editable,
      ...(editable && tags.get(hit.path) ? { edit_tag: tags.get(hit.path) } : {})
    };
  });

  return { hits: selected, matches, contexts, outputLimited, warnings: boundedWarnings(warnings) };
}

function renderSearchText(result: {
  options: SearchOptions;
  matches: SearchMatch[];
  contexts: SearchContextBlock[];
  hasMore: boolean;
  nextCursor?: string;
  warnings: string[];
}): string {
  const lines = [
    "# Search Files",
    "",
    `Kind: ${result.options.kind}`,
    `Scope: ${result.options.scope}`,
    `Returned matches: ${result.matches.length}`,
    `Context: ${result.options.contextBefore} before / ${result.options.contextAfter} after`,
    ""
  ];
  if (!result.matches.length) lines.push("No matches.");
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
    lines.push("More matches are available. Reuse the same arguments with:", "", `cursor: ${result.nextCursor}`);
  }
  return lines.join("\n").trim();
}

export async function searchWorkspace(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  rawOptions: Partial<SearchOptions>
): Promise<SearchResult> {
  const query = rawOptions.symbol?.toString() || rawOptions.query?.toString() || "";
  if (!query) throw new CodexProError("query is required.");
  const kind = rawOptions.kind ?? "text";
  const scope = rawOptions.scope ?? "workspace";
  const options: SearchOptions = {
    query,
    regex: Boolean(rawOptions.regex),
    root: rawOptions.root,
    glob: rawOptions.glob,
    includeHidden: Boolean(rawOptions.includeHidden),
    maxResults: Math.max(1, Math.min(rawOptions.maxResults ?? config.maxSearchResults, config.maxSearchResults)),
    intent: rawOptions.intent,
    symbol: rawOptions.symbol,
    includeTests: rawOptions.includeTests,
    contextBefore: Math.max(0, Math.min(rawOptions.contextBefore ?? 2, 20)),
    contextAfter: Math.max(0, Math.min(rawOptions.contextAfter ?? 2, 20)),
    groupByFile: rawOptions.groupByFile !== false,
    cursor: rawOptions.cursor,
    kind,
    configFormat: rawOptions.configFormat ?? "auto",
    scope,
    baseRef: rawOptions.baseRef,
    diffTarget: rawOptions.diffTarget ?? "worktree",
    includeUntracked: rawOptions.includeUntracked !== false,
    editSnapshots: rawOptions.editSnapshots
  };
  if (options.kind === "config" && options.regex) {
    throw new CodexProError("regex is not used by configuration queries. Use wildcards in the query path instead.");
  }
  if (options.kind === "config" && options.symbol) {
    throw new CodexProError("symbol cannot be combined with kind=config.");
  }
  const signature = searchSignature(workspace, options);
  const afterKey = decodeCursor(options.cursor, signature);
  const primary = await primarySearch(config, guard, workspace, options, afterKey);
  const pageHits = primary.hits.slice(0, options.maxResults);
  const loaded = await loadContextFiles(config, guard, workspace, pageHits);
  const contextual = await buildContexts(config, options, pageHits, loaded.files);
  const hasMore = primary.hasMore || contextual.outputLimited || contextual.hits.length < pageHits.length;
  const lastHit = contextual.hits[contextual.hits.length - 1];
  const nextCursor = hasMore && lastHit ? encodeCursor(signature, hitKey(lastHit)) : undefined;
  const warnings = boundedWarnings([
    ...primary.warnings,
    ...loaded.warnings,
    ...contextual.warnings,
    ...(contextual.outputLimited ? ["The page stopped early to keep complete context within the output budget."] : [])
  ]);

  let analysis: StructuredSearchResult | undefined;
  const structuredRequested = options.intent !== undefined || options.symbol !== undefined || options.includeTests !== undefined;
  if (structuredRequested && options.kind === "text" && options.scope === "workspace" && !options.cursor) {
    if (!config.analysisEnabled) {
      analysis = {
        schemaVersion: 1,
        query,
        intent: options.intent && options.intent !== "auto" ? options.intent : "text",
        groups: { definitions: [], references: [], tests: [], configuration: [], documentation: [], other: [] },
        matches: [],
        coverage: { inventoryFiles: 0, analyzedFiles: 0, scannedBytes: 0, symbolCount: 0, relationshipCount: 0, truncated: true, warnings: ["Repository analysis is disabled by configuration."] },
        warnings: ["Repository analysis is disabled by configuration."],
        cache: { hit: false, key: "disabled" }
      };
    } else {
      try {
        analysis = await searchWorkspaceStructured(config, guard, workspace, {
          query,
          intent: options.intent ?? "auto",
          includeTests: Boolean(options.includeTests),
          regex: options.regex,
          root: options.root,
          maxResults: options.maxResults
        });
      } catch (error) {
        analysis = {
          schemaVersion: 1,
          query,
          intent: options.intent && options.intent !== "auto" ? options.intent : "text",
          groups: { definitions: [], references: [], tests: [], configuration: [], documentation: [], other: [] },
          matches: [],
          coverage: { inventoryFiles: 0, analyzedFiles: 0, scannedBytes: 0, symbolCount: 0, relationshipCount: 0, truncated: true, warnings: [] },
          warnings: [`Repository analysis unavailable: ${redactSensitiveText(error instanceof Error ? error.message : String(error))}`],
          cache: { hit: false, key: "unavailable" }
        };
      }
    }
  }

  const text = renderSearchText({ options, matches: contextual.matches, contexts: contextual.contexts, hasMore, nextCursor, warnings });
  return {
    text,
    matches: contextual.matches,
    contexts: contextual.contexts,
    truncated: hasMore,
    hasMore,
    nextCursor,
    queryFingerprint: signature,
    used: primary.used,
    scope: options.scope,
    kind: options.kind,
    warnings,
    ...(analysis ? { analysis } : {})
  };
}
