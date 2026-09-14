/** Detached supervisor: registration grant, bounded execution, then tree quiescence. */
import fs from "node:fs";
import path from "node:path";
import { spawn, spawnSync } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";
import { OutputWriter } from "./jobOutput.js";
import { terminateProcessGroup } from "./processOps.js";

interface Spec { command: string; cwd: string; stdout: string; stderr: string; exit: string; result: string; control: string; outputDir: string; deadline: number; timeoutMs: number; limit: number; grant?: string; nonce?: string; pathRedactions: Array<[string, string]> }
const spec: Spec = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const started = performance.now();
function requested(): string | undefined {
  try { const value = fs.readFileSync(spec.control, "utf8"); if (["stopped", "timeout", "output_limit"].includes(value)) return value; } catch {}
}
function expired(): boolean { return performance.now() - started >= spec.timeoutMs || Date.now() >= spec.deadline; }
function currentCgroup(): string | undefined {
  try { return fs.readFileSync('/proc/self/cgroup', 'utf8').split('\n').find(line => line.startsWith('0::'))?.slice(3); } catch { return undefined; }
}
function publish(code: number | null, signal: string | null, reason: string | undefined, quiescent: boolean) {
  const cgroup = currentCgroup();
  fs.writeFileSync(`${spec.result}.tmp`, JSON.stringify({ version: 2, exit_code: code, signal, stop_reason: reason, quiescent, cgroup, finished_at_ms: Date.now() }), { mode: 0o600 });
  fs.renameSync(`${spec.result}.tmp`, spec.result);
  fs.writeFileSync(spec.exit, String(code ?? 1), { mode: 0o600 });
}
let granted = !spec.grant;
while (!granted && performance.now() - started < 10_000 && !expired() && !requested()) {
  try { granted = fs.readFileSync(spec.grant!, "utf8") === spec.nonce; } catch {}
  if (!granted) await delay(25);
}
if (!granted || expired() || requested()) {
  publish(null, null, requested() ?? (expired() ? "timeout" : "lost"), true);
} else {
  // A supervisor spec is single-use, even if someone accidentally launches it twice.
  try { fs.writeFileSync(`${process.argv[2]}.started`, JSON.stringify({ pid: process.pid, cgroup: currentCgroup() }), { mode: 0o600, flag: "wx" }); }
  catch { process.exit(1); }
  const raw = [spec.stdout, spec.stderr].map(p => fs.openSync(p, "a", 0o600));
  const renderBudget = { remaining: spec.limit * 2, exhausted: false };
  const writers = ["stdout", "stderr"].map(n => new OutputWriter(path.join(spec.outputDir, `${n}.log`), spec.pathRedactions, renderBudget));
  let bytes = 0, reason: string | undefined, finished = false;
  let escalation: NodeJS.Timeout | undefined;
  const child = spawn(fs.existsSync("/bin/bash") ? "/bin/bash" : "bash", ["-lc", spec.command], {
    cwd: spec.cwd, env: process.env, stdio: ["ignore", "pipe", "pipe"], detached: process.platform !== "win32", windowsHide: true
  });
  function groupLive(): boolean {
    if (!child.pid) return false;
    if (process.platform === "win32") return true; // No POSIX quiescence proof available.
    const ps = spawnSync("ps", ["-eo", "pgid=,stat="], { encoding: "utf8", timeout: 1000 });
    if (ps.status !== 0) return true;
    return ps.stdout.split("\n").some(line => { const [group, state] = line.trim().split(/\s+/); return Number(group) === child.pid && !state?.startsWith("Z"); });
  }
  function stop(why: string) {
    if (reason || finished) return;
    reason = why;
    if (child.pid) terminateProcessGroup(child.pid, "SIGTERM");
    escalation = setTimeout(() => { if (child.pid) terminateProcessGroup(child.pid, "SIGKILL"); }, 1500);
  }
  async function complete(code: number | null, signal: string | null) {
    if (finished) return; finished = true;
    clearInterval(poller); if (escalation) clearTimeout(escalation);
    if (child.pid && groupLive()) {
      terminateProcessGroup(child.pid, "SIGTERM");
      await delay(1500);
      if (groupLive()) terminateProcessGroup(child.pid, "SIGKILL");
      for (let n = 0; n < 20 && groupLive(); n++) await delay(100);
    }
    if (reason) {
      const message = Buffer.from(reason === "timeout" ? `\n[codexpro] Command timed out after ${spec.timeoutMs} ms.\n` : `\n[codexpro] Command stopped: ${reason}.\n`);
      fs.writeSync(raw[1], message); writers[1].write(message);
    }
    for (const writer of writers) writer.finish();
    for (const fd of raw) fs.closeSync(fd);
    publish(code, signal, reason, !groupLive());
  }
  for (const [i, stream] of [child.stdout, child.stderr].entries()) stream?.on("data", (buffer: Buffer) => {
    const remaining = Math.max(0, spec.limit - bytes);
    const kept = buffer.subarray(0, remaining);
    if (kept.length) { bytes += kept.length; fs.writeSync(raw[i], kept); writers[i].write(kept); }
    if (buffer.length > remaining || renderBudget.exhausted) stop("output_limit");
  });
  const poller = setInterval(() => { if (expired()) stop("timeout"); const why = requested(); if (why) stop(why); }, 100);
  process.on("SIGTERM", () => stop("stopped"));
  process.on("SIGINT", () => stop("stopped"));
  child.on("error", () => { reason = "lost"; void complete(null, null); });
  child.on("close", (code, signal) => { void complete(code, signal); });
}
