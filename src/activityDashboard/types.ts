import type { CodexProActionV1, ActionStatusResult } from "../audit.js";
import type { TimelineModel } from "./timeline.js";

/**
 * recorded: project_id was journaled with the action.
 * recovered: inferred (best effort) from another record sharing the workspace id.
 * unknown: a project_id was journaled but no catalog project has that id.
 * unattributed: no project could be determined.
 */
export type ActionAttribution = "recorded" | "recovered" | "unknown" | "unattributed";

export interface ActivityDashboardField {
  key: string;
  label: string;
  value: string;
  mono?: boolean;
  tone?: "positive" | "negative" | "muted";
}

export interface ActivityDashboardEvidence {
  label: string;
  value: string;
}

export interface ActivityDashboardGitEvidence {
  branch?: string;
  head?: string;
  dirty: boolean;
  changedPathCount: number;
}

export interface ActivityDashboardFact {
  label: string;
  value: string;
  tone?: "positive" | "negative" | "muted";
}

export interface ActivityDashboardAction {
  actionId: string;
  sequence: number;
  finishedAt: string;
  projectId?: string;
  projectLabel: string;
  workspaceId?: string;
  attribution: ActionAttribution;
  toolName: string;
  operation: string;
  operationClass: CodexProActionV1["operation_class"];
  status: CodexProActionV1["status"];
  durationMs: number;
  mutating: boolean;
  headline: string;
  /** Curated, class-specific facts shown in the expanded card (replaces raw request/result dumps). */
  facts: ActivityDashboardFact[];
  /** Paths the action read or searched (read-class tools). */
  readPaths: string[];
  changedPaths: string[];
  hiddenPathCount: number;
  changedPathsTruncated: boolean;
  requestFields: ActivityDashboardField[];
  resultFields: ActivityDashboardField[];
  pathEvidence: ActivityDashboardEvidence[];
  gitBefore?: ActivityDashboardGitEvidence;
  gitAfter?: ActivityDashboardGitEvidence;
  errorCode?: string;
  batchPath?: string;
  batchHref?: string;
  shellScripts: Array<{
    operationId?: string;
    script: string;
    truncated: boolean;
  }>;
}

export interface ActivityDashboardGit {
  available: boolean;
  message?: string;
  branch?: string;
  head?: string;
  committedAt?: string;
  dirty: boolean;
  trackedChangedPaths: string[];
  untrackedPaths: string[];
  hiddenPathCount: number;
  omittedPathCount: number;
  additions: number;
  deletions: number;
  /** True when a tracked diff can be fetched lazily from /activity/diff. */
  diffAvailable: boolean;
}

export interface ActivityProjectDiff {
  diff: string;
  truncated: boolean;
}

export interface ActivityDashboardProject {
  id: string;
  label: string;
  latestActivityAt?: string;
  actions: ActivityDashboardAction[];
  git: ActivityDashboardGit;
}

export interface ActivityDashboardSnapshot {
  generatedAt: string;
  audit: ActionStatusResult;
  projects: ActivityDashboardProject[];
  /** Newest actions across every project, newest first. */
  recentActions: ActivityDashboardAction[];
  /** Binned per-project view of the newest actions; undefined when the journal is empty. */
  timeline?: TimelineModel;
  timelineNote: string;
}

export interface ActivityBatchView {
  projectId: string;
  projectLabel: string;
  workspaceId?: string;
  path: string;
  autoStored: boolean;
  definition: unknown;
}
