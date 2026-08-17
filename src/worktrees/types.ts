import type { Workspace } from "../guard.js";

export type WorktreeLeaseState = "provisioning" | "ready" | "removing" | "orphaned" | "failed";

export interface RepositoryInfo {
  scopeRoot: string;
  topLevel: string;
  commonDir: string;
  scopeRelativePath: string;
  repositoryId: string;
}

export interface WorktreeLease {
  version: 1 | 2;
  revision: number;
  workspaceId: string;
  projectId?: string;
  repositoryId: string;
  repositoryCommonDir: string;
  checkoutRoot: string;
  workspaceRoot: string;
  branch: string;
  baseCommit: string;
  ownerId: string;
  state: WorktreeLeaseState;
  creationKeyHash?: string;
  label?: string;
  createdAt: string;
  updatedAt: string;
  lastUsedAt: string;
  releasedAt?: string;
  failure?: string;
}

export interface WorkspaceHandle {
  workspace: Workspace;
  projectId: string;
  branch: string;
  baseCommit: string;
  created: boolean;
}

export interface CreateWorkspaceOptions {
  projectId?: string;
  baseRef?: string;
  label?: string;
  idempotencyKey?: string;
}
