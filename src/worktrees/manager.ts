import { createHash, randomBytes } from "node:crypto";
import fs from "node:fs";
import fsp from "node:fs/promises";
import path from "node:path";
import type { CodexProConfig } from "../config.js";
import { CodexProError, isSubpath, type Workspace } from "../guard.js";
import type { ToolCallContext } from "../toolContext.js";
import { FileLeaseStore } from "./leaseStore.js";
import { GitWorktreeDriver } from "./gitDriver.js";
import { KeyedMutex } from "./keyedMutex.js";
import type { CreateWorkspaceOptions, RepositoryInfo, WorktreeLease, WorkspaceHandle } from "./types.js";

function sha256(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

function cleanLabel(value: string | undefined): string | undefined {
  const label = value?.replace(/\s+/g, " ").trim();
  return label ? label.slice(0, 120) : undefined;
}

function validateIdempotencyKey(value: string | undefined): string | undefined {
  const key = value?.trim();
  if (!key) return undefined;
  if (key.length > 200 || /[\0\r\n]/.test(key)) {
    throw new CodexProError("idempotency_key must be at most 200 characters without control newlines.");
  }
  return key;
}

export class WorktreeManager {
  private readonly leases = new Map<string, WorktreeLease>();
  private readonly mutex = new KeyedMutex();
  private readonly activeOperations = new Map<string, number>();
  private repository!: RepositoryInfo;
  private defaultBaseCommit!: string;
  private canonicalManagedRoot!: string;

  constructor(
    private readonly config: CodexProConfig,
    private readonly store = new FileLeaseStore(config.worktreeRoot),
    private readonly git = new GitWorktreeDriver(config.maxOutputBytes)
  ) {}

  async initialize(): Promise<void> {
    await this.store.initialize();
    this.repository = await this.git.inspectRepository(this.config.defaultRoot);
    const canonicalWorktreeRoot = fs.realpathSync(this.config.worktreeRoot);
    if (isSubpath(canonicalWorktreeRoot, this.repository.topLevel) || isSubpath(this.repository.topLevel, canonicalWorktreeRoot)) {
      throw new CodexProError("Managed worktree storage must not overlap the source Git repository.");
    }
    const managedRoot = path.join(this.config.worktreeRoot, "repositories", this.repository.repositoryId);
    await fsp.mkdir(path.join(managedRoot, "checkouts"), { recursive: true, mode: 0o700 });
    this.canonicalManagedRoot = fs.realpathSync(managedRoot);
    this.defaultBaseCommit = await this.git.resolveCommit(this.repository, this.config.worktreeBaseRef);
    for (const lease of await this.store.loadAll()) {
      if (lease.repositoryId !== this.repository.repositoryId) continue;
      this.leases.set(lease.workspaceId, lease);
    }
    await this.reconcile();
  }

  repositoryInfo(): RepositoryInfo {
    return this.repository;
  }

  private managedRepoRoot(): string {
    return this.canonicalManagedRoot;
  }

  private checkoutRootFor(workspaceId: string): string {
    return path.join(this.managedRepoRoot(), "checkouts", sha256(workspaceId).slice(0, 32));
  }

  private assertManagedLease(lease: WorktreeLease): void {
    const managedRoot = path.resolve(this.managedRepoRoot());
    const expectedCheckout = path.resolve(this.checkoutRootFor(lease.workspaceId));
    const expectedWorkspace = path.resolve(
      this.repository.scopeRelativePath ? path.join(expectedCheckout, this.repository.scopeRelativePath) : expectedCheckout
    );
    if (path.resolve(lease.checkoutRoot) !== expectedCheckout || path.resolve(lease.workspaceRoot) !== expectedWorkspace) {
      throw new CodexProError("Worktree lease paths do not match its workspace_id and repository scope.");
    }
    if (!isSubpath(path.resolve(lease.checkoutRoot), managedRoot) || !isSubpath(path.resolve(lease.workspaceRoot), lease.checkoutRoot)) {
      throw new CodexProError("Worktree lease contains a path outside the managed worktree root.");
    }
    if (lease.repositoryCommonDir !== this.repository.commonDir || lease.repositoryId !== this.repository.repositoryId) {
      throw new CodexProError("Worktree lease belongs to a different repository.");
    }
    if (fs.existsSync(lease.checkoutRoot)) {
      const realCheckout = fs.realpathSync(lease.checkoutRoot);
      if (!isSubpath(realCheckout, managedRoot)) throw new CodexProError("Worktree checkout resolves outside the managed root.");
    }
    if (fs.existsSync(lease.workspaceRoot)) {
      const realWorkspace = fs.realpathSync(lease.workspaceRoot);
      const realCheckout = fs.realpathSync(lease.checkoutRoot);
      if (!isSubpath(realWorkspace, realCheckout)) throw new CodexProError("Workspace scope resolves outside its managed checkout.");
    }
  }

  private workspaceFor(lease: WorktreeLease): Workspace {
    return {
      id: lease.workspaceId,
      root: lease.workspaceRoot,
      openedAt: lease.createdAt,
      kind: "worktree",
      branch: lease.branch,
      baseCommit: lease.baseCommit
    };
  }

  private leaseFor(context: ToolCallContext, workspaceId: string | undefined): WorktreeLease {
    if (!workspaceId) {
      throw new CodexProError("workspace_id is required in MCP worktree mode. Call create_workspace once, or reuse the workspace_id returned earlier.");
    }
    const lease = this.leases.get(workspaceId);
    if (!lease || lease.repositoryId !== this.repository.repositoryId) throw new CodexProError("Unknown workspace_id.");
    if (lease.ownerId !== context.principalId) throw new CodexProError("Workspace access denied for this authenticated principal.");
    if (lease.state !== "ready") throw new CodexProError(`Workspace is not ready (state: ${lease.state}).`);
    this.assertManagedLease(lease);
    if (!fs.existsSync(lease.workspaceRoot)) throw new CodexProError("Workspace checkout is missing; it will be reconciled on restart.");
    return lease;
  }

  getWorkspace(context: ToolCallContext, workspaceId?: string): Workspace {
    return this.workspaceFor(this.leaseFor(context, workspaceId));
  }

  listWorkspaces(context: ToolCallContext): Workspace[] {
    return [...this.leases.values()]
      .filter((lease) => lease.ownerId === context.principalId && lease.state === "ready")
      .map((lease) => this.workspaceFor(lease));
  }

  async createWorkspace(context: ToolCallContext, options: CreateWorkspaceOptions = {}): Promise<WorkspaceHandle> {
    const idempotencyKey = validateIdempotencyKey(options.idempotencyKey);
    const creationKeyHash = idempotencyKey
      ? sha256(`${this.repository.repositoryId}\0${context.principalId}\0${idempotencyKey}`)
      : context.transportSessionId
        ? sha256(`${this.repository.repositoryId}\0${context.principalId}\0transport:${context.transportSessionId}\0request:${context.requestId}`)
        : undefined;

    return this.mutex.runExclusive(`create:${creationKeyHash ?? randomBytes(16).toString("hex")}`, async () => {
      const existing = creationKeyHash
        ? [...this.leases.values()].find((lease) => lease.creationKeyHash === creationKeyHash)
        : undefined;
      if (existing) {
        if (existing.ownerId !== context.principalId || existing.state !== "ready") {
          throw new CodexProError(`A previous workspace creation with this idempotency key is in state ${existing.state}.`);
        }
        const requestedBase = options.baseRef
          ? await this.git.resolveCommit(this.repository, options.baseRef)
          : existing.baseCommit;
        if (requestedBase !== existing.baseCommit) {
          throw new CodexProError("idempotency_key was already used with a different base_ref.");
        }
        return { workspace: this.workspaceFor(existing), branch: existing.branch, baseCommit: existing.baseCommit, created: false };
      }

      const retained = [...this.leases.values()].filter((lease) => lease.state !== "removing").length;
      if (retained >= this.config.maxWorktrees) {
        throw new CodexProError(`Worktree limit reached (${this.config.maxWorktrees}). Remove an existing clean managed workspace before creating another.`);
      }

      const workspaceId = `wt_${randomBytes(24).toString("base64url")}`;
      const internalId = sha256(workspaceId);
      const checkoutRoot = this.checkoutRootFor(workspaceId);
      const workspaceRoot = this.repository.scopeRelativePath
        ? path.join(checkoutRoot, this.repository.scopeRelativePath)
        : checkoutRoot;
      const baseCommit = options.baseRef
        ? await this.git.resolveCommit(this.repository, options.baseRef)
        : this.defaultBaseCommit;
      const branch = `codexpro/mcp/${internalId.slice(0, 24)}`;
      const now = new Date().toISOString();
      const lease: WorktreeLease = {
        version: 1,
        revision: 1,
        workspaceId,
        repositoryId: this.repository.repositoryId,
        repositoryCommonDir: this.repository.commonDir,
        checkoutRoot,
        workspaceRoot,
        branch,
        baseCommit,
        ownerId: context.principalId,
        state: "provisioning",
        creationKeyHash,
        label: cleanLabel(options.label),
        createdAt: now,
        updatedAt: now,
        lastUsedAt: now
      };
      this.assertManagedLease(lease);
      await fsp.mkdir(path.dirname(checkoutRoot), { recursive: true, mode: 0o700 });
      this.leases.set(workspaceId, lease);
      await this.store.save(lease);

      try {
        await this.mutex.runExclusive(`repository:${this.repository.repositoryId}`, async () => {
          await this.git.create(this.repository, checkoutRoot, branch, baseCommit);
        });
        lease.checkoutRoot = fs.realpathSync(checkoutRoot);
        lease.workspaceRoot = fs.realpathSync(workspaceRoot);
        const inspected = await this.git.inspectRepository(workspaceRoot);
        if (inspected.commonDir !== this.repository.commonDir) throw new CodexProError("Created worktree points at a different Git common directory.");
        if (await this.git.head(workspaceRoot) !== baseCommit) throw new CodexProError("Created worktree HEAD does not match the pinned base commit.");
        lease.state = "ready";
        lease.revision += 1;
        lease.updatedAt = new Date().toISOString();
        await this.store.save(lease);
        return { workspace: this.workspaceFor(lease), branch, baseCommit, created: true };
      } catch (error) {
        lease.state = "failed";
        lease.failure = error instanceof Error ? error.message.slice(0, 1000) : String(error).slice(0, 1000);
        lease.revision += 1;
        lease.updatedAt = new Date().toISOString();
        await this.store.save(lease).catch(() => {});
        throw error;
      }
    });
  }

  async releaseWorkspace(context: ToolCallContext, workspaceId: string): Promise<Workspace> {
    return this.mutex.runExclusive(`workspace:${workspaceId}`, async () => {
      const lease = this.leaseFor(context, workspaceId);
      const now = new Date().toISOString();
      lease.releasedAt = now;
      lease.lastUsedAt = now;
      lease.updatedAt = now;
      lease.revision += 1;
      await this.store.save(lease);
      return this.workspaceFor(lease);
    });
  }

  async execute<T>(
    context: ToolCallContext,
    workspaceId: string | undefined,
    mutating: boolean,
    operation: () => Promise<T>
  ): Promise<T> {
    if (!workspaceId) return operation();
    const run = async () => {
      const lease = this.leaseFor(context, workspaceId);
      this.activeOperations.set(workspaceId, (this.activeOperations.get(workspaceId) ?? 0) + 1);
      try {
        const result = await operation();
        lease.lastUsedAt = new Date().toISOString();
        lease.updatedAt = lease.lastUsedAt;
        lease.revision += 1;
        await this.store.save(lease);
        return result;
      } finally {
        const active = (this.activeOperations.get(workspaceId) ?? 1) - 1;
        if (active <= 0) this.activeOperations.delete(workspaceId);
        else this.activeOperations.set(workspaceId, active);
      }
    };
    return mutating ? this.mutex.runExclusive(`workspace:${workspaceId}`, run) : run();
  }

  async reconcile(): Promise<void> {
    for (const lease of this.leases.values()) {
      try {
        this.assertManagedLease(lease);
        if (lease.state === "provisioning") {
          if (!fs.existsSync(lease.workspaceRoot)) {
            lease.state = "failed";
            lease.failure = "Provisioning was interrupted before the worktree became available.";
          } else {
            const inspected = await this.git.inspectRepository(lease.workspaceRoot);
            if (inspected.commonDir !== this.repository.commonDir) throw new CodexProError("Interrupted worktree belongs to a different repository.");
            lease.state = "ready";
          }
          lease.revision += 1;
          lease.updatedAt = new Date().toISOString();
          await this.store.save(lease);
        } else if (lease.state === "ready") {
          if (!fs.existsSync(lease.workspaceRoot)) {
            lease.state = "orphaned";
            lease.failure = "Managed worktree directory is missing.";
            lease.revision += 1;
            lease.updatedAt = new Date().toISOString();
            await this.store.save(lease);
          } else {
            const inspected = await this.git.inspectRepository(lease.workspaceRoot);
            if (inspected.commonDir !== this.repository.commonDir) throw new CodexProError("Managed worktree belongs to a different repository.");
          }
        } else if (lease.state === "removing") {
          if (!fs.existsSync(lease.checkoutRoot)) {
            this.leases.delete(lease.workspaceId);
            await this.store.delete(lease.workspaceId);
          } else {
            lease.state = "ready";
            lease.failure = "Removal was interrupted before Git removed the worktree.";
            lease.revision += 1;
            lease.updatedAt = new Date().toISOString();
            await this.store.save(lease);
          }
        }
      } catch (error) {
        lease.state = "orphaned";
        lease.failure = error instanceof Error ? error.message.slice(0, 1000) : String(error).slice(0, 1000);
        lease.revision += 1;
        lease.updatedAt = new Date().toISOString();
        await this.store.save(lease).catch(() => {});
      }
    }
  }

  async removeWorkspace(context: ToolCallContext, workspaceId: string): Promise<void> {
    await this.mutex.runExclusive(`workspace:${workspaceId}`, async () => {
      const lease = this.leaseFor(context, workspaceId);
      lease.state = "removing";
      try {
        if ((this.activeOperations.get(workspaceId) ?? 0) > 0) throw new CodexProError("Workspace still has active operations.");
        if (await this.git.isDirty(lease.workspaceRoot)) throw new CodexProError("Refusing to remove a dirty worktree.");
        lease.revision += 1;
        lease.updatedAt = new Date().toISOString();
        await this.store.save(lease);
        await this.mutex.runExclusive(`repository:${this.repository.repositoryId}`, () => this.git.remove(this.repository, lease.checkoutRoot));
        this.leases.delete(workspaceId);
        await this.store.delete(workspaceId);
      } catch (error) {
        if (this.leases.has(workspaceId) && fs.existsSync(lease.checkoutRoot)) {
          lease.state = "ready";
          lease.failure = error instanceof Error ? error.message.slice(0, 1000) : String(error).slice(0, 1000);
          lease.revision += 1;
          lease.updatedAt = new Date().toISOString();
          await this.store.save(lease).catch(() => {});
        }
        throw error;
      }
    });
  }
}
