import type { AuditJournal } from "../audit.js";
/** Shared, redacted startup briefing. Audit timestamps describe observations, not exact edit times. */
export function recentActivity(journal: AuditJournal, projectId: string, workspaceId?: string, afterSequence?: number) {
  const recent = journal.list({ projectId, limit: 500, mutatingOnly: true });
  const changes = journal.list({ projectId, ...(afterSequence === undefined ? {} : { afterSequence }), limit: 10, mutatingOnly: true });
  const changed = recent.actions.filter(a => a.changed_path_count > 0 || (a.git_before?.head && a.git_after?.head && a.git_before.head !== a.git_after.head)).at(-1);
  return { enabled: changes.enabled, last_recorded_project_change: changed ? { at: changed.finished_at, action_id: changed.action_id, workspace_id: changed.workspace_id, changed_paths: changed.changed_paths.slice(0, 10) } : null,
    latest_recorded_activity: recent.actions.at(-1)?.finished_at ?? null,
    actions: changes.actions.map(a => ({ action_id: a.action_id, sequence: a.sequence, at: a.finished_at, tool: a.tool_name, status: a.status,
      actor_ref: a.actor_ref, workspace_id: a.workspace_id, changed_paths: a.changed_paths.slice(0, 10), changed_path_count: a.changed_path_count,
      attribution: a.workspace_id === workspaceId ? "this workspace; see actor/operation evidence" : "other or unattributed project work", work: a.work })),
    latest_sequence: changes.latest_sequence, next_sequence: changes.next_sequence, earliest_sequence: changes.earliest_sequence, has_more: changes.has_more, gap_detected: changes.gap_detected,
    coverage: "Recorded CodexPro activity only; the last-change scan covers at most 500 actions. Launch is not completion. External edits, older changes and expired records may be missing; inspect current source. A different worktree's changes may not be present here." };
}
