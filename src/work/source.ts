import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { isSubpath } from "../guard.js";
import { redactSensitiveText } from "../redact.js";
import type { SourceSnapshot } from "./types.js";

function git(root: string, args: string[]): Buffer {
  const result = spawnSync("git", ["--no-optional-locks", ...args], {
    cwd: root, timeout: 10_000, maxBuffer: 8 * 1024 * 1024,
    env: { ...process.env, GIT_TERMINAL_PROMPT: "0", GIT_OPTIONAL_LOCKS: "0" }
  });
  if (result.error || result.status !== 0) throw new Error(result.error?.message ?? result.stderr.toString().trim() ?? "Git observation failed");
  return result.stdout;
}

/** Complete, bounded fingerprint of tracked and non-ignored untracked source. */
export function observeSource(root: string, maxBytes: number, contextDir = ".ai-bridge", at = new Date().toISOString()): SourceSnapshot {
  const result: SourceSnapshot = { observed_at: at, dirty_paths: [], complete: false };
  try {
    result.head = git(root, ["rev-parse", "HEAD"]).toString().trim();
    result.branch = git(root, ["rev-parse", "--abbrev-ref", "HEAD"]).toString().trim();
    const status = git(root, ["status", "--porcelain=v1", "-z", "--", "."]).toString().split("\0");
    for (let index = 0; index < status.length; index++) {
      if (!status[index]) continue;
      const record = status[index];
      const file = record.slice(3);
      if (file !== contextDir && !file.startsWith(`${contextDir}/`)) result.dirty_paths.push(file);
      if (/[RC]/.test(record.slice(0, 2))) index++;
    }
    const names = [...new Set(git(root, ["ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "."]).toString().split("\0").filter(Boolean))].sort();
    if (names.length > 50_000) throw new Error("Source fingerprint exceeds the 50000-path limit");
    const hash = createHash("sha256").update(`head\0${result.head}\0`);
    hash.update(git(root, ["ls-files", "--stage", "-z", "--", ".", `:(exclude)${contextDir}`]));
    let bytes = 0;
    const buffer = Buffer.alloc(64 * 1024);
    for (const name of names) {
      if (name === contextDir || name.startsWith(`${contextDir}/`) || name.startsWith(".codexpro-batches/")) continue;
      const file = path.resolve(root, name);
      if (!isSubpath(file, root)) throw new Error("Source path escapes the workspace");
      hash.update(`\0path\0${name}\0`);
      let stat: fs.Stats;
      try { stat = fs.lstatSync(file); } catch (error) {
        if ((error as NodeJS.ErrnoException).code === "ENOENT") { hash.update("deleted"); continue; }
        throw error;
      }
      hash.update(`mode\0${stat.mode}\0`);
      if (!isSubpath(fs.realpathSync(path.dirname(file)), fs.realpathSync(root))) throw new Error("Source parent escapes the workspace");
      if (stat.isSymbolicLink()) { hash.update(`link\0${fs.readlinkSync(file)}`); continue; }
      if (!stat.isFile()) throw new Error(`Unsupported source entry: ${name}`);
      bytes += stat.size;
      if (bytes > maxBytes) throw new Error(`Source fingerprint exceeds ${maxBytes} bytes`);
      const fd = fs.openSync(file, "r");
      try { for (;;) { const n = fs.readSync(fd, buffer, 0, buffer.length, null); if (!n) break; hash.update(buffer.subarray(0, n)); } }
      finally { fs.closeSync(fd); }
      const after = fs.lstatSync(file);
      if (after.size !== stat.size || after.mtimeMs !== stat.mtimeMs || after.ino !== stat.ino) throw new Error(`Source changed during observation: ${name}`);
    }
    if (git(root, ["rev-parse", "HEAD"]).toString().trim() !== result.head) throw new Error("HEAD changed during source observation");
    result.fingerprint = hash.digest("hex");
    result.complete = true;
  } catch (error) {
    result.error = redactSensitiveText(error instanceof Error ? error.message : String(error)).slice(0, 1000);
  }
  result.dirty_paths = [...new Set(result.dirty_paths)].slice(0, 200);
  return result;
}
