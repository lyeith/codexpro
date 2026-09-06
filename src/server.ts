import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CodexProConfig } from "./config.js";
import { EditSnapshotStore } from "./fsOps.js";
import { TOOL_CARD_LEGACY_URIS, TOOL_CARD_MIME_TYPE, TOOL_CARD_URI, toolCardWidgetHtml } from "./toolCardWidget.js";
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

function registerToolCardResource(server: McpServer, config: CodexProConfig): void {
  if (config.connectionTest) return;
  const s = server as any;
  if (typeof s.registerResource !== "function") {
    throw new Error("Unsupported MCP SDK: CodexPro widgets require registerResource.");
  }

  const registerUri = (uri: string, name: string): void => {
    s.registerResource(
      name,
      uri,
      {
        title: "CodexPro Tool Card",
        description: "Compact visual renderer for CodexPro workspace orientation, source changes, and handoffs.",
        mimeType: TOOL_CARD_MIME_TYPE
      },
      async () => ({
        contents: [
          {
            uri,
            mimeType: TOOL_CARD_MIME_TYPE,
            text: toolCardWidgetHtml,
            _meta: {
              ui: {
                prefersBorder: true,
                domain: config.widgetDomain,
                csp: {
                  connectDomains: [],
                  resourceDomains: []
                }
              },
              "openai/widgetDescription": "Renders CodexPro workspace orientation, diagnostics, file diffs, change reviews, terminal checks, Pro context exports, and handoff plans as compact developer cards with bounded previews.",
              "openai/widgetPrefersBorder": true,
              "openai/widgetDomain": config.widgetDomain,
              "openai/widgetCSP": {
                connect_domains: [],
                resource_domains: []
              }
            }
          }
        ]
      })
    );
  };

  registerUri(TOOL_CARD_URI, "codexpro-tool-card");
  for (const legacyUri of TOOL_CARD_LEGACY_URIS) {
    registerUri(legacyUri, `codexpro-tool-card-${legacyUri.match(/v\d+/)?.[0] ?? "legacy"}`);
  }
}

function serverInstructions(config: CodexProConfig): string {
  const editInstruction =
    config.connectionTest
      ? "4. Connection test mode is read-only. Write, patch, debug-export, and handoff-writing tools are unavailable."
      : config.writeMode === "workspace"
      ? "4. Prefer tagged edit for every one-file change. Immediately before editing, use an edit_tag from read or from a complete current-file search/ast_grep context that displayed every target range. Combine all same-file hunks into one edit. If edit fails, do not resend it unchanged: follow the structured recovery hint and refresh context when requested. Use apply_patch only for a deliberate raw Git multi-file diff or a mixed-line-ending file, never for *** Begin Patch wrapper syntax."
      : config.writeMode === "handoff"
        ? "4. Source writes are disabled and generic write/edit/apply_patch tools are unavailable. Use the enabled handoff tools for bounded .ai-bridge plans."
        : config.handoffMode === "on"
          ? "4. Write/edit/apply_patch tools are disabled. Use the enabled handoff tools for bounded .ai-bridge planning only."
          : "4. Write/edit/apply_patch and handoff tools are disabled. Do not attempt source or .ai-bridge writes.";
  const bashInstruction =
    config.bashMode === "off"
      ? "5. Bash is disabled and the bash tool is unavailable. Do not attempt shell commands."
      : config.bashMode === "full"
        ? "5. bash runs any shell command (full mode). Use it for tests, builds, lint, typecheck, project scripts and git operations that have no dedicated tool. Commit with commit_changes when the user asks for a commit."
        : "5. bash is in safe mode: only allowlisted verification commands (tests, build, lint, typecheck, project scripts) run. Commit with commit_changes.";

  return [
    config.worktreeMode === "mcp"
      ? "CodexPro gives each task an isolated Git worktree identified by an explicit workspace_id. When multiple projects are configured, call list_projects and choose project_id during create_workspace. Then copy workspace_id unchanged into every repository tool call."
      : config.projects.length > 1
        ? "CodexPro connects ChatGPT to a named catalog of allowed local development projects. Call list_projects once, then open_workspace with project_id or project_ids."
        : "CodexPro connects ChatGPT to explicitly allowed local development workspaces.",
    "",
    "Preferred workflow:",
    config.worktreeMode === "mcp"
      ? "1. If more than one project is available, call list_projects. Start with create_workspace(project_id). Resume prior work with open_workspace(workspace_id). Every later repository tool call must include that exact workspace_id."
      : config.projects.length > 1
        ? "1. Call list_projects once; it returns every project's workspace_id. For read-only questions pass that workspace_id straight to tree/search/read. Before editing, call open_workspace(project_id) once to load the project's AGENTS.md guidance (or open_workspace(project_ids=[...]) for several related projects). Reuse workspace_ids; never guess project ids that list_projects did not return."
        : "1. Start with open_current_workspace. Use open_workspace only when the user gives a different allowed root or asks to switch projects; that selection stays active for this MCP session.",
    canCreateProjects(config)
      ? "Project creation: call list_projects, then create_project with a returned parent_id (prefer a creation root when available). Open the returned project_id with open_workspace or create_workspace as directed."
      : "",
    "2. Follow any AGENTS.md-style instructions returned by the workspace open call before editing files.",
    "3. Inspect with tree, contextual search, ast_grep for structural syntax questions, and read only when returned context is insufficient. Prefer show_changes/tree/search/read over bash for git status, git diff and file reading: they are cheaper and carry edit tags.",
    editInstruction,
    bashInstruction,
    "6. Keep tool calls minimal. Do not wrap one or two ordinary reads, or a one-file mutation followed only by read/show_changes, in batch. Use one consolidated parallel batch for three or more independent reads/searches, or one serial batch for coordinated write/edit children that target distinct files followed by actual Bash verification. Combine same-file hunks into one edit; apply_patch remains exclusive. Verification batches persist by default; otherwise use persist=true explicitly. Batch does not interpolate child outputs.",
    config.codexSessions !== "off"
      ? `7. Codex session history access is enabled in ${config.codexSessions} mode. Use it only when the user asks for local Codex session history.`
      : "",
    config.requireBashSession && config.bashSessionId
      ? `8. Bash session guard is enabled. Every bash call must include session_id="${config.bashSessionId}".`
      : config.bashSessionId
        ? `8. Bash session label for this server is "${config.bashSessionId}".`
        : "",
    config.handoffMode === "on"
      ? "Handoff/AI-Bridge tools are enabled. Use them only when the user explicitly chooses that workflow."
      : "",
    config.auditMode === "metadata"
      ? "Debug activity tools are enabled for metadata-only engineering diagnostics; they are not day-to-day operational tools."
      : "",
    "",
    `Current modes: tool=${config.toolMode}, bash=${config.bashMode}, write=${config.writeMode}, handoff=${config.handoffMode}, debug_activity=${config.auditMode}.`
  ].filter(Boolean).join("\n");
}

const sharedEditSnapshots = new EditSnapshotStore();

export function createCodexProServer(config: CodexProConfig, workspaceAccess?: WorkspaceAccess): McpServer {
  if (config.worktreeMode === "mcp" && !workspaceAccess) {
    throw new Error("MCP worktree mode requires initialized workspace access.");
  }
  const workspaces = workspaceAccess ?? createDirectWorkspaceAccess(config);
  const server = new McpServer({ name: "CodexPro", version: "0.31.0" }, { instructions: serverInstructions(config) });
  registerToolCardResource(server, config);

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
