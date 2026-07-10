import { createHash, randomBytes } from "node:crypto";
import fs from "node:fs";
import fsp from "node:fs/promises";
import path from "node:path";
import { CodexProError, isSubpath } from "../guard.js";
import type { WorktreeLease } from "./types.js";

function handleHash(workspaceId: string): string {
  return createHash("sha256").update(workspaceId).digest("hex");
}

function isLease(value: unknown): value is WorktreeLease {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const lease = value as Partial<WorktreeLease>;
  return (lease.version === 1 || lease.version === 2) &&
    typeof lease.workspaceId === "string" &&
    (lease.version === 1 || typeof lease.projectId === "string") &&
    typeof lease.repositoryId === "string" &&
    typeof lease.repositoryCommonDir === "string" &&
    typeof lease.checkoutRoot === "string" &&
    typeof lease.workspaceRoot === "string" &&
    typeof lease.branch === "string" &&
    typeof lease.baseCommit === "string" &&
    typeof lease.ownerId === "string" &&
    ["provisioning", "ready", "removing", "orphaned", "failed"].includes(String(lease.state)) &&
    typeof lease.createdAt === "string" &&
    typeof lease.updatedAt === "string" &&
    typeof lease.lastUsedAt === "string";
}

export class FileLeaseStore {
  readonly leasesDir: string;

  constructor(readonly root: string) {
    this.leasesDir = path.join(root, "leases");
  }

  async initialize(): Promise<void> {
    await fsp.mkdir(this.leasesDir, { recursive: true, mode: 0o700 });
    try {
      await fsp.chmod(this.leasesDir, 0o700);
    } catch {
      // Best effort on filesystems without POSIX permissions.
    }
  }

  pathFor(workspaceId: string): string {
    return path.join(this.leasesDir, `${handleHash(workspaceId)}.json`);
  }

  async loadAll(): Promise<WorktreeLease[]> {
    await this.initialize();
    const entries = await fsp.readdir(this.leasesDir, { withFileTypes: true });
    const leases: WorktreeLease[] = [];
    for (const entry of entries) {
      if (!entry.isFile() || !entry.name.endsWith(".json")) continue;
      const file = path.join(this.leasesDir, entry.name);
      try {
        const parsed = JSON.parse(await fsp.readFile(file, "utf8"));
        if (!isLease(parsed) || handleHash(parsed.workspaceId) + ".json" !== entry.name) {
          throw new Error("lease schema or filename does not match");
        }
        leases.push(parsed);
      } catch (error) {
        const quarantine = `${file}.invalid-${Date.now()}-${randomBytes(4).toString("hex")}`;
        await fsp.rename(file, quarantine).catch(() => {});
        console.error(`[CodexPro] quarantined invalid worktree lease ${entry.name}: ${error instanceof Error ? error.message : String(error)}`);
      }
    }
    return leases;
  }

  async save(lease: WorktreeLease): Promise<void> {
    await this.initialize();
    const destination = this.pathFor(lease.workspaceId);
    if (!isSubpath(destination, this.leasesDir)) throw new CodexProError("Worktree lease path escaped its store.");
    const temporary = `${destination}.tmp-${process.pid}-${randomBytes(6).toString("hex")}`;
    await fsp.writeFile(temporary, `${JSON.stringify(lease, null, 2)}\n`, { mode: 0o600, flag: "wx" });
    try {
      await fsp.rename(temporary, destination);
      try {
        await fsp.chmod(destination, 0o600);
      } catch {
        // Best effort on filesystems without POSIX permissions.
      }
    } finally {
      if (fs.existsSync(temporary)) await fsp.rm(temporary, { force: true }).catch(() => {});
    }
  }

  async delete(workspaceId: string): Promise<void> {
    await fsp.rm(this.pathFor(workspaceId), { force: true });
  }
}
