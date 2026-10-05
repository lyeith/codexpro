/** Resource ownership augments the existing scope; it never creates another process owner. */
import path from "node:path";
import { spawnSync } from "node:child_process";
import type { CodexProConfig } from "./config.js";

export interface JobResourceClaim { version: 1; helper: string; run_id: string; registration_id: string }
function invoke(helper: string, args: string[]): unknown {
  const result = spawnSync(helper, ["external", ...args], { encoding: "utf8", timeout: 10_000, maxBuffer: 1024 * 1024 });
  // Do not copy helper stderr into MCP errors: it may contain private paths.
  if (result.error || result.status !== 0) throw new Error("Host job resource operation failed.");
  try { return JSON.parse(result.stdout); } catch { throw new Error("Host job resource response was invalid."); }
}
export function resourceHelper(config: CodexProConfig): string | undefined {
  const helper = config.hostResourcesHelper;
  if (helper !== undefined && (process.platform !== "linux" || !path.isAbsolute(helper))) {
    throw new Error("Host job resources require an absolute helper path on Linux.");
  }
  return helper;
}
export function beginResources(helper: string, cwd: string, scopeUnit: string): JobResourceClaim {
  const result = invoke(helper, ["begin", "--purpose", "tool", "--cwd", cwd, "--owner-unit", scopeUnit, "--cache-consumer"]) as Record<string, unknown>;
  if (!/^[a-f0-9]{32}$/.test(String(result.run_id)) || !/^[a-f0-9]{32}$/.test(String(result.registration_id))) throw new Error("Host job resource registration was invalid.");
  return { version: 1, helper, run_id: String(result.run_id), registration_id: String(result.registration_id) };
}
export function resourceExec(claim: JobResourceClaim, command: string[]): [string, string[]] {
  return [claim.helper, ["external", "exec", claim.run_id, "--registration-id", claim.registration_id, "--", ...command]];
}
export function cancelResources(claim: JobResourceClaim): boolean {
  try { const result = invoke(claim.helper, ["cancel", claim.run_id, "--registration-id", claim.registration_id]) as Record<string, unknown>; return result.cancelled === true; } catch { return false; }
}
export function finishResources(claim: JobResourceClaim, exitCode: number): boolean {
  try { const result = invoke(claim.helper, ["finish", claim.run_id, "--registration-id", claim.registration_id, "--exit-code", String(exitCode)]) as Record<string, unknown>; return result.quiescent === true; } catch { return false; }
}
