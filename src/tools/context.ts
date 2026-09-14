import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { z } from "zod";
import type { CodexProConfig } from "../config.js";
import { PathGuard, CodexProError, type Workspace } from "../guard.js";
import type { EditSnapshotStore } from "../fsOps.js";
import { elapsedLabel, getJobManager, type JobManager, type JobRecord } from "../jobs.js";
import { AuditJournal, type ActionEvidenceSnapshot } from "../audit.js";
import { contextFromRequest, runWithToolContext, type ToolCallContext } from "../toolContext.js";
import type { WorkspaceAccess } from "../workspaceAccess.js";
import type { WorkRuntime } from "../work/runtime.js";
import { recentActivity } from "../work/activity.js";
import { pathRedactions, redactPathsDeep, redactPathsInText } from "../pathLabels.js";
import { auditStructuredResult, errorResult } from "./shared.js";
import {
  BATCH_MUTATING_CHILD_TOOLS,
  GLOBAL_LIFECYCLE_TOOLS,
  MUTATING_WORKSPACE_TOOLS,
  SUPERTOOL_NAME,
  WORKTREE_TOOL_NAMES,
  ACTIVITY_TOOL_NAMES,
  isToolAvailable,
  normalizeSupertoolAction
} from "./registry.js";

export type CodexToolHandler = (args: any) => Promise<any> | any;
export type CodexToolValidator = (args: any) => any;

/** Everything a tool family needs to register its tools on one server instance. */
export interface ToolContext {
  config: CodexProConfig;
  server: McpServer;
  workspaces: WorkspaceAccess;
  guard: PathGuard;
  /** Shared across servers so edit tags stay valid across HTTP sessions. */
  editSnapshots: EditSnapshotStore;
  /** show_changes "since=last_shown" checkpoints, per server. */
  reviewCheckpoints: Map<string, string>;
  /** Background job runner (process-wide, keyed by jobs dir). */
  jobs: JobManager;
  work?: WorkRuntime;
  /** Lazily created metadata audit journal for this server. */
  auditJournal(): AuditJournal;
  /** Register a tool if the registry exposes it for this config; otherwise a no-op. */
  register(name: string, options: Record<string, unknown>, handler: CodexToolHandler): void;
  /** Names registered so far, in registration order. */
  registeredToolNames(): string[];
  registeredToolHandler(name: string): CodexToolHandler | undefined;
  registeredToolValidator(name: string): CodexToolValidator | undefined;
}

function validateToolArgs(config: CodexProConfig, name: string, options: Record<string, unknown>, args: unknown): any {
  const inputSchema = options.inputSchema;
  if (!inputSchema || typeof inputSchema !== "object" || Array.isArray(inputSchema)) return args ?? {};
  const shape: Record<string, z.ZodTypeAny> = {};
  for (const [key, value] of Object.entries(inputSchema)) {
    if (value && typeof (value as { safeParse?: unknown }).safeParse === "function") {
      shape[key] = value as z.ZodTypeAny;
    }
  }
  if (!Object.keys(shape).length) return {};
  const parsed = z.object(shape).safeParse(args ?? {});
  if (parsed.success) return parsed.data;
  const details = parsed.error.issues
    .map((issue) => `${issue.path.length ? issue.path.join(".") : "arguments"}: ${issue.message}`)
    .join("; ");
  const missingWorkspace = parsed.error.issues.some((issue) => issue.path[0] === "workspace_id");
  throw new CodexProError(`Invalid arguments for ${name}: ${details}`, {
    code: "args_invalid",
    retryUnchanged: false,
    ...(missingWorkspace
      ? { recovery: { tool: isToolAvailable(config, "list_projects") ? "list_projects" : "open_workspace", message: isToolAvailable(config, "list_projects") ? "Copy workspace_id from list_projects or the workspace open result." : "Copy workspace_id from the workspace open result." } }
      : {})
  });
}

function redactAbsolutePaths(result: any, config: CodexProConfig): void {
  const structured = result.structuredContent;
  const ordered = pathRedactions(config, structured && typeof structured === "object" ? structured : {});
  if (!ordered.length) return;
  if (structured && typeof structured === "object") result.structuredContent = redactPathsDeep(structured, ordered);
  if (Array.isArray(result.content)) {
    result.content = result.content.map((item: any) =>
      item?.type === "text" && typeof item.text === "string"
        ? { ...item, text: redactPathsInText(item.text, ordered) }
        : item
    );
  }
}

function tagToolResult(result: any, name: string, options: Record<string, unknown>, config: CodexProConfig): any {
  if (!result || typeof result !== "object") return result;
  const structured = result.structuredContent;
  const base =
    structured && typeof structured === "object" && !Array.isArray(structured)
      ? structured
      : {};
  const tagged = {
    codexpro_tool: name,
    codexpro_title: options.title ?? name,
    ...base
  };
  result.structuredContent = tagged;
  if (!config.exposeAbsolutePaths) redactAbsolutePaths(result, config);
  return result;
}

function toolCallLoggingEnabled(): boolean {
  return process.env.CODEXPRO_LOG_TOOL_CALLS === "1" || process.env.CODEXPRO_LOG_REQUESTS === "1";
}

function logToolCall(name: string, status: "ok" | "error", started: number): void {
  if (!toolCallLoggingEnabled()) return;
  console.error(`[CodexProTool] ${name} ${status} ${Date.now() - started}ms`);
}

function batchOperationToolNames(rawArgs: unknown): string[] {
  if (!rawArgs || typeof rawArgs !== "object" || Array.isArray(rawArgs)) return [];
  const operations = (rawArgs as Record<string, unknown>).operations;
  if (!Array.isArray(operations)) return [];
  return operations
    .map((operation) => {
      if (!operation || typeof operation !== "object" || Array.isArray(operation)) return "";
      return String((operation as Record<string, unknown>).tool ?? "").trim();
    })
    .filter(Boolean);
}

function batchInvocationMutating(config: CodexProConfig, rawArgs: unknown): boolean {
  if (!rawArgs || typeof rawArgs !== "object" || Array.isArray(rawArgs)) return false;
  const args = rawArgs as Record<string, unknown>;
  const filePath = args.path;
  if (typeof filePath === "string" && filePath.trim()) {
    return config.writeMode === "workspace" || config.bashMode !== "off";
  }

  const tools = batchOperationToolNames(rawArgs);
  const childMutates = tools.some((tool) => BATCH_MUTATING_CHILD_TOOLS.has(tool));
  const persistenceDefault = tools.includes("bash");
  const persistenceRequested = args.persist === true || (args.persist === undefined && persistenceDefault);
  return childMutates || Boolean(args.checkpoint) || (persistenceRequested && config.writeMode === "workspace" && !config.connectionTest);
}

interface AuditInvocation {
  toolName: string;
  args: Record<string, unknown>;
  invocationSurface: "direct" | "codexpro";
  mutating: boolean;
  skip: boolean;
}

const SERVER_AUDIT_TOOLS = new Set(["server_config", "list_projects", "list_workspaces", "codexpro_inventory", "codexpro.list_actions"]);

function auditInvocationFor(config: CodexProConfig, name: string, rawArgs: any): AuditInvocation {
  const outerArgs = rawArgs && typeof rawArgs === "object" && !Array.isArray(rawArgs)
    ? rawArgs as Record<string, unknown>
    : {};
  if (name !== SUPERTOOL_NAME) {
    return {
      toolName: name,
      args: outerArgs,
      invocationSurface: "direct",
      mutating:
        name === "batch"
          ? batchInvocationMutating(config, outerArgs)
          : name === "create_project" || WORKTREE_TOOL_NAMES.has(name) || MUTATING_WORKSPACE_TOOLS.has(name),
      skip: ACTIVITY_TOOL_NAMES.has(name)
    };
  }

  const action = normalizeSupertoolAction(outerArgs.action);
  const childArgs = outerArgs.args && typeof outerArgs.args === "object" && !Array.isArray(outerArgs.args)
    ? outerArgs.args as Record<string, unknown>
    : {};
  const toolName = action === "list_actions" || action === "help" ? "codexpro.list_actions" : action;
  return {
    toolName,
    args: childArgs,
    invocationSurface: "codexpro",
    mutating:
      toolName === "batch"
        ? batchInvocationMutating(config, childArgs)
        : toolName === "create_project" || WORKTREE_TOOL_NAMES.has(toolName) || MUTATING_WORKSPACE_TOOLS.has(toolName),
    skip: ACTIVITY_TOOL_NAMES.has(toolName)
  };
}

function auditWorkspaceFor(
  access: WorkspaceAccess | undefined,
  invocation: AuditInvocation,
  rawResult?: unknown
): Workspace | undefined {
  if (!access) return undefined;
  const result = auditStructuredResult(rawResult);
  const candidates = [
    invocation.args.workspace_id,
    result.workspace_id,
    result.selected_workspace_id
  ];
  for (const candidate of candidates) {
    if (typeof candidate !== "string" || !candidate.trim()) continue;
    try {
      return access.getWorkspace(candidate);
    } catch {
      // Lifecycle operations can legitimately make a former workspace unavailable.
    }
  }
  if (access.mode !== "direct" || GLOBAL_LIFECYCLE_TOOLS.has(invocation.toolName) || SERVER_AUDIT_TOOLS.has(invocation.toolName) || candidates.some(value => value !== undefined)) return undefined;
  try {
    return access.getWorkspace();
  } catch {
    return undefined;
  }
}

function resolveAuditTarget(ctx: ToolContext, invocation: AuditInvocation, context: ToolCallContext, rawResult?: unknown): Workspace | undefined {
  const result = auditStructuredResult(rawResult);
  if (invocation.toolName.startsWith("work_")) {
    // A run id takes precedence over a supplied project filter. Failed ownership
    // checks must neither expose that run's identity nor fall back to source.
    context.auditTarget = { scope: "unattributed" };
    const ignoresInputRun = (invocation.toolName === "work_status" && (invocation.args.action ?? "list") === "list") || (invocation.toolName === "work_manage" && invocation.args.action === "create");
    const runId = ignoresInputRun ? result.run_id : invocation.args.run_id ?? result.run_id;
    if (typeof runId === "string") {
      try {
        const run = ctx.work?.coordinator.require(context.principalId, runId);
        if (run) context.auditTarget = { scope: run.workspace ? "workspace" : "project", project_id: run.project_id, workspace_id: run.workspace?.id, run_id: run.id };
      } catch { /* Unknown or inaccessible runs stay unattributed. */ }
    } else {
      const projectId = invocation.args.project_id;
      context.auditTarget = projectId === undefined ? { scope: "server" }
        : ctx.config.projects.some(project => project.id === projectId) ? { scope: "project", project_id: String(projectId) } : { scope: "unattributed" };
    }
    return undefined; // Control-plane updates do not capture source Git diffs.
  }
  const workspace = auditWorkspaceFor(ctx.workspaces, invocation, rawResult);
  if (GLOBAL_LIFECYCLE_TOOLS.has(invocation.toolName) || SERVER_AUDIT_TOOLS.has(invocation.toolName)) {
    const projectId = result.project_id ?? invocation.args.project_id;
    const identity = ctx.auditJournal().captureIdentity(workspace);
    context.auditTarget = workspace ? { scope: "workspace", project_id: identity.project_id, workspace_id: identity.workspace_id }
      : typeof projectId === "string" && ctx.config.projects.some(project => project.id === projectId) ? { scope: "project", project_id: projectId }
      : { scope: "server" };
  }
  return workspace;
}

const JOB_STATUS_EXEMPT_TOOLS = new Set(["bash", "start_jobs", "jobs", "stop_jobs", SUPERTOOL_NAME]);
const JOB_STATUS_MAX_ENTRIES = 8;

/**
 * Append a one-line background-job digest to a tool result while jobs are
 * running or have finished unacknowledged, so the agent learns about
 * completions without polling. Finished jobs are reported once.
 */
function attachJobStatus(ctx: ToolContext, name: string, args: Record<string, unknown>, result: any): any {
  if (JOB_STATUS_EXEMPT_TOOLS.has(name) || !result || typeof result !== "object") return result;
  const workspaceId = typeof args.workspace_id === "string" ? args.workspace_id : undefined;
  let summary;
  try {
    summary = ctx.jobs.statusSummary(workspaceId);
  } catch {
    return result;
  }
  const entries = [...summary.running, ...summary.finished].slice(0, JOB_STATUS_MAX_ENTRIES);
  if (!entries.length) return result;
  const now = Date.now();
  const describe = (job: JobRecord) => {
    const elapsed = elapsedLabel((job.finished_at_ms ?? now) - job.started_at_ms);
    return job.status === "running"
      ? `${job.id} (${job.command_label}) running ${elapsed}`
      : `${job.id} (${job.command_label}) ${job.status}${job.exit_code !== null ? ` exit ${job.exit_code}` : ""} after ${elapsed}`;
  };
  const line = `Background jobs: ${entries.map(describe).join("; ")}. Collect with jobs(job_ids=[...], wait_ms).`;
  const structured = result.structuredContent && typeof result.structuredContent === "object" ? result.structuredContent : {};
  result.structuredContent = {
    ...structured,
    background_jobs: entries.map((job) => ({
      job_id: job.id,
      status: job.status,
      command: job.command_label,
      elapsed_ms: (job.finished_at_ms ?? now) - job.started_at_ms,
      exit_code: job.exit_code,
      workspace_id: job.workspace_id
    }))
  };
  if (Array.isArray(result.content)) {
    const text = result.content.find((item: any) => item?.type === "text" && typeof item.text === "string");
    if (text) text.text = `${text.text}\n\n${line}`;
    else result.content.push({ type: "text", text: line });
  }
  ctx.jobs.acknowledge(entries.filter(job => job.status !== "running").map((job) => job.id));
  return result;
}

function registerToolCompat(
  ctx: ToolContext,
  name: string,
  options: Record<string, unknown>,
  handler: (args: any) => Promise<any> | any
): void {
  const { config, server } = ctx;
  const wrapped = async (args: any, extra: any) => {
    const started = Date.now();
    const journal = ctx.auditJournal();
    const invocation = auditInvocationFor(config, name, args);
    const access = ctx.workspaces;
    let context: ToolCallContext | undefined;
    let before: ActionEvidenceSnapshot | undefined;
    let after: ActionEvidenceSnapshot | undefined;
    try {
      context = contextFromRequest(config, extra);
      const invoke = async () => {
        if (journal.enabled && invocation.mutating && !invocation.skip) {
          const workspace = resolveAuditTarget(ctx, invocation, context!);
          before = invocation.toolName.startsWith("work_") ? journal.captureIdentity() : journal.capture(invocation.toolName, invocation.args, workspace);
        }
        try {
          const raw = await handler(args ?? {});
          if (["open_workspace", "open_current_workspace", "create_workspace"].includes(invocation.toolName) && raw?.structuredContent?.project_id && !raw.isError) {
            const brief = recentActivity(journal, raw.structuredContent.project_id, raw.structuredContent.workspace_id);
            raw.structuredContent.recent_activity = brief;
            const text = raw.content?.find((item: any) => item.type === "text");
            if (text) text.text += `\n\nLast recorded project change: ${brief.last_recorded_project_change?.at ?? "unavailable in retained activity"}. ${brief.coverage}`;
          }
          if (journal.enabled && !invocation.skip) {
            const workspace = resolveAuditTarget(ctx, invocation, context!, raw);
            after = invocation.mutating && !invocation.toolName.startsWith("work_")
              ? journal.capture(invocation.toolName, invocation.args, workspace, raw)
              : journal.captureIdentity(workspace);
          }
          return raw;
        } catch (error) {
          if (journal.enabled && !invocation.skip) {
            const workspace = resolveAuditTarget(ctx, invocation, context!);
            after = invocation.mutating && !invocation.toolName.startsWith("work_")
              ? journal.capture(invocation.toolName, invocation.args, workspace)
              : journal.captureIdentity(workspace);
          }
          throw error;
        }
      };
      const raw = await runWithToolContext(context, () => {
        if (!access || GLOBAL_LIFECYCLE_TOOLS.has(invocation.toolName)) return invoke();
        const workspaceId = typeof invocation.args.workspace_id === "string"
          ? invocation.args.workspace_id
          : undefined;
        return access.execute(workspaceId, invocation.mutating, invoke);
      });
      const finished = Date.now();
      const recorded = invocation.skip
        ? undefined
        : journal.record({
          toolName: invocation.toolName,
          invocationSurface: invocation.invocationSurface,
          args: invocation.args,
          result: raw,
          startedAtMs: started,
          finishedAtMs: finished,
          mutating: invocation.mutating,
          context,
          before,
          after
        });
      const result = attachJobStatus(ctx, name, invocation.args, tagToolResult(raw, name, options, config));
      logToolCall(name, recorded && recorded.status !== "succeeded" ? "error" : raw?.isError ? "error" : "ok", started);
      return result;
    } catch (error) {
      const finished = Date.now();
      if (!invocation.skip) {
        if (journal.enabled && !after) {
          after = journal.captureIdentity(auditWorkspaceFor(access, invocation));
        }
        journal.record({
          toolName: invocation.toolName,
          invocationSurface: invocation.invocationSurface,
          args: invocation.args,
          error,
          startedAtMs: started,
          finishedAtMs: finished,
          mutating: invocation.mutating,
          context,
          before,
          after
        });
      }
      const result = tagToolResult(errorResult(error), name, options, config);
      logToolCall(name, "error", started);
      return result;
    }
  };

  const securitySchemes = [{ type: "noauth" }];
  const fullOptions: Record<string, unknown> = {
    securitySchemes,
    ...options,
    _meta: {
      securitySchemes,
      ...(options._meta as Record<string, unknown> | undefined)
    }
  };

  const s = server as any;
  if (typeof s.registerTool === "function") {
    s.registerTool(name, fullOptions, wrapped);
    return;
  }

  if (typeof s.tool === "function") {
    s.tool(name, (fullOptions.description as string | undefined) ?? name, fullOptions.inputSchema ?? {}, wrapped);
    return;
  }

  throw new Error("Unsupported MCP SDK: McpServer has neither registerTool nor tool.");
}

export function createToolContext(
  config: CodexProConfig,
  server: McpServer,
  workspaces: WorkspaceAccess,
  editSnapshots: EditSnapshotStore
): ToolContext {
  const names: string[] = [];
  const handlers = new Map<string, CodexToolHandler>();
  const validators = new Map<string, CodexToolValidator>();
  let journal: AuditJournal | undefined;
  const ctx: ToolContext = {
    config,
    server,
    workspaces,
    guard: new PathGuard(config),
    jobs: getJobManager(config),
    editSnapshots,
    reviewCheckpoints: new Map<string, string>(),
    auditJournal() {
      if (!journal) journal = new AuditJournal(config);
      return journal;
    },
    register(name, options, handler) {
      if (!isToolAvailable(config, name)) return;
      if (ctx.work && !name.startsWith("work_")) options = { ...options, inputSchema: { ...(options.inputSchema as object), execution: z.object({ attempt_token: z.string().min(1).max(160), operation_key: z.string().min(1).max(160).optional() }).optional().describe("For managed run workspaces: current claim token; mutations also require a stable operation key.") } };
      // hiddenInputSchema: accepted and validated, but not advertised in tools/list
      // (compatibility parameters that newer guidance steers away from).
      const { hiddenInputSchema, ...advertised } = options as Record<string, unknown> & { hiddenInputSchema?: Record<string, unknown> };
      const validationOptions = hiddenInputSchema
        ? { ...advertised, inputSchema: { ...((advertised.inputSchema as Record<string, unknown> | undefined) ?? {}), ...hiddenInputSchema } }
        : advertised;
      const validator: CodexToolValidator = (args) => validateToolArgs(config, name, validationOptions, args);
      const validatedHandler: CodexToolHandler = (args) => { const parsed = validator(args); return ctx.work ? ctx.work.invoke(name, parsed, handler) : handler(parsed); };
      // The SDK parses arguments against the advertised schema (stripping unknown
      // keys) before our wrapper runs, so hidden parameters need a passthrough
      // object there; our validator above still enforces the full schema.
      const sdkOptions = hiddenInputSchema && advertised.inputSchema && typeof advertised.inputSchema === "object"
        ? { ...advertised, inputSchema: z.object(advertised.inputSchema as z.ZodRawShape).passthrough() }
        : advertised;
      registerToolCompat(ctx, name, sdkOptions, validatedHandler);
      if (!names.includes(name)) names.push(name);
      handlers.set(name, validatedHandler);
      validators.set(name, validator);
    },
    registeredToolNames() {
      return [...names];
    },
    registeredToolHandler(name) {
      return handlers.get(name);
    },
    registeredToolValidator(name) {
      return validators.get(name);
    }
  };
  return ctx;
}
