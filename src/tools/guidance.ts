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
  const has = (name: string) => isToolAvailable(config, name);
  return [
    "CodexPro connects this session to explicitly allowed development workspaces.",
    has("work_status") ? "Optional durable work runs: call work_status to discover existing runs, claims and recovery evidence. Use work_manage(create) to plan a manual or ralph run; work_claim starts one packet; work_update checkpoints and finishes the iteration; work_manage(finish_run) verifies whole-run completion. Retain the returned attempt_token for managed workspace calls and operation_key for each mutation. Fresh agents can discover and resume without predecessor credentials. Run time, claim duration and iteration count are unlimited. Keep the idle lease alive with authenticated workspace calls or heartbeat; individual jobs remain bounded. No-progress warnings are advisory. Use todo_updates and acceptance_updates to grow plans across bounded requests. Only ralph mode has a 30-minute continuation recommendation, calculated by the server clock across consecutive packets using the same session_token. Respect blockers, stop requests and completion. Never estimate elapsed time yourself or wait to fill the target." : "",
    has("work_update") ? "Consolidate memory: one work_update checkpoint/finish_iteration can atomically save documents[], todos and handoff. A managed serial batch can also take checkpoint={expected_revision,summary,next_action,...} to save progress after every selected child succeeds. Use only the outer execution credential. Checkpoint data and credentials are not saved in batch files. Failed or unfinished verification skips the checkpoint; source edits remain applied. Retry an identical MCP request with the same operation_key for its receipt. Repair a failed checkpoint using work_update; do not rerun successful edits. Claim, recovery and iteration/run completion remain explicit work calls." : "",
    has("list_projects") ? "Call list_projects once and copy returned project/workspace ids unchanged." : "",
    config.worktreeMode === "mcp"
      ? "Start with create_workspace(project_id), or resume with open_workspace(workspace_id)."
      : config.projects.length > 1 ? "Read-only calls may use listed workspace_ids directly. Before editing, call open_workspace(project_id) once; reuse the workspace_id."
      : "Start with open_current_workspace; use open_workspace to switch allowed roots.",
    "Follow AGENTS.md guidance returned by the open call before editing.",
    inspectionGuidance(config),
    has("edit") ? "Prefer tagged edit for every one-file change. Use an edit_tag from a read or complete current-file search context that displayed every targeted range. Combine all same-file hunks in one edit; do not reuse tags after a mutation. Follow recovery hints after failure." : "Source writes are disabled.",
    has("apply_patch") ? "apply_patch accepts raw Git unified diffs and native *** Begin Patch syntax. Use it for deliberate multi-file changes or files tagged edit cannot handle. Never resend a failed patch unchanged." : "",
    has("bash") ? `bash runs ${config.bashMode === "full" ? "any shell command (full mode)" : "only allowlisted verification commands (safe mode)"}. Use it for tests, builds and project scripts. Outputs are bounded; inspect retained job output when truncated.` : "Bash is disabled.",
    has("commit_changes") ? "Use commit_changes when the user asks for a commit; it does not push." : "",
    has("batch") ? "Use direct tools for one or two ordinary reads. Use batch for three or more independent reads, coordinated distinct-file mutations, or actual verification. Combine same-file hunks; apply_patch remains exclusive. Verification batches persist by default; batch does not interpolate child outputs." : "",
    has("load_skill") ? "For skills, open the workspace with include_skills=true, then use load_skill by a discovered name." : "",
    has("handoff_to_agent") ? "Handoff/AI-Bridge tools are enabled; use them only when the user chooses that workflow." : "",
    config.codexSessions !== "off" ? "Use enabled Codex session tools only when the user asks for session history." : "",
    config.requireBashSession && config.bashSessionId ? `Bash calls require session_id=${JSON.stringify(config.bashSessionId)}.` : "",
    `Current modes: tool=${config.toolMode}, bash=${config.bashMode}, write=${config.writeMode}, handoff=${config.handoffMode}, debug_activity=${config.auditMode}.`
  ].filter(Boolean).join("\n");
}
