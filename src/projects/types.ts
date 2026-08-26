export interface ProjectDefinition {
  id: string;
  label: string;
  root: string;
  baseRef?: string;
  maxWorktrees?: number;
}

export interface ProjectCreationRoot {
  id: string;
  label: string;
  root: string;
}

export interface ProjectCatalog {
  version: 1;
  filePath?: string;
  defaultProjectId: string;
  projects: ProjectDefinition[];
  creationRoots: ProjectCreationRoot[];
}

export interface ProjectSummary {
  id: string;
  label: string;
  default: boolean;
  baseRef: string;
  maxWorktrees: number;
}
