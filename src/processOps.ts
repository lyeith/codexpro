import { spawnSync, type ChildProcess } from "node:child_process";

/** Signal a detached process group by its leader pid (jobs re-attached after a restart have no ChildProcess). */
export function terminateProcessGroup(pid: number, signal: NodeJS.Signals): void {
  if (!pid) return;
  if (process.platform === "win32") {
    spawnSync("taskkill", ["/pid", String(pid), "/t", "/f"], { stdio: "ignore", windowsHide: true });
    return;
  }
  try {
    process.kill(-pid, signal);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ESRCH") return;
    try { process.kill(pid, signal); } catch { /* already gone */ }
  }
}

export function terminateProcessTree(child: ChildProcess, signal: NodeJS.Signals): void {
  if (!child.pid) return;
  if (process.platform === "win32") {
    // Windows does not provide Unix-style cooperative signals to process trees.
    // Force the full tree while the parent PID still identifies its descendants;
    // otherwise the parent can exit first and orphan a transport grandchild.
    const args = ["/pid", String(child.pid), "/t", "/f"];
    const result = spawnSync("taskkill", args, { stdio: "ignore", windowsHide: true });
    if (result.status !== 0) child.kill(signal);
    return;
  }
  try {
    process.kill(-child.pid, signal);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ESRCH") child.kill(signal);
  }
}
