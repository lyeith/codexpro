import path from "node:path";
import { CodexProError } from "./guard.js";
import { decodeGitQuotedPath } from "./gitPaths.js";

export interface NativeHunk { anchor?: string; lines: string[]; eof: boolean }
export interface NativeFile { op: "Add" | "Update" | "Delete"; path: string; move?: string; hunks: NativeHunk[]; added: string[] }

export function patchError(message: string, code = "patch_format_invalid"): never {
  throw new CodexProError(message, { code, retryUnchanged: false,
    recovery: { message: "Read the current target files and regenerate the patch. Do not retry unchanged." } });
}

export function isNativePatch(patch: string): boolean { return patch.trimStart().startsWith("*** Begin Patch"); }

export function parseNativePatch(patch: string): NativeFile[] {
  const lines = patch.trim().split(/\r?\n/);
  if (lines.shift() !== "*** Begin Patch" || lines.pop() !== "*** End Patch") patchError("Expected complete *** Begin Patch / *** End Patch markers.");
  const files: NativeFile[] = [];
  let file: NativeFile | undefined;
  let hunk: NativeHunk | undefined;
  for (const line of lines) {
    const header = /^\*\*\* (Add|Update|Delete) File: (.+)$/.exec(line);
    if (header) {
      file = { op: header[1] as NativeFile["op"], path: header[2], hunks: [], added: [] };
      files.push(file); hunk = undefined; continue;
    }
    if (!file) patchError("Patch content must follow a file header.");
    if (line.startsWith("*** Move to: ") && file.op === "Update" && !file.move && !file.hunks.length) { file.move = line.slice(13); continue; }
    if (file.op === "Add" && line.startsWith("+")) { file.added.push(line.slice(1)); continue; }
    if (file.op === "Update" && (line === "@@" || line.startsWith("@@ "))) {
      hunk = { anchor: line === "@@" ? undefined : line.slice(3), lines: [], eof: false }; file.hunks.push(hunk); continue;
    }
    if (file.op === "Update" && hunk && line === "*** End of File" && !hunk.eof) { hunk.eof = true; continue; }
    if (file.op === "Update" && hunk && !hunk.eof && /^[ +\-]/.test(line)) { hunk.lines.push(line); continue; }
    patchError(`Invalid ${file.op} patch content for ${file.path}.`);
  }
  if (!files.length) patchError("Patch has no file operations.");
  const targets = new Set<string>();
  for (const f of files) {
    if (f.op === "Update" && !f.hunks.length && !f.move) patchError(`No update hunks for ${f.path}.`);
    for (const p of [f.path, ...(f.move ? [f.move] : [])]) {
      if (!p.trim() || p.includes("\0") || targets.has(p)) patchError("Duplicate or invalid patch target.");
      targets.add(p);
    }
  }
  return files;
}

function gitPath(value: string, strip = true): string | undefined {
  const raw = value.split("\t")[0].trim();
  if (raw === "/dev/null") return undefined;
  const decoded = raw.startsWith('"') ? decodeGitQuotedPath(raw) : raw;
  if (!strip || path.isAbsolute(decoded) || path.win32.isAbsolute(decoded)) return decoded;
  const slash = decoded.indexOf("/");
  return slash < 0 ? decoded : decoded.slice(slash + 1);
}

/** Shared with audit: hunk payloads cannot masquerade as file headers. */
export function patchPaths(patch: unknown): string[] {
  if (typeof patch !== "string") return [];
  if (isNativePatch(patch)) return parseNativePatch(patch).flatMap(f => [f.path, ...(f.move ? [f.move] : [])]);
  const paths = new Set<string>();
  let old = 0, next = 0;
  for (const line of patch.split(/\r?\n/)) {
    if (old || next) {
      if (line[0] === " ") { old--; next--; }
      else if (line[0] === "-") old--;
      else if (line[0] === "+") next--;
      if (old < 0 || next < 0) patchError("Invalid unified hunk length.");
      continue;
    }
    if (line.startsWith("diff --git ")) {
      const header = line.slice(11);
      // Git leaves spaces unquoted. Enumerate delimiters outside C-quoted paths;
      // reject ambiguous headers rather than miss a mode-only/binary target.
      const pairs: string[][] = [];
      let quoted = false, escaped = false;
      for (let i = 0; i < header.length; i++) {
        const c = header[i];
        if (escaped) { escaped = false; continue; }
        if (quoted && c === "\\") { escaped = true; continue; }
        if (c === '"') { quoted = !quoted; continue; }
        if (c !== " " || quoted) continue;
        const a = header.slice(0, i), b = header.slice(i + 1);
        if (/^"?[^/\s"]*\//.test(a) && /^"?[^/\s"]*\//.test(b)) pairs.push([a, b]);
      }
      if (quoted || pairs.length !== 1) patchError("Ambiguous or invalid diff --git paths.");
      for (const value of pairs[0]) { const p = gitPath(value); if (p) paths.add(p); }
      continue;
    }
    const hunk = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/.exec(line);
    if (hunk) { old = Number(hunk[2] ?? 1); next = Number(hunk[4] ?? 1); continue; }
    if (line.startsWith("--- ") || line.startsWith("+++ ")) { const p = gitPath(line.slice(4)); if (p) paths.add(p); }
    const move = /^(?:rename|copy) (?:from|to) (.+)$/.exec(line);
    if (move) { const p = gitPath(move[1], false); if (p) paths.add(p); }
    if (line.startsWith("*** ")) patchError("Cannot mix native patch markers and Git diffs.");
  }
  if (old || next) patchError("Incomplete unified hunk.");
  return [...paths];
}
