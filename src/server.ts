import { serverGuidance } from "./tools/guidance.js";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CodexProConfig } from "./config.js";
import { EditSnapshotStore } from "./fsOps.js";
import { createDirectWorkspaceAccess, type WorkspaceAccess } from "./workspaceAccess.js";
import { createToolContext } from "./tools/context.js";
import { canCreateProjects } from "./tools/registry.js";
import { registerAdminTools, registerSupertool } from "./tools/admin.js";
import { registerProjectTools } from "./tools/projects.js";
import { registerWorkspaceTools } from "./tools/workspaces.js";
import { registerFileTools } from "./tools/files.js";
import { registerBashTools } from "./tools/bash.js";
import { registerGitTools } from "./tools/git.js";
import { registerHandoffTools } from "./tools/handoff.js";
import { registerBatchTools } from "./tools/batch.js";

const sharedEditSnapshots = new EditSnapshotStore();

export function createCodexProServer(config: CodexProConfig, workspaceAccess?: WorkspaceAccess): McpServer {
  if (config.worktreeMode === "mcp" && !workspaceAccess) {
    throw new Error("MCP worktree mode requires initialized workspace access.");
  }
  const workspaces = workspaceAccess ?? createDirectWorkspaceAccess(config);
  const server = new McpServer({ name: "CodexPro", version: "0.31.0" }, { instructions: serverGuidance(config) });

  // Registration order is the tools/list order; keep it stable.
  const ctx = createToolContext(config, server, workspaces, sharedEditSnapshots);
  registerSupertool(ctx);
  registerProjectTools(ctx);
  registerAdminTools(ctx);
  registerWorkspaceTools(ctx);
  registerFileTools(ctx);
  registerBashTools(ctx);
  registerGitTools(ctx);
  registerHandoffTools(ctx);
  registerBatchTools(ctx);

  return server;
}
