import type { CodexProConfig } from "../config.js";
import { isToolAvailable } from "./registry.js";

export function availableTools(config: CodexProConfig, names: string[]): string[] {
  return names.filter(name => isToolAvailable(config, name));
}

export function inspectionGuidance(config: CodexProConfig): string {
  const names = availableTools(config, ["tree", "search", "ast_grep", "read", "show_changes"]);
  return `Inspect files and changes with ${names.join(", ")}. Read and complete current-file search contexts establish edit tags; tree and change review do not.`;
}

export function serverGuidance(config: CodexProConfig): string {
  return [
    isToolAvailable(config, "list_projects") ? "Use list_projects to select a workspace; reuse its returned ids." : "",
    config.worktreeMode === "mcp"
      ? "Create a workspace with create_workspace, or open a retained workspace with open_workspace."
      : config.projects.length > 1 ? "Before editing, call open_workspace once to load project instructions."
      : "Before editing, call open_current_workspace to load project instructions.",
    "Follow the loaded AGENTS instructions and check for more specific instructions in subdirectories. Resolve unreadable or truncated instructions before editing.",
    isToolAvailable(config, "work_status") ? "For an existing managed run, use work_status to read its assignment, workspace_id and saved progress." : "",
    config.requireBashSession && config.bashSessionId ? `Bash requires session_id=${JSON.stringify(config.bashSessionId)}.` : ""
  ].filter(Boolean).join("\n");
}
