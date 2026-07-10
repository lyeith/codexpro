import type { CodexProConfig } from "./config.js";
import { CodexProError, WorkspaceManager, type Workspace } from "./guard.js";
import type { ProjectSummary } from "./projects/types.js";
import { currentToolContext, type ToolCallContext } from "./toolContext.js";
import { WorktreeManager } from "./worktrees/manager.js";
import type { CreateWorkspaceOptions, WorkspaceHandle } from "./worktrees/types.js";

export interface WorkspaceAccess {
  readonly mode: "direct" | "mcp";
  defaultWorkspace(): Workspace;
  openWorkspace(rootInput?: string): Workspace;
  openProject(projectId: string): Workspace;
  getWorkspace(id?: string): Workspace;
  listWorkspaces(): Workspace[];
  listProjects(): ProjectSummary[];
  createWorkspace(options?: CreateWorkspaceOptions): Promise<WorkspaceHandle>;
  releaseWorkspace(workspaceId: string): Promise<Workspace>;
  removeWorkspace(workspaceId: string): Promise<void>;
  execute<T>(workspaceId: string | undefined, mutating: boolean, operation: () => Promise<T>): Promise<T>;
}

function requiredContext(): ToolCallContext {
  const context = currentToolContext();
  if (!context) throw new CodexProError("MCP worktree operation is missing request context.");
  return context;
}

class DirectWorkspaceAccess implements WorkspaceAccess {
  readonly mode = "direct" as const;
  private readonly manager: WorkspaceManager;

  constructor(config: CodexProConfig) {
    this.manager = new WorkspaceManager(config);
  }

  defaultWorkspace(): Workspace { return this.manager.defaultWorkspace(); }
  openWorkspace(rootInput?: string): Workspace { return this.manager.openWorkspace(rootInput); }
  openProject(projectId: string): Workspace { return this.manager.openProject(projectId); }
  getWorkspace(id?: string): Workspace { return this.manager.getWorkspace(id); }
  listWorkspaces(): Workspace[] { return this.manager.listWorkspaces(); }
  listProjects(): ProjectSummary[] { return this.manager.listProjects(); }
  async createWorkspace(): Promise<WorkspaceHandle> { throw new CodexProError("MCP worktree mode is disabled."); }
  async releaseWorkspace(): Promise<Workspace> { throw new CodexProError("MCP worktree mode is disabled."); }
  async removeWorkspace(): Promise<void> { throw new CodexProError("MCP worktree mode is disabled."); }
  async execute<T>(_workspaceId: string | undefined, _mutating: boolean, operation: () => Promise<T>): Promise<T> { return operation(); }
}

class WorktreeWorkspaceAccess implements WorkspaceAccess {
  readonly mode = "mcp" as const;

  constructor(private readonly manager: WorktreeManager) {}

  defaultWorkspace(): Workspace {
    throw new CodexProError("There is no shared default workspace in MCP worktree mode. Call create_workspace.");
  }
  openWorkspace(workspaceId?: string): Workspace { return this.getWorkspace(workspaceId); }
  openProject(): Workspace { throw new CodexProError("Create an isolated workspace for a project with create_workspace."); }
  getWorkspace(id?: string): Workspace { return this.manager.getWorkspace(requiredContext(), id); }
  listWorkspaces(): Workspace[] { return this.manager.listWorkspaces(requiredContext()); }
  listProjects(): ProjectSummary[] { return this.manager.listProjects(); }
  createWorkspace(options: CreateWorkspaceOptions = {}): Promise<WorkspaceHandle> {
    return this.manager.createWorkspace(requiredContext(), options);
  }
  releaseWorkspace(workspaceId: string): Promise<Workspace> {
    return this.manager.releaseWorkspace(requiredContext(), workspaceId);
  }
  removeWorkspace(workspaceId: string): Promise<void> {
    return this.manager.removeWorkspace(requiredContext(), workspaceId);
  }
  execute<T>(workspaceId: string | undefined, mutating: boolean, operation: () => Promise<T>): Promise<T> {
    return this.manager.execute(requiredContext(), workspaceId, mutating, operation);
  }
}

export async function createWorkspaceAccess(config: CodexProConfig): Promise<WorkspaceAccess> {
  if (config.worktreeMode !== "mcp") return new DirectWorkspaceAccess(config);
  const manager = new WorktreeManager(config);
  await manager.initialize();
  return new WorktreeWorkspaceAccess(manager);
}

export function createDirectWorkspaceAccess(config: CodexProConfig): WorkspaceAccess {
  return new DirectWorkspaceAccess(config);
}
