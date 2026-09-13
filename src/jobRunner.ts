/** Detached, per-job supervisor. Deadlines and capture bounds survive MCP restarts. */
import fs from "node:fs";
import path from "node:path";
import { spawn } from "node:child_process";
import { OutputWriter } from "./jobOutput.js";
import { terminateProcessGroup } from "./processOps.js";

interface Spec { command: string; cwd: string; stdout: string; stderr: string; exit: string; result: string; control: string; outputDir: string; deadline: number; timeoutMs: number; limit: number; pathRedactions: Array<[string, string]> }
const spec: Spec = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
fs.unlinkSync(process.argv[2]);
const raw = [spec.stdout, spec.stderr].map(p => fs.openSync(p, "a", 0o600));
const renderBudget = { remaining: spec.limit * 2, exhausted: false };
const writers = ["stdout", "stderr"].map(n => new OutputWriter(path.join(spec.outputDir, `${n}.log`), spec.pathRedactions, renderBudget));
let bytes = 0, reason: string | undefined, finished = false;
let escalation: NodeJS.Timeout | undefined;
const child = spawn(fs.existsSync("/bin/bash") ? "/bin/bash" : "bash", ["-lc", spec.command], {
  cwd: spec.cwd, env: process.env, stdio: ["ignore", "pipe", "pipe"], detached: process.platform !== "win32", windowsHide: true
});
function stop(why: string) {
  if (reason || finished) return;
  reason = why;
  if (child.pid) terminateProcessGroup(child.pid, "SIGTERM");
  escalation = setTimeout(() => { if (child.pid) terminateProcessGroup(child.pid, "SIGKILL"); }, 1500);
}
function complete(code: number | null, signal: string | null) {
  if (finished) return; finished = true;
  clearInterval(poller); if (escalation) clearTimeout(escalation);
  if (child.pid) {
    terminateProcessGroup(child.pid, "SIGTERM");
    // Keep the supervisor alive for escalation even if descendants closed their
    // pipes before the command exited. Otherwise an ignored SIGTERM can orphan them.
    let descendantsRemain = false;
    if (process.platform !== "win32") {
      try { process.kill(-child.pid, 0); descendantsRemain = true; } catch {}
    }
    if (descendantsRemain) setTimeout(() => terminateProcessGroup(child.pid!, "SIGKILL"), 1500);
  }
  if (reason) {
    const message = Buffer.from(reason === "timeout" ? `\n[codexpro] Command timed out after ${spec.timeoutMs} ms.\n` : `\n[codexpro] Command stopped: ${reason}.\n`);
    fs.writeSync(raw[1], message); writers[1].write(message);
  }
  for (const writer of writers) writer.finish();
  for (const fd of raw) fs.closeSync(fd);
  const result = { version: 1, exit_code: code, signal, stop_reason: reason, finished_at_ms: Date.now() };
  fs.writeFileSync(`${spec.result}.tmp`, JSON.stringify(result), { mode: 0o600 });
  fs.renameSync(`${spec.result}.tmp`, spec.result);
  fs.writeFileSync(spec.exit, String(code ?? 1), { mode: 0o600 });
}
for (const [i, stream] of [child.stdout, child.stderr].entries()) stream?.on("data", (buffer: Buffer) => {
  const remaining = Math.max(0, spec.limit - bytes);
  const kept = buffer.subarray(0, remaining);
  if (kept.length) { bytes += kept.length; fs.writeSync(raw[i], kept); writers[i].write(kept); }
  if (buffer.length > remaining || renderBudget.exhausted) stop("output_limit");
});
const poller = setInterval(() => {
  if (Date.now() >= spec.deadline) stop("timeout");
  try { const requested = fs.readFileSync(spec.control, "utf8"); if (["stopped", "timeout", "output_limit"].includes(requested)) stop(requested); } catch {}
}, 100);
process.on("SIGTERM", () => stop("stopped"));
process.on("SIGINT", () => stop("stopped"));
child.on("error", () => { reason = "lost"; complete(null, null); });
child.on("close", complete);
