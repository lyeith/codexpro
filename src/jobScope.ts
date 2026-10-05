/** Exact identity of the one systemd scope that owns a managed job. */
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";

export interface JobScopeIdentity {
  version: 1;
  unit: string;
  boot_id: string;
  invocation_id: string;
  cgroup: string;
}

type ScopeProperties = { LoadState?: string; ActiveState?: string; InvocationID?: string; ControlGroup?: string };
function properties(unit: string): ScopeProperties | undefined {
  const result = spawnSync("systemctl", ["--user", "show", "--property=LoadState,ActiveState,InvocationID,ControlGroup", unit], {
    encoding: "utf8", timeout: 2000
  });
  if (result.error || result.status !== 0) return undefined;
  return Object.fromEntries(result.stdout.trim().split("\n").map(line => {
    const split = line.indexOf("="); return [line.slice(0, split), line.slice(split + 1)];
  }));
}
function bootId(): string | undefined {
  try { return fs.readFileSync("/proc/sys/kernel/random/boot_id", "utf8").trim(); } catch { return undefined; }
}
export function validScopeIdentity(value: unknown, unit?: string): value is JobScopeIdentity {
  if (!value || typeof value !== "object") return false;
  const identity = value as JobScopeIdentity;
  return identity.version === 1 && typeof identity.unit === "string" && /^codexpro-job_[a-f0-9]+-[a-f0-9]{32}\.scope$/.test(identity.unit) &&
    (!unit || identity.unit === unit) && /^[a-f0-9-]{36}$/.test(identity.boot_id) && /^[a-f0-9]{32}$/.test(identity.invocation_id) &&
    typeof identity.cgroup === "string" && identity.cgroup.startsWith("/") && !identity.cgroup.split("/").includes("..") && path.basename(identity.cgroup) === identity.unit;
}
export function captureScopeIdentity(unit: string): JobScopeIdentity {
  const current = properties(unit);
  const boot = bootId();
  let selfCgroup: string | undefined;
  try { selfCgroup = fs.readFileSync("/proc/self/cgroup", "utf8").split("\n").find(line => line.startsWith("0::"))?.slice(3); } catch {}
  const identity = { version: 1 as const, unit, boot_id: boot ?? "", invocation_id: current?.InvocationID ?? "", cgroup: current?.ControlGroup ?? "" };
  if (!validScopeIdentity(identity) || current?.LoadState !== "loaded" || current.ActiveState !== "active" || selfCgroup !== identity.cgroup) {
    throw new Error("The job supervisor is not inside its registered systemd scope.");
  }
  return identity;
}

export type ScopeState = "active" | "quiescent" | "unknown" | "mismatch";
/** Missing paths alone never authorize cleanup of an unverified registration. */
export function inspectScope(identity: JobScopeIdentity): ScopeState {
  if (!validScopeIdentity(identity)) return "unknown";
  const boot = bootId();
  if (!boot) return "unknown";
  if (boot !== identity.boot_id) return "quiescent";
  const current = properties(identity.unit);
  if (!current) return "unknown";
  const absent = current.LoadState === "not-found";
  if (!absent && (current.InvocationID !== identity.invocation_id || current.ControlGroup !== identity.cgroup)) return "mismatch";
  const directory = path.resolve("/sys/fs/cgroup", `.${identity.cgroup}`);
  const empty = (root: string): boolean | undefined => {
    try {
      if (fs.readFileSync(path.join(root, "cgroup.procs"), "utf8").trim()) return false;
      for (const child of fs.readdirSync(root, { withFileTypes: true }).filter(entry => entry.isDirectory())) {
        const result = empty(path.join(root, child.name)); if (result !== true) return result;
      }
      return true;
    } catch (error) { return (error as NodeJS.ErrnoException).code === "ENOENT" ? true : undefined; }
  };
  const result = empty(directory);
  return result === true ? "quiescent" : result === false && !absent ? "active" : "unknown";
}

export function signalScope(identity: JobScopeIdentity, signal: NodeJS.Signals): boolean {
  if (inspectScope(identity) !== "active") return false;
  const result = spawnSync("systemctl", ["--user", "kill", "--kill-whom=all", `--signal=${signal}`, identity.unit], { stdio: "ignore", timeout: 2000 });
  return !result.error && result.status === 0;
}

export function settleScope(identity: JobScopeIdentity): boolean {
  const initial = inspectScope(identity);
  if (initial === "quiescent") return true;
  if (initial !== "active" || !signalScope(identity, "SIGKILL")) return false;
  const until = performance.now() + 1000;
  do {
    const state = inspectScope(identity);
    if (state === "quiescent") return true;
    if (state !== "active") return false;
  } while (performance.now() < until);
  return false;
}
