import { z } from "zod";
import { CodexProError, unknownProjectError, type Workspace } from "../guard.js";
import { workspaceSummary } from "../workspaceOps.js";
import { inspectWorkspace } from "../analysis/index.js";
import type { ToolContext } from "./context.js";
import {
  HANDOFF_WRITE_ANNOTATIONS,
  LOCAL_WRITE_ANNOTATIONS,
  READ_ONLY_ANNOTATIONS,
  SESSION_READ_ANNOTATIONS,
  boundedBatchStructuredContent,
  limitInt,
  parseBool,
  textResult,
  toolMeta,
  truncateUtf8WithMarker,
  usesToolCard,
  workspaceIdSchema
} from "./shared.js";

export function registerWorkspaceTools(ctx: ToolContext): void {
  const { config, workspaces, guard } = ctx;

  ctx.register(

    "create_workspace",
    {
      title: "Create Isolated Workspace",
      description:
        "Create one isolated Git worktree in a configured project and return its stable workspace_id. Call this exactly once at the start of a new task. For retries, reuse the same project_id and idempotency_key. After creation, pass workspace_id unchanged to every repository tool.",
      inputSchema: {
        project_id: config.projectsFile || config.projects.length > 1
          ? z.string().min(1).describe("Required project id returned by list_projects. Persistent catalogs can grow while this server is running.")
          : z.string().min(1).optional().describe("Configured project id. Optional only for a fixed single-project connector."),
        base_ref: z.string().optional().describe("Optional Git ref to pin as the worktree base. Default: the selected project's configured base ref."),
        label: z.string().max(120).optional().describe("Optional human-readable task label. It never controls the branch name or filesystem path."),
        idempotency_key: z.string().max(200).optional().describe("Stable retry key. Reusing it returns the same worktree instead of creating a duplicate."),
        include_tree: z.boolean().optional().describe("Include a compact file tree. Default: false for speed."),
        max_depth: z.number().int().min(1).max(8).optional().describe("Tree depth when include_tree=true. Default: 2."),
        include_skills: z.boolean().optional().describe("Discover workspace, user, and plugin skills. Default: true."),
        include_global_skills: z.boolean().optional().describe("Also scan installed user/plugin skills. Default: true.")
      },
      annotations: HANDOFF_WRITE_ANNOTATIONS,
      _meta: toolMeta("create_workspace")
    },
    async (args) => {
      const handle = await workspaces.createWorkspace({
        projectId: args.project_id,
        baseRef: args.base_ref,
        label: args.label,
        idempotencyKey: args.idempotency_key
      });
      const summary = await workspaceSummary(config, guard, handle.workspace, {
        includeTree: parseBool(args.include_tree, false),
        maxDepth: limitInt(args.max_depth, 2, 1, 8),
        includeSkills: parseBool(args.include_skills, true),
        includeGlobalSkills: parseBool(args.include_global_skills, true),
        bootstrapContext: false
      });
      const action = handle.created ? "Created" : "Reused";
      const text = [
        `# ${action} Isolated Workspace`,
        "",
        `Workspace ID: ${handle.workspace.id}`,
        `Project ID: ${handle.projectId}`,
        `Branch: ${handle.branch}`,
        `Base commit: ${handle.baseCommit}`,
        "",
        "Pass this workspace_id unchanged to every later repository tool call.",
        "",
        summary.text
      ].join("\n");
      return textResult(text, {
        workspace_id: handle.workspace.id,
        project_id: handle.projectId,
        root: handle.workspace.root,
        branch: handle.branch,
        base_commit: handle.baseCommit,
        created: handle.created,
        agents_loaded: summary.agentsLoaded,
        agents_path: summary.agentsPath,
        skills: summary.skills,
        skill_inventory: summary.skillInventory,
        skill_counts: summary.skillCounts,
        tree: summary.tree,
        git_status: summary.gitStatus,
        worktree_mode: config.worktreeMode
      });
    }
  );


  ctx.register(

    "list_workspaces",
    {
      title: "List Workspaces",
      description: "List workspaces opened in this MCP session and identify the currently selected workspace.",
      inputSchema: {},
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("list_workspaces")
    },
    async () => {
      const selectedWorkspaceId = workspaces.currentWorkspaceId();
      const current = workspaces.listWorkspaces();
      const text = current
        .map((workspace) => `- ${workspace.id} — ${workspace.root}${workspace.projectId ? ` project=${workspace.projectId}` : ""}${workspace.branch ? ` [${workspace.branch}]` : ""}${workspace.id === selectedWorkspaceId ? " (selected)" : ""} (opened ${workspace.openedAt})`)
        .join("\n");
      return textResult(text, {
        workspaces: current,
        count: current.length,
        selected_workspace_id: selectedWorkspaceId
      });
    }
  );


  ctx.register(

    "open_current_workspace",
    {
      title: "Open Current Workspace",
      description:
        "Open and select the configured default workspace for this MCP session. Use this to return to the launch workspace after switching roots.",
      inputSchema: {
        include_tree: z.boolean().optional().describe("Include a compact file tree. Default: false for speed."),
        max_depth: z.number().int().min(1).max(8).optional().describe("Tree depth when include_tree=true. Default: 2."),
        include_skills: z.boolean().optional().describe("Discover skills by name/description. Default: false for speed."),
        include_global_skills: z.boolean().optional().describe("Also scan installed user/plugin skills when include_skills=true. Default: false.")
      },
      annotations: SESSION_READ_ANNOTATIONS,
      _meta: toolMeta("open_current_workspace")
    },
    async (args) => {
      const workspace = workspaces.selectDefaultWorkspace();
      const summary = await workspaceSummary(config, guard, workspace, {
        includeTree: parseBool(args.include_tree, false),
        maxDepth: limitInt(args.max_depth, 2, 1, 8),
        includeSkills: parseBool(args.include_skills, false),
        includeGlobalSkills: parseBool(args.include_global_skills, false),
        bootstrapContext: false
      });
      return textResult(summary.text, {
        workspace_id: summary.workspaceId,
        selected_workspace_id: summary.workspaceId,
        project_id: workspace.projectId ?? config.defaultProjectId,
        root: summary.root,
        agents_loaded: summary.agentsLoaded,
        agents_path: summary.agentsPath,
        skills: summary.skills,
        skill_inventory: summary.skillInventory,
        skill_counts: summary.skillCounts,
        tree: summary.tree,
        git_status: summary.gitStatus,
        bash_mode: config.bashMode,
        write_mode: config.writeMode,
        tool_mode: config.toolMode
      });
    }
  );


  ctx.register(

    "open_workspace",
    {
      title: "Open Workspace",
      description:
        config.worktreeMode === "mcp"
          ? "Resume an existing isolated Git worktree by its stable workspace_id. This never accepts a local path and never creates a new worktree."
          : (config.projectsFile
              ? "Open a catalog project by project_id (or several with project_ids) and return its workspace_id, AGENTS.md guidance, git status and an optional tree. Only ids returned by list_projects are valid; an unknown id fails and lists the valid ones. Not required before read-only tools (list_projects already gives workspace_ids), but call it once before editing. Reuse workspace_ids instead of reopening."
              : "Open one allowed local project directory (root) or resolve several named projects in one call. With project_ids, duplicate ids are collapsed and the first project becomes the selected primary. Reuse returned workspace_ids instead of reopening projects."),
      inputSchema: config.worktreeMode === "mcp"
        ? {
            workspace_id: z.string().describe("Stable workspace_id returned by create_workspace."),
            include_tree: z.boolean().optional().describe("Include a compact file tree. Default: true."),
            max_depth: z.number().int().min(1).max(8).optional().describe("Tree depth. Default: 3."),
            max_entries: z.number().int().min(1).max(3000).optional().describe("Maximum tree entries. Default: 500."),
            include_skills: z.boolean().optional().describe("Discover skills by name/description. Default: false for speed."),
            include_global_skills: z.boolean().optional().describe("Also scan installed user/plugin skills when include_skills=true. Default: false.")
          }
        : {
            project_id: z.string().min(1).optional().describe("One project id from list_projects. Cannot be combined with project_ids."),
            project_ids: z.array(z.string().min(1)).min(1).max(12).optional().describe(
              "Open several projects in one call. Duplicate ids are collapsed; the first project becomes the selected primary workspace. Cannot be combined with project_id."
            ),
            ...(config.projectsFile
              ? {}
              : {
                  root: z.string().optional().describe("Project directory to open. Omit to use CODEXPRO_ROOT/current working directory. Supports ~/ paths."),
                  path: z.string().optional().describe("Alias for root. Useful for clients that naturally send path instead of root.")
                }),
            include_tree: z.boolean().optional().describe(
              "Include compact file trees. Defaults to true for a newly opened singular workspace, false for repeated singular opens and project_ids arrays."
            ),
            max_depth: z.number().int().min(1).max(8).optional().describe("Tree depth. Default: 3."),
            max_entries: z.number().int().min(1).max(3000).optional().describe(
              "Maximum tree entries. Default: 500. With project_ids, this is one total budget divided across the opened workspaces."
            ),
            include_skills: z.boolean().optional().describe("Discover skills by name/description. Default: false for speed."),
            include_global_skills: z.boolean().optional().describe("Also scan installed user/plugin skills when include_skills=true. Default: false."),
            bootstrap_context: z.boolean().optional().describe("Deprecated and ignored. Use handoff_to_agent to create .ai-bridge files.")
          },
      annotations: SESSION_READ_ANNOTATIONS,
      _meta: toolMeta("open_workspace")
    },
    async (args) => {
      if (config.worktreeMode !== "mcp" && args.root && args.path && args.root !== args.path) {
        throw new CodexProError("open_workspace accepts either root or path. If both are provided, they must match.");
      }

      const requestedProjectIds: string[] = config.worktreeMode === "mcp" || !Array.isArray(args.project_ids)
        ? []
        : [...new Set<string>(args.project_ids.map((value: unknown) => String(value).trim()))];
      if (requestedProjectIds.some((projectId) => !projectId)) {
        throw new CodexProError("project_ids must contain non-empty project ids from list_projects.");
      }
      if (requestedProjectIds.length && (args.project_id || args.root || args.path)) {
        throw new CodexProError("open_workspace accepts project_ids or one project_id/root/path target, not both.");
      }
      if (config.worktreeMode !== "mcp" && args.project_id && (args.root || args.path)) {
        throw new CodexProError("open_workspace accepts project_id or root/path, not both.");
      }

      const arrayMode = requestedProjectIds.length > 0;
      const previouslyOpen = new Set(workspaces.listWorkspaces().map((workspace) => workspace.id));
      let openedWorkspaces: Workspace[];
      let selectedWorkspaceId: string;

      if (arrayMode) {
        const knownProjectIds = new Set(workspaces.listProjects().map((project) => project.id));
        const unknownProjectIds = requestedProjectIds.filter((projectId) => !knownProjectIds.has(projectId));
        if (unknownProjectIds.length) throw unknownProjectError(config, unknownProjectIds);
        openedWorkspaces = requestedProjectIds.map((projectId) => workspaces.openProject(projectId));
        // Each open selects its workspace. Re-select the first request so array order
        // consistently identifies the primary workspace rather than the final item.
        workspaces.openProject(requestedProjectIds[0]);
        selectedWorkspaceId = openedWorkspaces[0].id;
      } else {
        const workspace = config.worktreeMode === "mcp"
          ? workspaces.getWorkspace(args.workspace_id)
          : args.project_id
            ? workspaces.openProject(String(args.project_id))
            : workspaces.openWorkspace(args.root ?? args.path);
        openedWorkspaces = [workspace];
        selectedWorkspaceId = workspace.id;
      }

      const includeTree = args.include_tree !== undefined
        ? parseBool(args.include_tree, false)
        : arrayMode
          ? false
          : config.worktreeMode === "mcp"
            ? true
            : !previouslyOpen.has(openedWorkspaces[0].id);
      const totalTreeEntries = limitInt(args.max_entries, 500, 1, 3000);
      const treeEntriesPerWorkspace = arrayMode && includeTree
        ? Math.max(1, Math.floor(totalTreeEntries / openedWorkspaces.length))
        : totalTreeEntries;
      const summaries = await Promise.all(openedWorkspaces.map((workspace) =>
        workspaceSummary(config, guard, workspace, {
          includeTree,
          maxDepth: limitInt(args.max_depth, 3, 1, 8),
          maxEntries: treeEntriesPerWorkspace,
          includeSkills: parseBool(args.include_skills, false),
          includeGlobalSkills: parseBool(args.include_global_skills, false),
          bootstrapContext: false
        })
      ));
      const entries = summaries.map((summary, index) => {
        const workspace = openedWorkspaces[index];
        return {
          workspace_id: summary.workspaceId,
          project_id: workspace.projectId ?? null,
          root: summary.root,
          already_open: previouslyOpen.has(workspace.id),
          agents_loaded: summary.agentsLoaded,
          agents_path: summary.agentsPath,
          skills: summary.skills,
          skill_inventory: summary.skillInventory,
          skill_counts: summary.skillCounts,
          tree: summary.tree,
          git_status: summary.gitStatus,
          branch: workspace.branch,
          base_commit: workspace.baseCommit
        };
      });

      if (arrayMode) {
        const rows = entries.map((entry) => {
          const label = entry.project_id ?? entry.workspace_id;
          const gitHeadline = entry.git_status.split("\n").find((line) => line.trim()) ?? "Git status unavailable";
          return [
            `- ${label} — ${entry.workspace_id}${entry.already_open ? " (already open)" : " (opened)"}`,
            `  Root: ${entry.root}`,
            `  ${entry.agents_loaded ? `Instructions: ${entry.agents_path ?? "AGENTS.md"}` : "Instructions: none"}`,
            `  Git: ${gitHeadline}`
          ].join("\n");
        });
        const trees = entries
          .filter((entry) => entry.tree)
          .map((entry) => `## ${entry.project_id ?? entry.workspace_id} files\n\n${entry.tree}`);
        const text = [
          "# Workspaces Opened",
          "",
          `Count: ${entries.length}`,
          `Selected primary: ${entries[0].project_id ?? entries[0].workspace_id} (${selectedWorkspaceId})`,
          "Reuse the returned workspace_ids for later calls; reopening them is unnecessary.",
          "",
          ...rows,
          ...(trees.length ? ["", ...trees] : [])
        ].join("\n");
        const boundedText = truncateUtf8WithMarker(
          text,
          config.maxOutputBytes,
          "\n...[multi-workspace output truncated]"
        );
        const workspaceResultBudget = Math.max(2_000, Math.floor(config.maxOutputBytes / entries.length));
        const boundedEntries = entries.map((entry) => boundedBatchStructuredContent(entry, workspaceResultBudget));
        return textResult(boundedText.value, {
          workspaces: boundedEntries.map((entry) => entry.value),
          workspace_ids: entries.map((entry) => entry.workspace_id),
          project_ids: entries.map((entry) => entry.project_id).filter(Boolean),
          count: entries.length,
          already_open_count: entries.filter((entry) => entry.already_open).length,
          primary_workspace_id: selectedWorkspaceId,
          selected_workspace_id: selectedWorkspaceId,
          selected_project_id: entries[0].project_id,
          include_tree: includeTree,
          tree_max_entries_per_workspace: includeTree ? treeEntriesPerWorkspace : 0,
          output_truncated: boundedText.truncated,
          workspace_results_truncated_count: boundedEntries.filter((entry) => entry.truncated).length,
          bash_mode: config.bashMode,
          write_mode: config.writeMode,
          tool_mode: config.toolMode,
          worktree_mode: config.worktreeMode
        });
      }

      const summary = summaries[0];
      const workspace = openedWorkspaces[0];
      const entry = entries[0];
      const text = entry.already_open && args.include_tree === undefined
        ? `${summary.text}\n\nThis workspace was already open. Reuse workspace_id=${summary.workspaceId}; another open_workspace call is unnecessary.`
        : summary.text;
      return textResult(text, {
        workspace_id: summary.workspaceId,
        primary_workspace_id: summary.workspaceId,
        selected_workspace_id: summary.workspaceId,
        project_id: workspace.projectId ?? null,
        root: summary.root,
        already_open: entry.already_open,
        agents_loaded: summary.agentsLoaded,
        agents_path: summary.agentsPath,
        skills: summary.skills,
        skill_inventory: summary.skillInventory,
        skill_counts: summary.skillCounts,
        tree: summary.tree,
        git_status: summary.gitStatus,
        workspaces: entries,
        count: 1,
        include_tree: includeTree,
        bash_mode: config.bashMode,
        write_mode: config.writeMode,
        tool_mode: config.toolMode,
        worktree_mode: config.worktreeMode,
        branch: workspace.branch,
        base_commit: workspace.baseCommit
      });
    }
  );


  ctx.register(

    "release_workspace",
    {
      title: "Release Workspace",
      description: "Mark an isolated workspace idle while preserving its files, branch, and uncommitted changes. This does not remove the Git worktree.",
      inputSchema: {
        workspace_id: z.string().describe("Stable workspace_id returned by create_workspace.")
      },
      annotations: HANDOFF_WRITE_ANNOTATIONS,
      _meta: toolMeta("release_workspace")
    },
    async (args) => {
      const workspace = await workspaces.releaseWorkspace(String(args.workspace_id ?? ""));
      return textResult(
        `# Workspace Released\n\nWorkspace: ${workspace.id}\nBranch: ${workspace.branch ?? "unknown"}\n\nThe worktree and all changes were preserved.`,
        { workspace_id: workspace.id, project_id: workspace.projectId ?? null, root: workspace.root, branch: workspace.branch, released: true }
      );
    }
  );


  ctx.register(

    "remove_workspace",
    {
      title: "Remove Clean Workspace",
      description: "Remove a managed Git worktree only when it has no uncommitted or untracked changes. The branch is preserved. Use release_workspace when work may still be needed.",
      inputSchema: {
        workspace_id: z.string().describe("Stable workspace_id returned by create_workspace.")
      },
      annotations: LOCAL_WRITE_ANNOTATIONS,
      _meta: toolMeta("remove_workspace")
    },
    async (args) => {
      const workspaceId = String(args.workspace_id ?? "");
      await workspaces.removeWorkspace(workspaceId);
      return textResult(
        `# Workspace Removed\n\nWorkspace: ${workspaceId}\n\nThe clean worktree was removed. Its Git branch was preserved.`,
        { workspace_id: workspaceId, removed: true, branch_preserved: true }
      );
    }
  );


  ctx.register(

    "inspect_workspace",
    {
      title: "Inspect Workspace",
      description: "Build a bounded repository map with languages, project types, entrypoints, areas, symbols, relationships, and coverage warnings.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        path: z.string().optional().describe("Optional workspace-relative area to emphasize. Default: entire workspace."),
        max_files: z.number().int().min(1).max(100000).optional().describe("Maximum returned file records. Default: 300."),
        include_symbols: z.boolean().optional().describe("Include symbols in structured output. Default: true."),
        include_relationships: z.boolean().optional().describe("Include relationships in structured output. Default: true."),
        max_symbols: z.number().int().min(1).max(100000).optional().describe("Maximum returned symbols. Analysis remains bounded by server config."),
        max_relationships: z.number().int().min(1).max(250000).optional().describe("Maximum returned relationships. Analysis remains bounded by server config.")
      },
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("inspect_workspace")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      if (args.path) guard.resolve(workspace, args.path);
      const result = await inspectWorkspace(config, guard, workspace);
      const prefix = typeof args.path === "string" && args.path.trim()
        ? guard.resolve(workspace, args.path).relPath.replace(/^\.\/?$/, "")
        : "";
      const inScope = (filePath: string) => !prefix || filePath === prefix || filePath.startsWith(`${prefix}/`);
      const areaInScope = (areaPath: string) => !prefix || areaPath === "." || inScope(areaPath) || prefix.startsWith(`${areaPath}/`);
      const cardWorkspaceAnalysis = usesToolCard(config, "inspect_workspace");
      const fileLimit = cardWorkspaceAnalysis ? 120 : limitInt(args.max_files, 300, 1, config.analysisLimits.maxInventoryFiles);
      const symbolLimit = cardWorkspaceAnalysis ? 80 : limitInt(args.max_symbols, 500, 1, config.analysisLimits.maxSymbols);
      const relationshipLimit = cardWorkspaceAnalysis ? 120 : limitInt(args.max_relationships, 800, 1, config.analysisLimits.maxRelationships);
      const scopedFiles = result.files.filter((file) => inScope(file.path));
      const scopedSymbols = result.symbols.filter((symbol) => inScope(symbol.path));
      const scopedRelationships = result.relationships.filter((relationship) => inScope(relationship.from) || inScope(relationship.to));
      const files = scopedFiles.slice(0, fileLimit);
      const symbols = args.include_symbols === false
        ? []
        : scopedSymbols.slice(0, symbolLimit);
      const relationships = args.include_relationships === false
        ? []
        : scopedRelationships.slice(0, relationshipLimit);
      const outputLimited = files.length < scopedFiles.length ||
        (args.include_symbols !== false && symbols.length < scopedSymbols.length) ||
        (args.include_relationships !== false && relationships.length < scopedRelationships.length);
      const outputWarnings = [
        ...result.warnings,
        ...(outputLimited ? ["Structured output was limited. Use path or max_* arguments to request a narrower or larger result."] : [])
      ];
      const text = [
        "# Workspace Analysis",
        "",
        `Workspace: ${workspace.root}`,
        `Projects: ${result.projectTypes.join(", ") || "unknown"}`,
        `Languages: ${result.languages.join(", ") || "unknown"}`,
        `Entrypoints: ${result.entrypoints.filter(inScope).join(", ") || "none detected"}`,
        `Coverage: ${result.coverage.analyzedFiles}/${result.coverage.inventoryFiles} files analyzed, ${result.coverage.symbolCount} symbols, ${result.coverage.relationshipCount} relationships${result.coverage.truncated ? " (partial)" : ""}`,
        `Returned: ${files.length} files, ${symbols.length} symbols, ${relationships.length} relationships`,
        ...(outputWarnings.length ? ["", "## Warnings", "", ...outputWarnings.map((warning) => `- ${warning}`)] : [])
      ].join("\n");
      return textResult(text, {
        schema_version: 1,
        workspace_id: workspace.id,
        root: workspace.root,
        path: args.path ?? ".",
        languages: result.languages,
        project_types: result.projectTypes,
        entrypoints: result.entrypoints.filter(inScope),
        important_files: result.importantFiles.filter(inScope),
        areas: result.areas.filter((area) => areaInScope(area.path)),
        files,
        symbols,
        relationships,
        coverage: result.coverage,
        warnings: outputWarnings,
        output_limited: outputLimited,
        returned: { files: files.length, symbols: symbols.length, relationships: relationships.length },
        cache: result.cache
      });
    }
  );
}
