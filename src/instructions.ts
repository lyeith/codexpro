import fs from "node:fs";
import fsp from "node:fs/promises";
import path from "node:path";
import { StringDecoder } from "node:string_decoder";
import type { CodexProConfig } from "./config.js";
import type { PathGuard, Workspace } from "./guard.js";
import { redactSensitiveText } from "./redact.js";

export interface InstructionSource {
  scope: "global" | "workspace";
  path: string;
  loaded: boolean;
  truncated: boolean;
  error?: string;
}

export interface Instructions {
  text: string;
  sources: InstructionSource[];
}

const NAMES = ["AGENTS.override.md", "AGENTS.md", "agents.md", ".agents.md"];

/** One file per scope: an override replaces the regular file in that directory. */
async function instructionName(dir: string): Promise<string | undefined> {
  let entries: fs.Dirent[];
  try { entries = await fsp.readdir(dir, { withFileTypes: true }); }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined;
    throw error;
  }
  for (const name of NAMES) {
    const entry = entries.find(item => item.name === name) ?? entries.find(item => item.name.toLowerCase() === name.toLowerCase());
    if (entry) return entry.name;
  }
  return undefined;
}

export function instructionMetadata(instructions: Instructions) {
  return {
    agents_loaded: instructions.sources.some(source => source.loaded),
    agents_path: instructions.sources.find(source => source.scope === "workspace" && source.loaded)?.path,
    agents_files: instructions.sources.filter(source => source.loaded).map(source => source.path),
    agents_sources: instructions.sources,
    agents_complete: instructions.sources.every(source => source.loaded && !source.truncated)
  };
}

export function instructionBudget(config: CodexProConfig): number {
  // Leave room for workspace identity, git status, skills and an optional tree.
  return Math.min(60_000, Math.floor(config.maxOutputBytes * 0.6));
}

export async function readInstructions(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  options: { targetPath?: string; includeGlobal?: boolean; includeWorkspace?: boolean; maxBytes?: number } = {}
): Promise<Instructions> {
  const scopes: Array<{ scope: InstructionSource["scope"]; dir: string; relativeDir?: string }> = [];
  if (options.includeGlobal !== false) scopes.push({ scope: "global", dir: config.codexDir });
  if (options.includeWorkspace !== false) {
    const target = guard.resolve(workspace, options.targetPath ?? ".");
    let relativeDir = target.relPath;
    try { if (!(await fsp.stat(target.absPath)).isDirectory()) relativeDir = path.dirname(relativeDir); }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
      // A not-yet-created target is treated as a file; its parent supplies rules.
      relativeDir = path.dirname(relativeDir);
    }
    scopes.push({ scope: "workspace", dir: workspace.root, relativeDir: "." });
    const parts = relativeDir === "." ? [] : relativeDir.split(path.sep).filter(Boolean);
    for (let i = 1; i <= parts.length; i++) {
      const relative = parts.slice(0, i).join(path.sep);
      guard.resolve(workspace, relative);
      scopes.push({ scope: "workspace", dir: path.join(workspace.root, relative), relativeDir: relative });
    }
  }

  const selected: Array<{ source: InstructionSource; absolute?: string }> = [];
  for (const scope of scopes) {
    try {
      const name = await instructionName(scope.dir);
      if (!name) continue;
      const sourcePath = scope.scope === "global" ? path.join(scope.dir, name) : path.join(scope.relativeDir!, name);
      selected.push({ source: { scope: scope.scope, path: sourcePath, loaded: false, truncated: false }, absolute: path.join(scope.dir, name) });
    } catch {
      selected.push({ source: { scope: scope.scope, path: scope.relativeDir ?? scope.dir, loaded: false, truncated: false, error: "Cannot discover instruction files in this directory." } });
    }
  }

  const chunks: string[] = [];
  const budget = Math.max(0, Math.min(options.maxBytes ?? instructionBudget(config), config.maxReadBytes));
  const perFile = Math.floor(budget / Math.max(1, selected.length));
  const seen = new Set<string>();
  const sources: InstructionSource[] = [];
  for (const { source, absolute } of selected) {
    try {
      if (source.error || !absolute) throw new Error(source.error);
      // Only the operator-configured global directory bypasses workspace scope.
      // A project symlink still cannot pull instructions from outside its root.
      const resolved = source.scope === "workspace" ? guard.resolve(workspace, source.path).absPath : absolute;
      const real = await fsp.realpath(resolved);
      if (seen.has(real)) continue;
      seen.add(real);
      const handle = await fsp.open(real, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
      let text: string;
      try {
        if (!(await handle.stat()).isFile()) throw new Error("Instructions must be a regular text file.");
        const buffer = Buffer.alloc(perFile + 1);
        let bytes = 0;
        while (bytes < buffer.length) {
          const read = await handle.read(buffer, bytes, buffer.length - bytes, bytes);
          if (!read.bytesRead) break;
          bytes += read.bytesRead;
        }
        if (buffer.subarray(0, bytes).includes(0)) throw new Error("Instructions must be a text file.");
        source.truncated = bytes > perFile;
        const decoder = new StringDecoder("utf8");
        text = decoder.write(buffer.subarray(0, Math.min(bytes, perFile)));
        if (!source.truncated) text += decoder.end();
      } finally { await handle.close(); }
      source.loaded = perFile > 0;
      chunks.push(`### ${source.scope} instructions: ${source.path}\n\n${redactSensitiveText(text)}${source.truncated ? "\n[Instructions truncated by the response budget; this scope is incomplete.]" : ""}`);
    } catch (error) {
      source.error = source.error ?? (error instanceof Error ? error.message : String(error));
      chunks.push(`### ${source.scope} instructions: ${source.path}\n\n[Instructions unreadable: ${source.error}]`);
    }
    sources.push(source);
  }
  if (options.includeGlobal !== false && !sources.some(source => source.scope === "global")) chunks.unshift(`Global instructions: none found in ${config.codexDir}.`);
  if (options.includeWorkspace !== false && !sources.some(source => source.scope === "workspace")) chunks.push("Workspace instructions: none found for this target.");
  return { text: chunks.join("\n\n"), sources };
}
