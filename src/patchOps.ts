import fs from "node:fs";
import { spawnSync } from "node:child_process";
import type { CodexProConfig } from "./config.js";
import { PathGuard, CodexProError, type Workspace } from "./guard.js";
import { withFileWriteLocks } from "./fsOps.js";
import { hasSecretValue, redactSensitiveText, secretContentBlockedError } from "./redact.js";
import { assertWriteToolAllowed } from "./tools/shared.js";
import { isNativePatch, parseNativePatch, patchPaths, patchError, type NativeFile } from "./patchSyntax.js";

function fullDiff(name: string, before: string | null, after: string | null, mode = "100644"): string {
  if (before === after) return "";
  const a = JSON.stringify(`a/${name}`), b = JSON.stringify(`b/${name}`);
  const lines = (s: string | null) => s ? s.split("\n").slice(0, s.endsWith("\n") ? -1 : undefined) : [];
  const old = lines(before), next = lines(after);
  const body = (ls: string[], text: string | null, prefix: string) => ls.flatMap((line, i) =>
    i === ls.length - 1 && !text?.endsWith("\n") ? [prefix + line, "\\ No newline at end of file"] : [prefix + line]);
  return [`diff --git ${a} ${b}`, ...(before === null ? [`new file mode ${mode}`] : after === null ? [`deleted file mode ${mode}`] : []),
    `--- ${before === null ? "/dev/null" : a}`, `+++ ${after === null ? "/dev/null" : b}`,
    ...(old.length || next.length ? [`@@ -${old.length ? 1 : 0},${old.length} +${next.length ? 1 : 0},${next.length} @@`, ...body(old, before, "-"), ...body(next, after, "+")] : []), ""].join("\n");
}

function updated(file: NativeFile, text: string): string {
  const crlf = text.includes("\r\n");
  if (text.replace(/\r\n/g, "").includes("\r") || (crlf && text.replace(/\r\n/g, "").includes("\n"))) patchError("Use a Git diff for mixed line endings.");
  const ending = crlf ? "\r\n" : "\n";
  const lines = text.split(ending);
  const finalNewline = text.endsWith(ending);
  if (finalNewline || text === "") lines.pop();
  let position = 0;
  for (const hunk of file.hunks) {
    if (hunk.anchor) {
      const matches = lines.map((line, i) => i >= position && line === hunk.anchor ? i : -1).filter(i => i >= 0);
      if (matches.length !== 1) patchError(`Missing or ambiguous anchor in ${file.path}.`, "patch_context_stale");
      position = matches[0] + 1;
    }
    const old = hunk.lines.filter(l => l[0] !== "+").map(l => l.slice(1));
    const next = hunk.lines.filter(l => l[0] !== "-").map(l => l.slice(1));
    if (!hunk.lines.length) patchError("Empty update hunk.");
    const matches: number[] = [];
    for (let i = position; i <= lines.length - old.length; i++) {
      if ((!hunk.eof || i + old.length === lines.length) && old.every((line, j) => lines[i + j] === line)) matches.push(i);
    }
    if (matches.length !== 1) patchError(`Missing or ambiguous hunk context in ${file.path}.`, "patch_context_stale");
    lines.splice(matches[0], old.length, ...next); position = matches[0] + next.length;
  }
  return lines.join(ending) + (lines.length && finalNewline ? ending : "");
}

export async function applyWorkspacePatch(config: CodexProConfig, guard: PathGuard, workspace: Workspace, input: string) {
  if (!input.trim() || Buffer.byteLength(input) > config.maxWriteBytes) patchError("Patch is empty or exceeds the write byte limit.");
  if (hasSecretValue(input)) throw secretContentBlockedError("apply_patch", input);
  if (/^(?:(?:new|old|deleted) file mode|new mode|old mode) 120000\s*$/m.test(input)) patchError("Symlink patches are blocked from apply_patch.");
  const paths = patchPaths(input);
  if (!paths.length) patchError("Patch must contain file headers.");
  const resolve = (p: string) => { assertWriteToolAllowed(config, p); return guard.resolve(workspace, p, { forWrite: true }).absPath; };
  const targets = paths.map(resolve);
  if (new Set(targets).size !== targets.length) patchError("Duplicate canonical patch targets.");
  return withFileWriteLocks(targets, () => {
    paths.forEach(resolve);
    let patch = input;
    if (isNativePatch(input)) {
      let normalizedBytes = 0;
      const normalizedDiff = (...args: Parameters<typeof fullDiff>) => {
        const diff = fullDiff(...args); normalizedBytes += Buffer.byteLength(diff);
        if (normalizedBytes > config.maxWriteBytes * 8) patchError("Expanded native diff exceeds its bounded normalization budget. Use a compact Git diff.");
        return diff;
      };
      patch = parseNativePatch(input).map(file => {
        const abs = resolve(file.path);
        if (file.op === "Add") {
          if (fs.existsSync(abs)) patchError(`Add target already exists: ${file.path}.`, "patch_context_stale");
          return normalizedDiff(file.path, null, file.added.length ? file.added.join("\n") + "\n" : "");
        }
        if (!fs.existsSync(abs)) patchError(`Target does not exist: ${file.path}.`, "patch_context_stale");
        const stat = fs.lstatSync(abs);
        if (!stat.isFile() || stat.size > config.maxWriteBytes) patchError("Patch target is not a bounded regular text file.");
        const buffer = fs.readFileSync(abs);
        const before = buffer.toString("utf8");
        if (before.includes("\0") || !Buffer.from(before).equals(buffer)) patchError("Native patches require UTF-8 text.");
        const mode = stat.mode & 0o111 ? "100755" : "100644";
        if (file.op === "Delete") return normalizedDiff(file.path, before, null, mode);
        const after = updated(file, before);
        if (Buffer.byteLength(after) > config.maxWriteBytes) patchError("Updated file exceeds the write byte limit.");
        if (file.move) {
          if (fs.existsSync(resolve(file.move))) patchError("Move destination already exists.", "patch_context_stale");
          return normalizedDiff(file.path, before, null, mode) + normalizedDiff(file.move, null, after, mode);
        }
        return normalizedDiff(file.path, before, after);
      }).join("");
    }
    if (!patch.trim()) return { paths, stdout: "", stderr: "", diff: "", additions: 0, deletions: 0, changed: false };
    for (const check of [true, false]) {
      const result = spawnSync("git", ["apply", ...(check ? ["--check"] : []), "--whitespace=nowarn"], {
        cwd: workspace.root, input: patch, encoding: "utf8", maxBuffer: config.maxOutputBytes, timeout: 30_000,
        env: { ...process.env, NO_COLOR: "1" }
      });
      if (result.error || result.status !== 0) {
        const message = redactSensitiveText(result.stderr?.trim() || result.error?.message || "git apply failed");
        const code = /patch failed|does not apply|while searching|does not exist/i.test(message) ? "patch_context_stale" : /corrupt|malformed|no valid patches|unrecognized/i.test(message) ? "patch_format_invalid" : "patch_apply_failed";
        throw new CodexProError(message, { code, retryUnchanged: false, recovery: { tool: paths.length === 1 ? "read" : undefined,
          message: "Read current targets and regenerate the patch or use tagged edit.", ...(paths.length === 1 ? { args: { path: paths[0] } } : {}) } });
      }
    }
    let additions = 0, deletions = 0, inHunk = false;
    for (const line of patch.split("\n")) { if (line.startsWith("diff --git")) inHunk = false; if (line.startsWith("@@")) inHunk = true;
      if (inHunk && line[0] === "+") additions++; if (inHunk && line[0] === "-") deletions++; }
    return { paths, stdout: "", stderr: "", diff: redactSensitiveText(patch.trimEnd()), additions, deletions, changed: true };
  });
}
