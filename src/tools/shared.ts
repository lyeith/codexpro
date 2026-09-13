import { decodeGitQuotedPath } from "../gitPaths.js";
export { decodeGitQuotedPath } from "../gitPaths.js";
import fsp from "node:fs/promises";
import path from "node:path";
import { z } from "zod";
import type { CodexProConfig } from "../config.js";
import { CodexProError, type PathGuard, type Workspace } from "../guard.js";
import type { runBash } from "../bashOps.js";
import { redactSensitiveText, redactStructured } from "../redact.js";

export const STRUCTURED_STRING_MAX_CHARS = 30_000;

export function errorText(error: unknown): string {
  if (error instanceof Error) return redactSensitiveText(`${error.name}: ${error.message}`);
  return redactSensitiveText(String(error));
}

export function compactStructuredContent<T>(value: T, depth = 0): T {
  if (depth > 8 || value === null || value === undefined) return value;
  if (typeof value === "string") {
    if (value.length <= STRUCTURED_STRING_MAX_CHARS) return value as T;
    return `${value.slice(0, STRUCTURED_STRING_MAX_CHARS)}\n...[structured field truncated to ${STRUCTURED_STRING_MAX_CHARS} chars]` as T;
  }
  if (Array.isArray(value)) return value.map((item) => compactStructuredContent(item, depth + 1)) as T;
  if (typeof value !== "object") return value;

  const out: Record<string, unknown> = {};
  for (const [key, item] of Object.entries(value)) {
    out[key] = compactStructuredContent(item, depth + 1);
  }
  return out as T;
}

export function truncateUtf8WithMarker(value: string, maxBytes: number, marker: string): { value: string; truncated: boolean } {
  if (Buffer.byteLength(value, "utf8") <= maxBytes) return { value, truncated: false };
  if (maxBytes <= 0) return { value: "", truncated: true };
  const markerBytes = Buffer.byteLength(marker, "utf8");
  const includeMarker = markerBytes <= maxBytes;
  const contentBudget = includeMarker ? maxBytes - markerBytes : maxBytes;
  let low = 0;
  let high = value.length;
  while (low < high) {
    const mid = Math.ceil((low + high) / 2);
    if (Buffer.byteLength(value.slice(0, mid), "utf8") <= contentBudget) low = mid;
    else high = mid - 1;
  }
  if (low > 0 && /[\uD800-\uDBFF]/.test(value[low - 1])) low -= 1;
  const suffix = includeMarker ? marker : "";
  return { value: `${value.slice(0, low)}${suffix}`, truncated: true };
}

export const BATCH_STRUCTURED_KEY_PRIORITY = [
  "workspace_id", "project_id", "root", "path", "paths", "error", "error_code", "retry_unchanged", "recovery",
  "provider", "provider_version", "mode", "language", "edit_tag", "base_edit_tag", "sha256",
  "start_line", "end_line", "total_lines", "bytes", "changed", "created", "existed",
  "additions", "deletions", "replacements", "edits_applied", "exit_code", "signal", "duration_ms",
  "timed_out", "truncated", "has_more", "next_cursor", "query_fingerprint", "count", "entries", "matches_count",
  "matches", "contexts", "warnings", "changed_paths", "operation_count", "succeeded_count", "failed_count",
  "skipped_count", "succeeded", "stdout", "stderr", "text", "diff"
];

export const BATCH_STRUCTURED_KEY_RANK = new Map(BATCH_STRUCTURED_KEY_PRIORITY.map((key, index) => [key, index]));

export function boundedBatchStructuredContent(value: unknown, maxBytes: number): { value: unknown; truncated: boolean } {
  const compact = compactStructuredContent(value);
  const jsonBytes = (candidate: unknown): number => {
    try {
      return Buffer.byteLength(JSON.stringify(candidate), "utf8");
    } catch {
      return Number.POSITIVE_INFINITY;
    }
  };
  if (jsonBytes(compact) <= maxBytes) return { value: compact, truncated: false };

  let stringBytes = Math.max(64, Math.min(2_000, Math.floor(maxBytes / 3)));
  let arrayItems = 20;
  let objectKeys = 40;
  const trim = (candidate: unknown, depth = 0): unknown => {
    if (candidate === null || candidate === undefined || typeof candidate === "number" || typeof candidate === "boolean") return candidate;
    if (typeof candidate === "string") return truncateUtf8WithMarker(candidate, stringBytes, "…").value;
    if (depth >= 5) return "[batch depth limit]";
    if (Array.isArray(candidate)) return candidate.slice(0, arrayItems).map((item) => trim(item, depth + 1));
    if (typeof candidate !== "object") return String(candidate);
    const source = candidate as Record<string, unknown>;
    const keys = Object.keys(source).sort((left, right) => {
      const leftRank = BATCH_STRUCTURED_KEY_RANK.get(left) ?? Number.MAX_SAFE_INTEGER;
      const rightRank = BATCH_STRUCTURED_KEY_RANK.get(right) ?? Number.MAX_SAFE_INTEGER;
      return leftRank - rightRank || left.localeCompare(right);
    }).slice(0, objectKeys);
    return Object.fromEntries(keys.map((key) => [key, trim(source[key], depth + 1)]));
  };

  for (let attempt = 0; attempt < 6; attempt += 1) {
    const candidate = trim(compact);
    if (jsonBytes(candidate) <= maxBytes) return { value: candidate, truncated: true };
    stringBytes = Math.max(32, Math.floor(stringBytes / 2));
    arrayItems = Math.max(1, Math.floor(arrayItems / 2));
    objectKeys = Math.max(4, Math.floor(objectKeys / 2));
  }

  if (compact && typeof compact === "object" && !Array.isArray(compact)) {
    const source = compact as Record<string, unknown>;
    const summary: Record<string, unknown> = { _batch_truncated: true };
    for (const key of BATCH_STRUCTURED_KEY_PRIORITY) {
      const item = source[key];
      if (item === undefined || (typeof item === "object" && item !== null)) continue;
      summary[key] = typeof item === "string" ? truncateUtf8WithMarker(item, 128, "…").value : item;
      if (jsonBytes(summary) > maxBytes) {
        delete summary[key];
        break;
      }
    }
    return { value: summary, truncated: true };
  }
  return { value: { _batch_truncated: true }, truncated: true };
}

export function textResult(text: string, structuredContent: Record<string, unknown> = {}, meta: Record<string, unknown> = {}): any {
  return {
    content: [{ type: "text", text: redactSensitiveText(text) }],
    structuredContent: redactStructured(structuredContent),
    _meta: meta
  };
}

export function countTextLines(value: string | undefined): number {
  if (!value) return 0;
  return value.split(/\r?\n/).filter((line) => line.length > 0).length;
}

export function bashTextResult(config: CodexProConfig, result: Awaited<ReturnType<typeof runBash>>): string {
  if (result.jobStatus === "running") {
    const stdoutTail = outputTail(result.stdout);
    const stderrTail = outputTail(result.stderr);
    return [
      "# Bash (background job)",
      "",
      `\`${result.command}\``,
      "",
      `CWD: ${result.cwd}`,
      result.jobOrigin === "promoted"
        ? `Still running after ${result.durationMs} ms; it was moved to the background as job ${result.jobId}.`
        : `Started as background job ${result.jobId} (${result.durationMs} ms so far).`,
      `Collect it with one jobs(job_ids=["${result.jobId}"], wait_ms=300000) call (waiting is cheaper than polling) or stop it with stop_jobs.`,
      stdoutTail.text ? `\n## stdout so far\n\n\`\`\`text\n${stdoutTail.text}\n\`\`\`` : "",
      stderrTail.text ? `\n## stderr so far\n\n\`\`\`text\n${stderrTail.text}\n\`\`\`` : ""
    ].filter((line) => line !== "").join("\n");
  }
  if (config.bashTranscript === "full") {
    return `# Bash\n\n\`\`\`bash\n$ ${result.command}\n\`\`\`\n\nCWD: ${result.cwd}\nExit: ${result.exitCode}${result.signal ? ` (${result.signal})` : ""}\nDuration: ${result.durationMs} ms\n\n## stdout\n\n\`\`\`text\n${result.stdout || ""}\n\`\`\`\n\n## stderr\n\n\`\`\`text\n${result.stderr || ""}\n\`\`\``;
  }

  const stdoutLines = countTextLines(result.stdout);
  const stderrLines = countTextLines(result.stderr);
  const stdoutTail = outputTail(result.stdout);
  const stderrTail = outputTail(result.stderr);
  return [
    "# Bash",
    "",
    `\`${result.command}\``,
    "",
    `CWD: ${result.cwd}`,
    `Exit: ${result.exitCode}${result.signal ? ` (${result.signal})` : ""}`,
    `Duration: ${result.durationMs} ms`,
    `Output: stdout ${stdoutLines} line${stdoutLines === 1 ? "" : "s"}, stderr ${stderrLines} line${stderrLines === 1 ? "" : "s"}.`,
    stdoutTail.text ? `\n## stdout${stdoutTail.truncated ? " (tail)" : ""}\n\n\`\`\`text\n${stdoutTail.text}\n\`\`\`` : "",
    stderrTail.text ? `\n## stderr${stderrTail.truncated ? " (tail)" : ""}\n\n\`\`\`text\n${stderrTail.text}\n\`\`\`` : "",
    "",
    (stdoutTail.truncated || stderrTail.truncated)
      ? "Only the tail is shown above; bounded stdout/stderr are in structured content. Use job output pages or managed files for more."
      : "Bounded stdout/stderr are also in structured content."
  ].filter((line) => line !== "").join("\n");
}

export const BASH_TEXT_TAIL_LINES = 40;

export const BASH_TEXT_TAIL_BYTES = 4 * 1024;

/** Last N lines / bytes of a command stream for the chat transcript. */
export function outputTail(value: string | undefined): { text: string; truncated: boolean } {
  const trimmed = (value ?? "").replace(/\s+$/, "");
  if (!trimmed) return { text: "", truncated: false };
  const lines = trimmed.split(/\r?\n/);
  let tail = lines.slice(-BASH_TEXT_TAIL_LINES).join("\n");
  let truncated = lines.length > BASH_TEXT_TAIL_LINES;
  if (Buffer.byteLength(tail, "utf8") > BASH_TEXT_TAIL_BYTES) {
    tail = Buffer.from(tail, "utf8").subarray(-BASH_TEXT_TAIL_BYTES).toString("utf8").replace(/^\uFFFD+/, "");
    truncated = true;
  }
  return { text: truncated ? `…\n${tail}` : tail, truncated };
}

export function errorResult(error: unknown): any {
  const message = errorText(error);
  const codexError = error instanceof CodexProError ? error : undefined;
  const recovery = codexError?.recovery;
  const content = [
    message,
    recovery?.message ? `Recovery: ${recovery.message}` : "",
    codexError?.retryUnchanged === false
      ? "Do not retry the same request unchanged. Refresh its inputs or use the suggested recovery action."
      : ""
  ].filter(Boolean).join("\n\n");
  return {
    isError: true,
    content: [{ type: "text", text: content }],
    structuredContent: {
      error: message,
      ...(codexError?.code ? { error_code: codexError.code } : {}),
      ...(codexError?.retryUnchanged !== undefined ? { retry_unchanged: codexError.retryUnchanged } : {}),
      ...(recovery ? { recovery } : {}),
      ...(codexError?.details ?? {})
    }
  };
}

export function isContextPath(config: CodexProConfig, relPath: string): boolean {
  const normalized = relPath.split(path.sep).join("/").replace(/^\.\//, "");
  const contextDir = config.contextDir.replace(/^\.\//, "").replace(/\/$/, "");
  return normalized === contextDir || normalized.startsWith(`${contextDir}/`);
}

export function assertWriteToolAllowed(config: CodexProConfig, relPath: string): void {
  if (config.writeMode === "workspace") return;
  if (config.writeMode === "handoff" && isContextPath(config, relPath)) return;
  if (config.writeMode === "handoff") {
    throw new CodexProError(
      `Source writes are disabled because CODEXPRO_WRITE_MODE=handoff. ` +
        `Use handoff_to_agent, or write/edit/apply_patch only inside ${config.contextDir}/.`
    );
  }
  if (config.handoffMode === "on") {
    throw new CodexProError("write/edit/apply_patch tools are disabled because CODEXPRO_WRITE_MODE=off. The explicitly enabled handoff tools remain available for bounded planning.");
  }
  throw new CodexProError("write/edit/apply_patch and handoff tools are disabled by the current runtime policy.");
}

export function auditStructuredResult(rawResult: unknown): Record<string, unknown> {
  if (!rawResult || typeof rawResult !== "object" || Array.isArray(rawResult)) return {};
  const root = rawResult as Record<string, unknown>;
  return root.structuredContent && typeof root.structuredContent === "object" && !Array.isArray(root.structuredContent)
    ? root.structuredContent as Record<string, unknown>
    : root;
}

export function workspaceIdSchema(config: CodexProConfig): z.ZodString | z.ZodOptional<z.ZodString> {
  // Persistent catalogs can grow while this process is running. Require explicit routing from
  // startup instead of letting a formerly single-project schema become ambiguous later.
  if (config.worktreeMode === "mcp" || config.projectsFile || config.projects.length > 1) {
    return z.string().describe(
      config.worktreeMode === "mcp"
        ? "Required stable workspace_id returned by create_workspace. Copy it exactly into every repository tool call."
        : "Required workspace_id returned by open_workspace for the selected project."
    );
  }
  return z.string().optional().describe("Workspace id from open_workspace. Omit to use the workspace selected for this MCP session.");
}

export function limitInt(value: unknown, fallback: number, min: number, max: number): number {
  const n = Number(value ?? fallback);
  if (!Number.isFinite(n)) return fallback;
  return Math.max(min, Math.min(max, Math.floor(n)));
}

export function parseBool(value: unknown, fallback = false): boolean {
  if (typeof value === "boolean") return value;
  if (value === undefined || value === null) return fallback;
  return ["1", "true", "yes", "y"].includes(String(value).toLowerCase());
}

export function diffBlock(diff: string): string {
  return `\n\n\`\`\`diff\n${diff}\n\`\`\``;
}

export function diffStats(diff: string): { additions: number; deletions: number; changed: boolean } {
  let additions = 0;
  let deletions = 0;
  for (const line of diff.split("\n")) {
    if (line.startsWith("+") && !line.startsWith("+++")) additions += 1;
    if (line.startsWith("-") && !line.startsWith("---")) deletions += 1;
  }
  return { additions, deletions, changed: Boolean(diff.trim()) };
}

export function normalizeGitOutput(output: string): string {
  return output.trim() === "(no output)" ? "" : output;
}


export function looksLikeGitError(output: string): boolean {
  const trimmed = output.trim();
  const lower = trimmed.toLowerCase();
  return (
    trimmed.startsWith("fatal:") ||
    trimmed.startsWith("error:") ||
    trimmed.startsWith("git unavailable or failed:") ||
    trimmed.startsWith("git exited with status") ||
    trimmed.startsWith("usage: git ") ||
    lower.includes("not a git repository")
  );
}

export function previewText(value: string, maxLines = 40, maxChars = 12_000): string {
  const lines = value.replace(/\r\n/g, "\n").split("\n").slice(0, maxLines).join("\n");
  return lines.length > maxChars ? `${lines.slice(0, maxChars)}\n...[preview truncated]` : lines;
}

export function changedStatusLines(status: string): string[] {
  return status
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line && line !== "(no output)" && !line.startsWith("##"));
}

export function changedPathsFromStatus(lines: string[]): string[] {
  const paths: string[] = [];
  for (const line of lines) {
    let raw: string;
    if (line.startsWith("?? ")) raw = line.slice(3).trim();
    else if (line.includes("\t")) raw = line.split("\t").pop()?.trim() ?? "";
    else if (/^.{2}\s/.test(line)) raw = line.slice(3).trim();
    else continue;
    if (raw.includes(" -> ")) raw = raw.split(" -> ").pop() ?? raw;
    const decoded = decodeGitQuotedPath(raw);
    if (decoded && !paths.includes(decoded)) paths.push(decoded);
  }
  return paths;
}

export function cleanOneLine(value: unknown, fallback: string, maxLength = 120): string {
  const text = String(value ?? "").replace(/\s+/g, " ").trim();
  return (text || fallback).slice(0, maxLength);
}

export async function readRawTextFileBounded(config: CodexProConfig, guard: PathGuard, workspace: Workspace, filePath: string): Promise<string> {
  const resolved = guard.resolve(workspace, filePath);
  await guard.assertTextFile(resolved.absPath, config.maxReadBytes);
  return fsp.readFile(resolved.absPath, "utf8");
}

export const READ_ONLY_ANNOTATIONS = { readOnlyHint: true, openWorldHint: false, destructiveHint: false };

export const SESSION_READ_ANNOTATIONS = { readOnlyHint: true, openWorldHint: false, destructiveHint: false, idempotentHint: false };

export // Workspace file writes are recoverable via git, so they are "write" but not "destructive"
// (clients such as ChatGPT still ask for confirmation because readOnlyHint is false).
const LOCAL_WRITE_ANNOTATIONS = { readOnlyHint: false, openWorldHint: false, destructiveHint: false, idempotentHint: false };

export const PROJECT_CREATE_ANNOTATIONS = { readOnlyHint: false, openWorldHint: true, destructiveHint: false, idempotentHint: false };

export const BASH_ANNOTATIONS = { readOnlyHint: false, openWorldHint: true, destructiveHint: true, idempotentHint: false };

export const HANDOFF_WRITE_ANNOTATIONS = { readOnlyHint: false, openWorldHint: false, destructiveHint: false, idempotentHint: false };
