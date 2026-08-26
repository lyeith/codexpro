import { createHash, randomBytes } from "node:crypto";
import fs from "node:fs";
import fsp from "node:fs/promises";
import path from "node:path";
import type { CodexProConfig } from "../config.js";
import { CodexProError, isSubpath, type Workspace } from "../guard.js";
import type { ProjectDefinition, ProjectSummary } from "../projects/types.js";
import type { ToolCallContext } from "../toolContext.js";
import { FileLeaseStore } from "./leaseStore.js";
import { GitWorktreeDriver } from "./gitDriver.js";
import { KeyedMutex } from "./keyedMutex.js";
import type { CreateWorkspaceOptions, RepositoryInfo, WorktreeLease, WorkspaceHandle } from "./types.js";

interface ProjectRuntime {
  definition: ProjectDefinition;
  repository: RepositoryInfo;
  defaultBaseCommit: string;
  managedRoot: string;
}

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
  private readonly projects = new Map<string, ProjectRuntime>();
  private readonly mutex = new KeyedMutex();
  private readonly activeOperations = new Map<string, number>();
  private canonicalManagedRoot!: string;

  constructor(
    private readonly config: CodexProConfig,
    private readonly store = new FileLeaseStore(config.worktreeRoot),
    private readonly git = new GitWorktreeDriver(config.maxOutputBytes)
  ) {}

  async initialize(): Promise<void> {
    await this.store.initialize();
    this.canonicalManagedRoot = fs.realpathSync(this.config.worktreeRoot);

    for (const definition of this.config.projects) {
      const runtime = await this.projectRuntime(definition);
      this.projects.set(definition.id, runtime);
    }

    for (const lease of await this.store.loadAll()) {
      if (lease.version === 1) {
        const candidates = [...this.projects.values()].filter((project) => project.repository.repositoryId === lease.repositoryId);
        if (candidates.length !== 1) continue;
        lease.version = 2;
        lease.projectId = candidates[0].definition.id;
        lease.revision += 1;
        lease.updatedAt = new Date().toISOString();
        await this.store.save(lease);
      }
      if (!lease.projectId) continue;
      const project = this.projects.get(lease.projectId);
      if (!project || lease.repositoryId !== project.repository.repositoryId || lease.repositoryCommonDir !== project.repository.commonDir) continue;
      this.leases.set(lease.workspaceId, lease);
    }
    await this.reconcile();
  }

  private summaryFor(project: ProjectRuntime): ProjectSummary {
    return {
      id: project.definition.id,
      label: project.definition.label,
      default: project.definition.id === this.config.defaultProjectId,
      baseRef: project.definition.baseRef ?? this.config.worktreeBaseRef,
      maxWorktrees: Math.min(project.definition.maxWorktrees ?? this.config.maxWorktrees, this.config.maxWorktrees)
    };
  }

  private async projectRuntime(definition: ProjectDefinition): Promise<ProjectRuntime> {
    const repository = await this.git.inspectRepository(definition.root);
    if (repository.scopeRoot !== definition.root) {
      throw new CodexProError(`Project root must be canonical before registration: ${definition.id}`);
    }
    if (isSubpath(this.canonicalManagedRoot, repository.topLevel) || isSubpath(repository.topLevel, this.canonicalManagedRoot)) {
      throw new CodexProError(`Managed worktree storage must not overlap project ${definition.id} (${repository.topLevel}).`);
    }
    if ([...this.projects.values()].some((project) => project.repository.repositoryId === repository.repositoryId)) {
      throw new CodexProError(`Projects must not resolve to the same Git repository scope: ${definition.id}`);
    }
    const defaultBaseCommit = await this.git.resolveCommit(repository, definition.baseRef ?? this.config.worktreeBaseRef);
    const managedRoot = path.join(this.canonicalManagedRoot, "repositories", repository.repositoryId);
    await fsp.mkdir(path.join(managedRoot, "checkouts"), { recursive: true, mode: 0o700 });
    return {
      definition,
      repository,
      defaultBaseCommit,
      managedRoot: fs.realpathSync(managedRoot)
    };
  }

  listProjects(): ProjectSummary[] {
    return [...this.projects.values()].map((project) => this.summaryFor(project));
  }

  async addProject(definition: ProjectDefinition): Promise<ProjectSummary> {
    return this.mutex.runExclusive("project-catalog", async () => {
      if (this.projects.has(definition.id) || this.config.projects.some((project) => project.id === definition.id)) {
        throw new CodexProError(`Project id already exists: ${definition.id}`);
      }
      if (this.config.projects.some((project) => project.root === definition.root)) {
        throw new CodexProError(`Project root already exists: ${definition.root}`);
      }
      const creationParents = this.config.projectCreationRoots.map((creationRoot) => creationRoot.root);
      if (![...this.config.allowedRoots, ...creationParents].some((allowedRoot) => isSubpath(definition.root, allowedRoot))) {
        throw new CodexProError("New project root must stay inside an allowed project or creation root.");
      }
      const runtime = await this.projectRuntime(definition);
      this.projects.set(definition.id, runtime);
      this.config.projects.push(definition);
      if (!this.config.allowedRoots.includes(definition.root)) this.config.allowedRoots.push(definition.root);
      return this.summaryFor(runtime);
    });
  }

  repositoryInfo(projectId?: string): RepositoryInfo {
    return this.projectForId(projectId).repository;
  }

  private projectForId(projectId?: string): ProjectRuntime {
    const selected = projectId?.trim();
    if (!selected) {
      if (this.projects.size === 1) return [...this.projects.values()][0];
      throw new CodexProError("project_id is required when multiple projects are configured. Call list_projects first.");
    }
    const project = this.projects.get(selected);
    if (!project) throw new CodexProError(`Unknown project_id: ${selected}. Call list_projects first.`);
    return project;
  }

  private projectForLease(lease: WorktreeLease): ProjectRuntime {
    if (!lease.projectId) throw new CodexProError("Worktree lease has no project identity.");
    const project = this.projects.get(lease.projectId);
    if (!project) throw new CodexProError(`Project is no longer configured: ${lease.projectId}`);
    if (lease.repositoryId !== project.repository.repositoryId || lease.repositoryCommonDir !== project.repository.commonDir) {
      throw new CodexProError("Worktree lease belongs to a different project repository.");
    }
    return project;
  }

  private checkoutRootFor(project: ProjectRuntime, workspaceId: string): string {
    return path.join(project.managedRoot, "checkouts", sha256(workspaceId).slice(0, 32));
  }

  private repositoryLock(project: ProjectRuntime): string {
    return `repository:${sha256(project.repository.commonDir)}`;
  }

  private assertManagedLease(lease: WorktreeLease): ProjectRuntime {
    const project = this.projectForLease(lease);
    const managedRoot = path.resolve(project.managedRoot);
    const expectedCheckout = path.resolve(this.checkoutRootFor(project, lease.workspaceId));
    const expectedWorkspace = path.resolve(
      project.repository.scopeRelativePath ? path.join(expectedCheckout, project.repository.scopeRelativePath) : expectedCheckout
    );
    if (path.resolve(lease.checkoutRoot) !== expectedCheckout || path.resolve(lease.workspaceRoot) !== expectedWorkspace) {
      throw new CodexProError("Worktree lease paths do not match its workspace_id and project scope.");
    }
    if (!isSubpath(path.resolve(lease.checkoutRoot), managedRoot) || !isSubpath(path.resolve(lease.workspaceRoot), lease.checkoutRoot)) {
      throw new CodexProError("Worktree lease contains a path outside the managed project root.");
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
    return project;
  }

  private workspaceFor(lease: WorktreeLease): Workspace {
    return {
      id: lease.workspaceId,
      root: lease.workspaceRoot,
      openedAt: lease.createdAt,
      kind: "worktree",
      branch: lease.branch,
      baseCommit: lease.baseCommit,
      projectId: lease.projectId
    };
  }

  private leaseFor(context: ToolCallContext, workspaceId: string | undefined): WorktreeLease {
    if (!workspaceId) {
      throw new CodexProError("workspace_id is required in MCP worktree mode. Call create_workspace once, or reuse the workspace_id returned earlier.");
    }
    const lease = this.leases.get(workspaceId);
    if (!lease) throw new CodexProError("Unknown workspace_id.");
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
    const project = this.projectForId(options.projectId);
    const idempotencyKey = validateIdempotencyKey(options.idempotencyKey);
    const creationKeyHash = idempotencyKey
      ? sha256(`${project.definition.id}\0${project.repository.repositoryId}\0${context.principalId}\0${idempotencyKey}`)
      : context.transportSessionId
        ? sha256(`${project.definition.id}\0${project.repository.repositoryId}\0${context.principalId}\0transport:${context.transportSessionId}\0request:${context.requestId}`)
        : undefined;
    const requestedBaseCommit = options.baseRef
      ? await this.git.resolveCommit(project.repository, options.baseRef)
      : project.defaultBaseCommit;
    const creationLock = creationKeyHash ?? randomBytes(16).toString("hex");

    return this.mutex.runExclusive(`create:${creationLock}`, async () => {
      const reservation: { existing?: WorkspaceHandle; lease?: WorktreeLease } = await this.mutex.runExclusive("create:quota", async () => {
        const existing = creationKeyHash
          ? [...this.leases.values()].find((lease) => lease.creationKeyHash === creationKeyHash)
          : undefined;
        if (existing) {
          if (existing.ownerId !== context.principalId || existing.state !== "ready") {
            throw new CodexProError(`A previous workspace creation with this idempotency key is in state ${existing.state}.`);
          }
          if ((options.baseRef && requestedBaseCommit !== existing.baseCommit) || existing.projectId !== project.definition.id) {
            throw new CodexProError("idempotency_key was already used with a different project or base_ref.");
          }
          return {
            existing: {
              workspace: this.workspaceFor(existing),
              projectId: project.definition.id,
              branch: existing.branch,
              baseCommit: existing.baseCommit,
              created: false
            }
          };
        }

        const retained = [...this.leases.values()].filter((lease) => lease.state !== "removing").length;
        if (retained >= this.config.maxWorktrees) {
          throw new CodexProError(`Global worktree limit reached (${this.config.maxWorktrees}). Remove an existing clean managed workspace before creating another.`);
        }
        const projectRetained = [...this.leases.values()].filter(
          (lease) => lease.projectId === project.definition.id && lease.state !== "removing"
        ).length;
        const projectLimit = Math.min(project.definition.maxWorktrees ?? this.config.maxWorktrees, this.config.maxWorktrees);
        if (projectRetained >= projectLimit) {
          throw new CodexProError(`Worktree limit reached for project ${project.definition.id} (${projectLimit}).`);
        }

        const workspaceId = `wt_${randomBytes(24).toString("base64url")}`;
        const internalId = sha256(workspaceId);
        const checkoutRoot = this.checkoutRootFor(project, workspaceId);
        const workspaceRoot = project.repository.scopeRelativePath
          ? path.join(checkoutRoot, project.repository.scopeRelativePath)
          : checkoutRoot;
        const branch = `codexpro/mcp/${internalId.slice(0, 24)}`;
        const now = new Date().toISOString();
        const lease: WorktreeLease = {
          version: 2,
          revision: 1,
          workspaceId,
          projectId: project.definition.id,
          repositoryId: project.repository.repositoryId,
          repositoryCommonDir: project.repository.commonDir,
          checkoutRoot,
          workspaceRoot,
          branch,
          baseCommit: requestedBaseCommit,
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
        return { lease };
      });

      if (reservation.existing) return reservation.existing;
      const lease = reservation.lease;
      if (!lease) throw new CodexProError("Worktree reservation did not produce a lease.");
      try {
        await this.mutex.runExclusive(this.repositoryLock(project), async () => {
          await this.git.create(project.repository, lease.checkoutRoot, lease.branch, lease.baseCommit);
        });
        lease.checkoutRoot = fs.realpathSync(lease.checkoutRoot);
        lease.workspaceRoot = fs.realpathSync(lease.workspaceRoot);
        const inspected = await this.git.inspectRepository(lease.workspaceRoot);
        if (inspected.commonDir !== project.repository.commonDir) throw new CodexProError("Created worktree points at a different Git common directory.");
        if (await this.git.head(lease.workspaceRoot) !== lease.baseCommit) throw new CodexProError("Created worktree HEAD does not match the pinned base commit.");
        lease.state = "ready";
        lease.revision += 1;
        lease.updatedAt = new Date().toISOString();
        await this.store.save(lease);
        return {
          workspace: this.workspaceFor(lease),
          projectId: project.definition.id,
          branch: lease.branch,
          baseCommit: lease.baseCommit,
          created: true
        };
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
        const project = this.assertManagedLease(lease);
        if (lease.state === "provisioning") {
          if (!fs.existsSync(lease.workspaceRoot)) {
            lease.state = "failed";
            lease.failure = "Provisioning was interrupted before the worktree became available.";
          } else {
            const inspected = await this.git.inspectRepository(lease.workspaceRoot);
            if (inspected.commonDir !== project.repository.commonDir) throw new CodexProError("Interrupted worktree belongs to a different repository.");
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
            if (inspected.commonDir !== project.repository.commonDir) throw new CodexProError("Managed worktree belongs to a different repository.");
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
      const project = this.assertManagedLease(lease);
      lease.state = "removing";
      try {
        if ((this.activeOperations.get(workspaceId) ?? 0) > 0) throw new CodexProError("Workspace still has active operations.");
        if (await this.git.isDirty(lease.workspaceRoot)) throw new CodexProError("Refusing to remove a dirty worktree.");
        lease.revision += 1;
        lease.updatedAt = new Date().toISOString();
        await this.store.save(lease);
        await this.mutex.runExclusive(this.repositoryLock(project), () => this.git.remove(project.repository, lease.checkoutRoot));
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
