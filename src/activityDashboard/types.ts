import type { CodexProActionV1, ActionStatusResult } from "../audit.js";

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

export interface ActivityDashboardAction {
  actionId: string;
  sequence: number;
  finishedAt: string;
  projectId?: string;
  projectLabel: string;
  workspaceId?: string;
  attributionRecovered: boolean;
  toolName: string;
  operation: string;
  operationClass: CodexProActionV1["operation_class"];
  status: CodexProActionV1["status"];
  durationMs: number;
  mutating: boolean;
  headline: string;
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
  diff: string;
  diffTruncated: boolean;
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
  recentActions: ActivityDashboardAction[];
  timelineActions: ActivityDashboardAction[];
}

export interface ActivityBatchView {
  projectId: string;
  projectLabel: string;
  workspaceId?: string;
  path: string;
  autoStored: boolean;
  definition: unknown;
}
