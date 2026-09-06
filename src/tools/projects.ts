import { z } from "zod";
import { createCatalogProject } from "../projects/create.js";
import type { ToolContext } from "./context.js";
import { PROJECT_CREATE_ANNOTATIONS, READ_ONLY_ANNOTATIONS, textResult, toolMeta } from "./shared.js";

export function registerProjectTools(ctx: ToolContext): void {
  const { config, workspaces } = ctx;

  ctx.register(

    "list_projects",
    {
      title: "List Projects",
      description: "List the configured projects with their ids and workspace_ids, plus creation roots for create_project. Call this first. The returned workspace_id can be passed straight to tree/search/read for read-only work; call open_workspace(project_id) before editing to load AGENTS.md guidance. Only ids returned here are valid.",
      inputSchema: {},
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("list_projects")
    },
    async () => {
      const projects = workspaces.listProjects().map((project) => ({
        ...project,
        ...(config.worktreeMode === "mcp" ? {} : { workspace_id: workspaces.workspaceIdForProject(project.id) })
      }));
      const creationRoots = config.projectCreationRoots.map(({ id, label }) => ({ id, label }));
      const projectText = projects.map((project) =>
        `- ${project.id} — ${project.label}${project.default ? " (default)" : ""}${project.workspace_id ? `; workspace_id=${project.workspace_id}` : ""}; base=${project.baseRef}${config.worktreeMode === "mcp" ? `; max_worktrees=${project.maxWorktrees}` : ""}`
      ).join("\n");
      const creationRootText = creationRoots.length
        ? creationRoots.map((creationRoot) => `- ${creationRoot.id} — ${creationRoot.label}`).join("\n")
        : "- none configured";
      const next = config.worktreeMode === "mcp"
        ? "Create one isolated workspace with create_workspace(project_id=...)."
        : "Read-only work can use a project's workspace_id directly with tree/search/read. Before editing, open_workspace(project_id=...) once (or open_workspace(project_ids=[...]) for several) to load AGENTS.md guidance; reuse the returned workspace_ids.";
      return textResult(
        `# Projects\n\n${projectText}\n\n# Creation Roots\n\n${creationRootText}\n\n# Next\n\n${next}`,
        {
          projects,
          count: projects.length,
          creation_roots: creationRoots,
          creation_root_count: creationRoots.length,
          default_project_id: config.defaultProjectId,
          multi_open_supported: config.worktreeMode !== "mcp",
          max_multi_open_projects: config.worktreeMode === "mcp" ? 0 : 12
        }
      );
    }
  );


  ctx.register(

    "create_project",
    {
      title: "Create Project",
      description:
        "Create a new project as a direct child of a named creation root or existing project, persist it in the projects catalog, and register it immediately. Prefer creation roots to avoid nesting repositories. source=empty creates a raw directory in direct mode. source=git either initializes a repository with an initial commit or clones repository when provided.",
      inputSchema: {
        project_id: z.string().regex(/^[a-z0-9][a-z0-9._-]{0,63}$/).describe("Stable lowercase project id to add to the catalog."),
        parent_id: z.string().min(1).describe("Creation-root or project id returned by list_projects whose root will contain the new direct-child directory."),
        label: z.string().max(120).optional().describe("Human-readable project label. Default: project_id."),
        directory: z.string().max(120).optional().describe("Portable direct-child directory name. Default: project_id. Path separators are not allowed."),
        source: z.enum(["empty", "git"]).describe("empty creates a raw directory; git initializes or clones a Git repository."),
        repository: z.string().max(2048).optional().describe("Optional HTTPS/SSH Git repository URL or allowed local path to clone when source=git."),
        initial_branch: z.string().max(255).optional().describe("Initial branch for a newly initialized repository. Default: main. Cannot be combined with repository."),
        base_ref: z.string().max(256).optional().describe("Optional configured worktree base ref. It must resolve after Git initialization or clone."),
        max_worktrees: z.number().int().min(1).max(512).optional().describe("Optional retained worktree limit for this project.")
      },
      annotations: PROJECT_CREATE_ANNOTATIONS,
      _meta: toolMeta("create_project")
    },
    async (args) => {
      const created = await createCatalogProject(
        config,
        {
          projectId: String(args.project_id ?? ""),
          parentId: String(args.parent_id ?? ""),
          label: args.label,
          directory: args.directory,
          source: args.source === "empty" ? "empty" : "git",
          repository: args.repository,
          initialBranch: args.initial_branch,
          baseRef: args.base_ref,
          maxWorktrees: args.max_worktrees
        },
        (project) => workspaces.addProject(project)
      );
      const nextTool = config.worktreeMode === "mcp" ? "create_workspace" : "open_workspace";
      const gitDetail = created.source === "empty"
        ? "not initialized"
        : created.cloned
          ? "cloned"
          : "initialized with an empty initial commit";
      const text = [
        "# Project Created",
        "",
        `Project ID: ${created.project.id}`,
        `Label: ${created.project.label}`,
        `Source: ${created.source}`,
        `Git: ${gitDetail}`,
        "Catalog: persisted and active in this server process",
        "",
        `Next: call ${nextTool} with project_id=${created.project.id}.`
      ].join("\n");
      return textResult(text, {
        project_id: created.project.id,
        parent_id: args.parent_id,
        project: created.summary,
        root: created.project.root,
        source: created.source,
        cloned: created.cloned,
        git_initialized: created.gitInitialized,
        initial_commit_created: created.initialCommitCreated,
        next_tool: nextTool
      });
    }
  );
}
