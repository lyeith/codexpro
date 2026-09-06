import type { CodexProConfig, ToolMode } from "../config.js";

/**
 * Single source of truth for which CodexPro tools exist, when each one is
 * available, and how it participates in cross-cutting behaviour (audit,
 * batch, tool cards, connection-test hiding).
 *
 * `toolNamesForMode` and `isToolAvailable` are both derived from this table,
 * so the self-test's "expected vs registered" comparison and the registration
 * guard can never drift apart.
 */

export const SUPERTOOL_NAME = "codexpro";

export const SUPERTOOL_ACTION_ALIASES: Record<string, string> = {
  actions: "list_actions",
  config: "server_config",
  self_test: "codexpro_self_test",
  inventory: "codexpro_inventory",
  open: "open_current_workspace",
  changes: "show_changes",
  commit: "commit_changes",
  ast: "ast_grep",
  handoff_poll: "wait_for_handoff",
  pro_export: "export_pro_context",
  agent_handoff: "handoff_to_agent"
};

export function normalizeSupertoolAction(value: unknown): string {
  const raw = String(value ?? "list_actions").trim();
  const normalized = raw.toLowerCase().replace(/[\s-]+/g, "_");
  return SUPERTOOL_ACTION_ALIASES[normalized] ?? normalized;
}

/** Runtime conditions a tool can depend on. Every predicate reads only `config`. */
export type ToolRequirement =
  | "write"              // writeMode === "workspace"
  | "bash"               // bashMode !== "off"
  | "handoff"            // handoffMode !== "off"
  | "handoffWrite"       // writeMode === "handoff"
  | "worktree"           // worktreeMode === "mcp"
  | "directWorkspaces"   // worktreeMode !== "mcp"
  | "audit"              // auditMode === "metadata"
  | "analysis"           // analysisEnabled
  | "projectsFile"       // a persistent projects catalog is configured
  | "projectCreation"    // canCreateProjects(config)
  | "multiProject"       // config.projects.length > 1
  | "singleProject"      // config.projects.length <= 1
  | "codexSessions"      // codexSessions !== "off"
  | "codexSessionsRead"; // codexSessions === "read"

export interface ToolDescriptor {
  /** Lowest tool mode that exposes the tool: minimal ⊂ standard ⊂ full. */
  tier: ToolMode;
  /** Every listed requirement must hold. */
  requires?: ToolRequirement[];
  /** At least one listed requirement must hold. */
  requiresAny?: ToolRequirement[];
  /**
   * When every listed requirement holds the tool is exposed in every tool
   * mode, ignoring `tier` (but still honouring `requires` and connection-test
   * hiding).
   */
  forceWhen?: ToolRequirement[];
  /** Counts as a workspace mutation for the audit journal and workspace access serialisation. */
  mutating?: boolean;
  /** Hidden when CODEXPRO_CONNECTION_TEST is on. */
  connectionTestHidden?: boolean;
  /** Runs outside any single workspace lock (project/worktree lifecycle). */
  globalLifecycle?: boolean;
  /** May be invoked as a child of the batch tool. */
  batchChild?: boolean;
  /** Parallel-safe as a batch child. */
  batchParallel?: boolean;
  /** Renders a tool card when CODEXPRO_TOOL_CARDS is on. */
  toolCard?: boolean;
  /** Apps SDK status strings shown while the tool runs / after it finishes. */
  invoking?: string;
  invoked?: string;
}

const TIER_RANK: Record<ToolMode, number> = { minimal: 0, standard: 1, full: 2 };

export function canCreateProjects(config: CodexProConfig): boolean {
  return Boolean(config.projectsFile) && config.writeMode === "workspace" && !config.connectionTest;
}

const REQUIREMENTS: Record<ToolRequirement, (config: CodexProConfig) => boolean> = {
  write: (config) => config.writeMode === "workspace",
  bash: (config) => config.bashMode !== "off",
  handoff: (config) => config.handoffMode !== "off",
  handoffWrite: (config) => config.writeMode === "handoff",
  worktree: (config) => config.worktreeMode === "mcp",
  directWorkspaces: (config) => config.worktreeMode !== "mcp",
  audit: (config) => config.auditMode === "metadata",
  analysis: (config) => config.analysisEnabled,
  projectsFile: (config) => Boolean(config.projectsFile),
  projectCreation: (config) => canCreateProjects(config),
  multiProject: (config) => config.projects.length > 1,
  singleProject: (config) => config.projects.length <= 1,
  codexSessions: (config) => config.codexSessions !== "off",
  codexSessionsRead: (config) => config.codexSessions === "read"
};

/**
 * Keyed in registration order, which is also the `tools/list` order.
 *
 * Notes on the special cases encoded here:
 * - Worktree lifecycle tools stay available in every tool mode when
 *   worktree=mcp (tier "minimal" + requires "worktree"); otherwise a
 *   minimal-mode session could create a worktree it can never release or
 *   remove. They are never exposed outside mcp mode.
 * - With a multi-project catalog the "current" workspace is just the default
 *   project; exposing open_current_workspace steered agents away from
 *   list_projects → open_workspace, so it requires a single project.
 * - handoff_to_agent is forced on in every tier when writeMode=handoff (the
 *   only way to write in that mode), as long as handoffMode is not off.
 * - Codex session tools are opt-in by config and ignore the tool mode.
 */
export const TOOL_DESCRIPTORS: Readonly<Record<string, ToolDescriptor>> = {
  [SUPERTOOL_NAME]: {
    tier: "full",
    connectionTestHidden: true,
    invoking: "Running CodexPro supertool action...",
    invoked: "CodexPro supertool action complete"
  },
  list_projects: {
    tier: "minimal",
    requiresAny: ["worktree", "multiProject", "projectCreation"],
    invoking: "Listing configured projects...",
    invoked: "Configured projects ready"
  },
  create_project: {
    tier: "minimal",
    requires: ["projectsFile", "write"],
    connectionTestHidden: true,
    globalLifecycle: true,
    invoking: "Creating CodexPro project...",
    invoked: "CodexPro project created"
  },
  server_config: {
    tier: "minimal",
    invoking: "Reading CodexPro server config...",
    invoked: "CodexPro server config ready"
  },
  activity_list: {
    tier: "minimal",
    requires: ["audit"],
    invoking: "Reading CodexPro debug action journal...",
    invoked: "CodexPro debug actions ready"
  },
  activity_get: {
    tier: "minimal",
    requires: ["audit"],
    invoking: "Reading CodexPro debug action...",
    invoked: "CodexPro debug action ready"
  },
  activity_status: {
    tier: "minimal",
    requires: ["audit"],
    invoking: "Reading CodexPro debug activity status...",
    invoked: "CodexPro debug activity status ready"
  },
  activity_export: {
    tier: "minimal",
    requires: ["audit"],
    invoking: "Exporting CodexPro debug actions...",
    invoked: "CodexPro debug action export ready"
  },
  codexpro_self_test: {
    tier: "minimal",
    mutating: true,
    connectionTestHidden: true,
    invoking: "Running CodexPro self-test...",
    invoked: "CodexPro self-test complete"
  },
  codexpro_inventory: {
    tier: "full",
    invoking: "Reading CodexPro inventory...",
    invoked: "CodexPro inventory ready"
  },
  load_skill: {
    tier: "standard",
    invoking: "Loading skill instructions...",
    invoked: "Skill instructions loaded"
  },
  create_workspace: {
    tier: "minimal",
    requires: ["worktree"],
    globalLifecycle: true,
    invoking: "Creating isolated Git worktree...",
    invoked: "Isolated workspace ready"
  },
  list_workspaces: {
    tier: "full",
    requires: ["directWorkspaces"],
    invoking: "Listing CodexPro workspaces...",
    invoked: "CodexPro workspaces listed"
  },
  open_current_workspace: {
    tier: "minimal",
    requires: ["directWorkspaces", "singleProject"],
    toolCard: true,
    invoking: "Opening current CodexPro workspace...",
    invoked: "Current CodexPro workspace opened"
  },
  open_workspace: {
    tier: "minimal",
    toolCard: true,
    invoking: "Opening CodexPro workspace...",
    invoked: "CodexPro workspace opened"
  },
  release_workspace: {
    tier: "minimal",
    requires: ["worktree"],
    globalLifecycle: true,
    invoking: "Releasing isolated workspace...",
    invoked: "Workspace released"
  },
  remove_workspace: {
    tier: "minimal",
    requires: ["worktree"],
    globalLifecycle: true,
    invoking: "Checking and removing clean workspace...",
    invoked: "Workspace removal complete"
  },
  inspect_workspace: {
    tier: "standard",
    requires: ["analysis"],
    batchChild: true,
    batchParallel: true,
    toolCard: true,
    invoking: "Inspecting workspace analysis...",
    invoked: "Workspace analysis ready"
  },
  tree: {
    tier: "standard",
    batchChild: true,
    batchParallel: true,
    invoking: "Listing workspace files...",
    invoked: "Workspace files listed"
  },
  search: {
    tier: "standard",
    batchChild: true,
    batchParallel: true,
    invoking: "Searching workspace...",
    invoked: "Workspace search complete"
  },
  ast_grep: {
    tier: "standard",
    batchChild: true,
    batchParallel: true,
    invoking: "Searching syntax trees...",
    invoked: "Structural search complete"
  },
  read: {
    tier: "minimal",
    batchChild: true,
    batchParallel: true,
    invoking: "Reading file...",
    invoked: "File read"
  },
  view_image: {
    tier: "standard"
  },
  write: {
    tier: "minimal",
    requires: ["write"],
    mutating: true,
    connectionTestHidden: true,
    batchChild: true,
    invoking: "Writing file...",
    invoked: "File written"
  },
  edit: {
    tier: "minimal",
    requires: ["write"],
    mutating: true,
    connectionTestHidden: true,
    batchChild: true,
    invoking: "Editing file...",
    invoked: "File edited"
  },
  apply_patch: {
    tier: "minimal",
    requires: ["write"],
    mutating: true,
    connectionTestHidden: true,
    batchChild: true,
    invoking: "Applying patch...",
    invoked: "Patch applied"
  },
  import_file: {
    tier: "minimal",
    requires: ["write"],
    mutating: true,
    connectionTestHidden: true,
    invoking: "Importing attachment...",
    invoked: "Attachment imported"
  },
  jobs: {
    tier: "minimal",
    requires: ["bash"],
    invoking: "Checking background jobs...",
    invoked: "Background jobs listed"
  },
  stop_job: {
    tier: "minimal",
    requires: ["bash"],
    mutating: true,
    connectionTestHidden: true,
    invoking: "Stopping background job...",
    invoked: "Background job stopped"
  },
  bash: {
    tier: "minimal",
    requires: ["bash"],
    mutating: true,
    connectionTestHidden: true,
    batchChild: true,
    toolCard: true,
    invoking: "Running bash command...",
    invoked: "Bash command finished"
  },
  show_changes: {
    tier: "minimal",
    batchChild: true,
    toolCard: true,
    invoking: "Summarizing workspace changes...",
    invoked: "Workspace changes summarized"
  },
  commit_changes: {
    tier: "minimal",
    requires: ["write"],
    mutating: true,
    connectionTestHidden: true,
    invoking: "Committing workspace changes...",
    invoked: "Commit created"
  },
  read_handoff: {
    tier: "standard",
    requires: ["handoff"],
    invoking: "Reading agent handoff context...",
    invoked: "Agent handoff context ready"
  },
  wait_for_handoff: {
    tier: "standard",
    requires: ["handoff"],
    invoking: "Waiting for local handoff result...",
    invoked: "Local handoff state ready"
  },
  codex_context: {
    tier: "full",
    requires: ["handoff"],
    invoking: "Loading Codex context...",
    invoked: "Codex context ready"
  },
  export_pro_context: {
    tier: "standard",
    requires: ["handoff"],
    mutating: true,
    connectionTestHidden: true,
    invoking: "Exporting Pro context...",
    invoked: "Pro context exported"
  },
  codex_sessions: {
    tier: "minimal",
    requires: ["codexSessions"],
    invoking: "Listing local Codex sessions...",
    invoked: "Codex sessions ready"
  },
  read_codex_session: {
    tier: "minimal",
    requires: ["codexSessionsRead"],
    invoking: "Reading local Codex session...",
    invoked: "Codex session read"
  },
  handoff_to_agent: {
    tier: "standard",
    requires: ["handoff"],
    forceWhen: ["handoffWrite"],
    mutating: true,
    connectionTestHidden: true,
    toolCard: true,
    invoking: "Writing agent handoff plan...",
    invoked: "Agent handoff plan written"
  },
  batch: {
    tier: "minimal",
    invoking: "Running batch...",
    invoked: "Batch complete"
  }
};

export const TOOL_NAMES: readonly string[] = Object.freeze(Object.keys(TOOL_DESCRIPTORS));

export function toolDescriptor(name: string): ToolDescriptor | undefined {
  return Object.prototype.hasOwnProperty.call(TOOL_DESCRIPTORS, name) ? TOOL_DESCRIPTORS[name] : undefined;
}

function holds(config: CodexProConfig, requirement: ToolRequirement): boolean {
  return REQUIREMENTS[requirement](config);
}

/** Whether `name` is registered (and therefore listed) for this config. */
export function isToolAvailable(config: CodexProConfig, name: string): boolean {
  const descriptor = toolDescriptor(name);
  if (!descriptor) return false;
  if (config.connectionTest && descriptor.connectionTestHidden) return false;
  if (descriptor.requires?.some((requirement) => !holds(config, requirement))) return false;
  if (descriptor.requiresAny && !descriptor.requiresAny.some((requirement) => holds(config, requirement))) return false;
  if (descriptor.forceWhen?.length && descriptor.forceWhen.every((requirement) => holds(config, requirement))) return true;
  return TIER_RANK[descriptor.tier] <= (TIER_RANK[config.toolMode] ?? TIER_RANK.standard);
}

/** Every tool the server registers for this config, in registration order. */
export function toolNamesForMode(config: CodexProConfig): string[] {
  return TOOL_NAMES.filter((name) => isToolAvailable(config, name));
}

function namesWhere(predicate: (descriptor: ToolDescriptor, name: string) => boolean): string[] {
  return TOOL_NAMES.filter((name) => predicate(TOOL_DESCRIPTORS[name], name));
}

// Derived views of the table, kept for the audit, batch, and tool-card code paths.
export const MINIMAL_TOOL_NAMES: readonly string[] = namesWhere((d) => TIER_RANK[d.tier] <= TIER_RANK.minimal);
export const STANDARD_TOOL_NAMES: readonly string[] = namesWhere((d) => TIER_RANK[d.tier] <= TIER_RANK.standard);
export const FULL_TOOL_NAMES: readonly string[] = namesWhere(() => true);

export const WORKTREE_TOOL_NAMES = new Set<string>(namesWhere((d) => Boolean(d.requires?.includes("worktree"))));
export const HANDOFF_TOOL_NAMES = new Set<string>(namesWhere((d) => Boolean(d.requires?.includes("handoff"))));
export const ACTIVITY_TOOL_NAMES = new Set<string>(namesWhere((d) => Boolean(d.requires?.includes("audit"))));
export const GLOBAL_LIFECYCLE_TOOLS = new Set<string>(namesWhere((d) => d.globalLifecycle === true));
export const MUTATING_WORKSPACE_TOOLS = new Set<string>(namesWhere((d) => d.mutating === true));
export const CONNECTION_TEST_HIDDEN_TOOLS = new Set<string>(namesWhere((d) => d.connectionTestHidden === true));
export const TOOL_CARD_RENDER_TOOL_NAMES = new Set<string>(namesWhere((d) => d.toolCard === true));

export const BATCH_ALLOWED_CHILD_TOOLS = new Set<string>(namesWhere((d) => d.batchChild === true));
export const BATCH_PARALLEL_CHILD_TOOLS = new Set<string>(namesWhere((d) => d.batchChild === true && d.batchParallel === true));
/** Batch children that mutate files: the batch children gated on write mode. */
export const BATCH_FILE_MUTATION_TOOLS = new Set<string>(namesWhere((d) => d.batchChild === true && Boolean(d.requires?.includes("write"))));
/** Batch children that execute commands: the batch children gated on bash mode. */
export const BATCH_EXECUTION_TOOLS = new Set<string>(namesWhere((d) => d.batchChild === true && Boolean(d.requires?.includes("bash"))));
export const BATCH_MUTATING_CHILD_TOOLS = new Set<string>([...BATCH_FILE_MUTATION_TOOLS, ...BATCH_EXECUTION_TOOLS]);
