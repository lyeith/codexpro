import { z } from "zod";
import { runBash } from "../bashOps.js";
import type { ToolContext } from "./context.js";
import { BASH_ANNOTATIONS, bashTextResult, textResult, toolMeta, workspaceIdSchema } from "./shared.js";

export function registerBashTools(ctx: ToolContext): void {
  const { config, workspaces, guard } = ctx;

  ctx.register(

    "bash",
    {
      title: "Bash",
      description: config.bashMode === "full"
        ? "Run one shell command in the workspace (full mode: no allowlist; chaining with &&, pipes and redirects is allowed). Use it for tests, builds, lint, typecheck, project scripts and git operations without a dedicated tool. Prefer read/search/tree/show_changes for reading files or reviewing diffs: they are cheaper and return edit tags. Blocked-path rules apply to file tools only, so bash can reach secrets and build outputs; never print credentials. The text result shows exit code and a bounded stdout/stderr tail; full output is in structured content."
        : "Run one allowlisted verification command in the workspace, such as tests, build, lint, typecheck, or a project script (safe mode). Do not use for git status/diff or file inspection; use show_changes, tree, search, and read instead. Do not chain commands with &&, pipes, redirects, or shell file readers. The text result shows exit code and a bounded stdout/stderr tail; full output is in structured content.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        command: z.string().describe("Command to run."),
        session_id: z.string().optional().describe(config.requireBashSession && config.bashSessionId ? `Required bash session id for this server: ${config.bashSessionId}.` : "Optional bash session id. If configured on the server, a provided value must match it."),
        cwd: z.string().optional().describe("Working directory relative to workspace root. Default: ."),
        timeout_ms: z
          .number()
          .int()
          .min(1000)
          .max(config.maxBashTimeoutMs)
          .optional()
          .describe(`Timeout in milliseconds. Default: 30000. Max: ${config.maxBashTimeoutMs}.`)
      },
      annotations: BASH_ANNOTATIONS,
      _meta: toolMeta("bash")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const result = await runBash(config, guard, workspace, String(args.command ?? ""), {
        cwd: args.cwd,
        timeoutMs: args.timeout_ms,
        sessionId: args.session_id
      });
      const text = bashTextResult(config, result);
      return textResult(text, { workspace_id: workspace.id, root: workspace.root, ...result, bash_session_id: result.bashSessionId ?? null });
    }
  );
}
