import fs from "node:fs";
import path from "node:path";
import { createHash } from "node:crypto";
import { StringDecoder } from "node:string_decoder";
import type { CodexProConfig } from "./config.js";
import type { JobRecord, JobOutput } from "./jobs.js";
import { CodexProError } from "./guard.js";
import { redactSensitiveText } from "./redact.js";
import { pathRedactions, redactPathsInText, type PathRedactions } from "./pathLabels.js";

export const OUTPUT_PAGE_DEFAULT = 16_384;
export const OUTPUT_PAGE_MAX = 24_576; // bounded combined text per page
export const OUTPUT_RESPONSE_MAX = 192 * 1024;
const MAX_RECORD_BYTES = 1024 * 1024;
export function size(file: string): number { try { return fs.statSync(file).size; } catch { return 0; } }
export function workspaceOutputDir(config: CodexProConfig, workspace: string): string {
  return path.join(config.jobsDir, "output", createHash("sha256").update(workspace).digest("hex").slice(0, 24));
}
export function outputDir(config: CodexProConfig, job: JobRecord): string { return path.join(workspaceOutputDir(config, job.workspace_id), job.id); }

/** Append only complete, redacted UTF-8 records. Never release a partial credential. */
export interface RenderBudget { remaining: number; exhausted: boolean }
export class OutputWriter {
  private decoder = new StringDecoder("utf8");
  private pending = "";
  private dropping = false;
  private rendered: string[] = [];
  constructor(private readonly file: string, private readonly paths: PathRedactions = [], private readonly budget?: RenderBudget) {
    fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
    fs.writeFileSync(file, "", { mode: 0o600 });
  }
  write(bytes: Buffer): void { this.accept(this.decoder.write(bytes)); this.flush(); }
  finish(): void { this.accept(this.decoder.end()); if (this.pending || this.dropping) this.record(this.pending); this.pending = ""; this.flush(); }
  private flush(): void {
    if (this.rendered.length) fs.appendFileSync(this.file, this.rendered.join(""));
    this.rendered = [];
  }
  private record(text: string): void {
    let safe = this.dropping || text.includes("\0") || text.includes("\uFFFD")
      ? "[codexpro: overlong or non-text output record omitted]\n"
      : redactPathsInText(redactSensitiveText(text), this.paths);
    if (this.budget) {
      if (this.budget.exhausted) return;
      if (Buffer.byteLength(safe) > this.budget.remaining) {
        this.budget.exhausted = true;
        safe = "[codexpro: rendered output limit reached]\n";
        if (Buffer.byteLength(safe) > this.budget.remaining) return;
      }
      this.budget.remaining -= Buffer.byteLength(safe);
    }
    this.rendered.push(safe);
    this.dropping = false;
  }
  private accept(text: string): void {
    // CR is treated as part of a line until LF or completion; preserves CRLF and progress records.
    for (const part of text.match(/[^\n]*\n|[^\n]+$/g) ?? []) {
      if (!this.dropping) this.pending += part;
      if (Buffer.byteLength(this.pending) > MAX_RECORD_BYTES) { this.dropping = true; this.pending = ""; }
      if (part.endsWith("\n")) { this.record(this.pending); if (this.dropping) this.dropping = false; this.pending = ""; }
    }
  }
}

function range(file: string, offset: number, budget: number, tail = false) {
  const bytes = size(file);
  const start = tail ? Math.max(0, bytes - budget) : offset;
  const count = Math.max(0, Math.min(budget, bytes - start));
  if (!count) return { text: "", next: start, bytes };
  const fd = fs.openSync(file, "r");
  try {
    const buffer = Buffer.alloc(count);
    const read = fs.readSync(fd, buffer, 0, count, start);
    let first = 0, last = read;
    if (tail) while (first < last && (buffer[first] & 0xc0) === 0x80) first++;
    while (last > first) {
      try { new TextDecoder("utf-8", { fatal: true }).decode(buffer.subarray(first, last)); break; }
      catch { if (read - last >= 3) throw new CodexProError("Invalid rendered output", { code: "job_output_invalid" }); last--; }
    }
    return { text: buffer.subarray(first, last).toString("utf8"), next: start + last, bytes };
  } finally { fs.closeSync(fd); }
}

export class JobOutputStore {
  constructor(private readonly config: CodexProConfig) {}
  private prepare(job: JobRecord): string {
    const dir = outputDir(this.config, job);
    if (job.output_expired) return dir;
    if (!job.runner_version) {
      // Compatibility for finite legacy jobs. New runners maintain append-only views themselves.
      const stamp = path.join(dir, ".legacy-size");
      const fingerprint = `${size(job.stdout_path)}:${size(job.stderr_path)}:${job.status}`;
      let previous = ""; try { previous = fs.readFileSync(stamp, "utf8"); } catch {}
      if (previous !== fingerprint) {
        const replacements = this.config.exposeAbsolutePaths ? [] : pathRedactions(this.config, { root: job.root, workspace_id: job.workspace_id });
        const budget = { remaining: job.output_limit_bytes * 2, exhausted: false };
        for (const [name, raw] of [["stdout", job.stdout_path], ["stderr", job.stderr_path]]) {
          const writer = new OutputWriter(path.join(dir, `${name}.log`), replacements, budget);
          const fd = fs.openSync(raw, "r");
          try { const buffer = Buffer.alloc(64 * 1024); let n, remaining = job.output_limit_bytes; while (remaining > 0 && (n = fs.readSync(fd, buffer, 0, Math.min(buffer.length, remaining), null)) > 0) { writer.write(buffer.subarray(0, n)); remaining -= n; } }
          finally { fs.closeSync(fd); }
          if (job.status !== "running") writer.finish();
        }
        fs.writeFileSync(stamp, fingerprint, { mode: 0o600 });
      }
    }
    return dir;
  }
  metadata(job: JobRecord) {
    const dir = this.prepare(job);
    const available = !job.output_expired;
    return {
      stdout_bytes: available ? size(job.stdout_path) : job.captured_stdout_bytes ?? 0,
      stderr_bytes: available ? size(job.stderr_path) : job.captured_stderr_bytes ?? 0,
      available_stdout_bytes: available ? size(path.join(dir, "stdout.log")) : 0,
      available_stderr_bytes: available ? size(path.join(dir, "stderr.log")) : 0,
      output_available: available, output_growing: job.status === "running",
      output_expires_at: job.finished_at_ms ? new Date(job.finished_at_ms + this.config.jobRetentionMs).toISOString() : null,
      retention_note: "May expire earlier under bounded storage/count retention; input_job_ids pins logs during analysis.",
      ...(available && this.config.bashMode === "full" ? { output_files: {
        stdout: `$CODEXPRO_JOB_OUTPUT_DIR/${job.id}/stdout.log`, stderr: `$CODEXPRO_JOB_OUTPUT_DIR/${job.id}/stderr.log`
      } } : {})
    };
  }
  require(job: JobRecord): void {
    if (job.output_expired) throw new CodexProError("Job output expired under retention. Rerun only if needed and safe.", { code: "job_output_expired", retryUnchanged: false });
  }
  read(job: JobRecord, budget: number, mode: "head" | "tail"): JobOutput {
    const meta = this.metadata(job);
    if (!meta.output_available) return { stdout: "", stderr: "", stdout_bytes: meta.stdout_bytes, stderr_bytes: meta.stderr_bytes, truncated: true };
    const dir = outputDir(this.config, job);
    const out = range(path.join(dir, "stdout.log"), 0, budget, mode === "tail");
    const err = range(path.join(dir, "stderr.log"), 0, budget, mode === "tail");
    return { stdout: out.text, stderr: err.text, stdout_bytes: meta.stdout_bytes, stderr_bytes: meta.stderr_bytes, truncated: out.bytes > Buffer.byteLength(out.text) || err.bytes > Buffer.byteLength(err.text) };
  }
  page(job: JobRecord, cursor: string | undefined, budget: number) {
    this.require(job); this.prepare(job);
    const identity = `${job.workspace_id}:${job.id}:${job.started_at}`;
    let offsets = [0, 0];
    if (cursor) {
      try {
        if (cursor.length > 2048) throw Error();
        const parsed = JSON.parse(Buffer.from(cursor, "base64url").toString());
        if (parsed.v !== 1 || parsed.id !== identity || !Array.isArray(parsed.offsets) || parsed.offsets.length !== 2 || !parsed.offsets.every((n: unknown) => Number.isSafeInteger(n) && Number(n) >= 0)) throw Error();
        offsets = parsed.offsets;
      } catch { throw new CodexProError("Cursor does not belong to this workspace/job output.", { code: "job_cursor_invalid", retryUnchanged: false }); }
    }
    const dir = outputDir(this.config, job);
    const files = ["stdout", "stderr"].map(n => path.join(dir, `${n}.log`));
    const lengths = files.map(size);
    if (offsets.some((offset, i) => offset > lengths[i])) throw new CodexProError("Output changed or cursor is beyond retained data.", { code: "job_cursor_invalid", retryUnchanged: false });
    for (const [i, offset] of offsets.entries()) {
      if (!offset || offset === lengths[i]) continue;
      const fd = fs.openSync(files[i], "r");
      try {
        const byte = Buffer.alloc(1); fs.readSync(fd, byte, 0, 1, offset);
        if ((byte[0] & 0xc0) === 0x80) throw new CodexProError("Cursor splits a UTF-8 character.", { code: "job_cursor_invalid", retryUnchanged: false });
      } finally { fs.closeSync(fd); }
    }
    const out = range(files[0], offsets[0], budget - Math.min(lengths[1] - offsets[1], Math.floor(budget / 2)));
    const err = range(files[1], offsets[1], budget - Buffer.byteLength(out.text));
    const next = [out.next, err.next];
    const hasMore = next.some((n, i) => n < lengths[i]);
    return { stdout: out.text, stderr: err.text,
      returned_bytes: Buffer.byteLength(out.text) + Buffer.byteLength(err.text),
      next_cursor: Buffer.from(JSON.stringify({ v: 1, id: identity, offsets: next })).toString("base64url"),
      has_more: hasMore, output_complete: job.status !== "running" && !hasMore, output_mode: "incremental" };
  }
  remove(job: JobRecord): void { fs.rmSync(outputDir(this.config, job), { recursive: true, force: true }); }
}
