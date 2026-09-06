import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { z } from "zod";
import type { CodexProConfig } from "../config.js";
import { PathGuard, CodexProError, type Workspace } from "../guard.js";
import type { EditSnapshotStore } from "../fsOps.js";
import { AuditJournal, type ActionEvidenceSnapshot } from "../audit.js";
import { contextFromRequest, runWithToolContext, type ToolCallContext } from "../toolContext.js";
import type { WorkspaceAccess } from "../workspaceAccess.js";
import { pathRedactions, redactPathsDeep, redactPathsInText } from "../pathLabels.js";
import { auditStructuredResult, compactStructuredContent, errorResult, usesToolCard } from "./shared.js";
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
  /** Lazily created metadata audit journal for this server. */
  auditJournal(): AuditJournal;
  /** Register a tool if the registry exposes it for this config; otherwise a no-op. */
  register(name: string, options: Record<string, unknown>, handler: CodexToolHandler): void;
  /** Names registered so far, in registration order. */
  registeredToolNames(): string[];
  registeredToolHandler(name: string): CodexToolHandler | undefined;
  registeredToolValidator(name: string): CodexToolValidator | undefined;
}

function validateToolArgs(name: string, options: Record<string, unknown>, args: unknown): any {
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
      ? { recovery: { tool: "list_projects", message: "workspace_id comes from list_projects (one per project) or from open_workspace(project_id)." } }
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
  const meta = (options._meta as Record<string, unknown> | undefined) ?? {};
  result.structuredContent = meta.ui || meta["openai/outputTemplate"] ? compactStructuredContent(tagged) : tagged;
  if (!config.exposeAbsolutePaths) redactAbsolutePaths(result, config);
  return result;
}

const OPTIONAL_TOOL_CARD_META = [
  "ui",
  "openai/outputTemplate",
  "openai/toolInvocation/invoking",
  "openai/toolInvocation/invoked"
] as const;

function descriptorOptionsForConfig(config: CodexProConfig, name: string, options: Record<string, unknown>): Record<string, unknown> {
  if (usesToolCard(config, name)) return options;
  const meta = { ...((options._meta as Record<string, unknown> | undefined) ?? {}) };
  for (const key of OPTIONAL_TOOL_CARD_META) delete meta[key];
  return { ...options, _meta: meta };
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
  return childMutates || (persistenceRequested && config.writeMode === "workspace" && !config.connectionTest);
}

interface AuditInvocation {
  toolName: string;
  args: Record<string, unknown>;
  invocationSurface: "direct" | "codexpro";
  mutating: boolean;
  skip: boolean;
}

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
  if (access.mode !== "direct") return undefined;
  try {
    return access.getWorkspace();
  } catch {
    return undefined;
  }
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
          before = journal.capture(invocation.toolName, invocation.args, auditWorkspaceFor(access, invocation));
        }
        try {
          const raw = await handler(args ?? {});
          if (journal.enabled && !invocation.skip) {
            const workspace = auditWorkspaceFor(access, invocation, raw);
            after = invocation.mutating
              ? journal.capture(invocation.toolName, invocation.args, workspace, raw)
              : journal.captureIdentity(workspace);
          }
          return raw;
        } catch (error) {
          if (journal.enabled && !invocation.skip) {
            const workspace = auditWorkspaceFor(access, invocation);
            after = invocation.mutating
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
      const result = tagToolResult(raw, name, options, config);
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
    editSnapshots,
    reviewCheckpoints: new Map<string, string>(),
    auditJournal() {
      if (!journal) journal = new AuditJournal(config);
      return journal;
    },
    register(name, options, handler) {
      if (!isToolAvailable(config, name)) return;
      const validator: CodexToolValidator = (args) => validateToolArgs(name, options, args);
      const validatedHandler: CodexToolHandler = (args) => handler(validator(args));
      registerToolCompat(ctx, name, descriptorOptionsForConfig(config, name, options), validatedHandler);
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
