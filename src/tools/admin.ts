import { createHash } from "node:crypto";
import { z } from "zod";
import { CodexProError } from "../guard.js";
import { readTextFile, writeTextFile, editTextFileByLines } from "../fsOps.js";
import { runBash } from "../bashOps.js";
import { gitStatus } from "../gitOps.js";
import { buildProContext } from "../proContext.js";
import { codexproInventory, loadSkill } from "../capabilitiesOps.js";
import { TOOL_CARD_URI } from "../toolCardWidget.js";
import { redactSensitiveText, redactStructured } from "../redact.js";
import { ACTION_NAMESPACE, ACTION_OPERATION_CLASSES, ACTION_SCHEMA_VERSION, ACTION_STATUSES } from "../audit.js";
import type { ToolContext } from "./context.js";
import { SUPERTOOL_ACTION_ALIASES, SUPERTOOL_NAME, canCreateProjects, normalizeSupertoolAction, toolNamesForMode } from "./registry.js";
import {
  BASH_ANNOTATIONS,
  HANDOFF_WRITE_ANNOTATIONS,
  READ_ONLY_ANNOTATIONS,
  assertWriteToolAllowed,
  changedStatusLines,
  cleanOneLine,
  errorResult,
  errorText,
  limitInt,
  looksLikeGitError,
  parseBool,
  textResult,
  toolMeta,
  workspaceIdSchema
} from "./shared.js";

export function registerSupertool(ctx: ToolContext): void {
  const { config } = ctx;

  ctx.register(

    SUPERTOOL_NAME,
    {
      title: "CodexPro Supertool",
      description:
        "Call any registered CodexPro tool through one wrapper: action is the tool name, args are that tool's arguments. Only useful for clients that need a single stable tool schema; call the tools directly otherwise. action=list_actions lists what this mode exposes.",
      inputSchema: {
        action: z.string().optional().describe("Action or registered tool name. Use list_actions to see what this server mode allows."),
        args: z.record(z.any()).optional().describe("Arguments for the selected action. Same shape as the wrapped CodexPro tool.")
      },
      annotations: BASH_ANNOTATIONS,
      _meta: toolMeta(SUPERTOOL_NAME)
    },
    async (args) => {
      const action = normalizeSupertoolAction(args.action);
      const names = ctx.registeredToolNames().filter((name) => name !== SUPERTOOL_NAME);
      if (action === "list_actions" || action === "help") {
        const text = [
          "# CodexPro Supertool",
          "",
          "Use `codexpro` only when a stable wrapper is useful for ChatGPT connector caching or custom workflows. The explicit tools remain the preferred default because they give clearer descriptions and validation.",
          "",
          "## Available actions",
          "",
          names.length ? names.map((name) => `- ${name}`).join("\n") : "- none",
          "",
          "## Usage",
          "",
          "```json",
          JSON.stringify({ action: "search", args: { workspace_id: "ws_...", query: "needle", path: "src" } }, null, 2),
          "```"
        ].join("\n");
        return textResult(text, {
          actions: names,
          action_count: names.length,
          aliases: SUPERTOOL_ACTION_ALIASES,
          tool_mode: config.toolMode,
          bash_mode: config.bashMode,
          write_mode: config.writeMode
        });
      }

      if (action === SUPERTOOL_NAME) {
        throw new CodexProError("codexpro cannot call itself. Use action=list_actions to inspect available wrapped actions.");
      }

      const handler = ctx.registeredToolHandler(action);
      if (!handler) {
        throw new CodexProError(
          `CodexPro action is not available in the current mode: ${action}. ` +
            "Call codexpro with action=list_actions, or restart CodexPro with a broader tool mode if that action should be exposed."
        );
      }

      const childArgs =
        args.args && typeof args.args === "object" && !Array.isArray(args.args)
          ? args.args
          : {};
      let result: any;
      try {
        result = await handler(childArgs);
      } catch (error) {
        result = errorResult(error);
      }
      if (result && typeof result === "object") {
        const structured = result.structuredContent;
        result.structuredContent = {
          codexpro_tool: action,
          codexpro_title: action,
          codexpro_super_action: action,
          wrapped_tool: action,
          ...(structured && typeof structured === "object" && !Array.isArray(structured) ? structured : {})
        };
      }
      return result;
    }
  );
}

export function registerAdminTools(ctx: ToolContext): void {
  const { config, workspaces, guard, editSnapshots } = ctx;

  ctx.register(

    "server_config",
    {
      title: "Server Config",
      description: "Show CodexPro server configuration, safety modes, limits, and blocked paths. Does not reveal auth tokens.",
      inputSchema: {},
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("server_config")
    },
    async () => {
      const safeConfig = {
        defaultRoot: config.defaultRoot,
        allowedRoots: config.allowedRoots,
        projectsFile: config.projectsFile ?? null,
        projectCreationEnabled: canCreateProjects(config),
        projectCreationRoots: config.projectCreationRoots,
        defaultProjectId: config.defaultProjectId,
        projects: workspaces.listProjects(),
        host: config.host,
        port: config.port,
        widgetDomain: config.widgetDomain,
        authMode: config.authMode,
        authEnabled: Boolean(config.authToken || config.cloudflareAccess),
        cloudflareAccess: config.cloudflareAccess
          ? { teamDomain: config.cloudflareAccess.teamDomain, audience: config.cloudflareAccess.audience, jwksUri: config.cloudflareAccess.jwksUri }
          : null,
        bashMode: config.bashMode,
        bashTranscript: config.bashTranscript,
        bashSessionId: config.bashSessionId ?? null,
        requireBashSession: config.requireBashSession,
        codexSessions: config.codexSessions,
        codexDir: config.codexDir,
        writeMode: config.writeMode,
        handoffMode: config.handoffMode,
        toolMode: config.toolMode,
        exposeAbsolutePaths: config.exposeAbsolutePaths,
        toolCards: config.toolCards,
        auditMode: config.auditMode,
        debugActivityMode: config.auditMode,
        auditEnabled: ctx.auditJournal().enabled,
        auditSchemaVersion: ACTION_SCHEMA_VERSION,
        auditLogConfigured: Boolean(config.auditLogPath),
        auditMaxBytes: config.auditMaxBytes,
        auditRetainActions: config.auditRetainActions,
        connectionTest: config.connectionTest,
        analysisEnabled: config.analysisEnabled,
        analysisLimits: config.analysisLimits,
        inheritEnv: config.inheritEnv,
        contextDir: config.contextDir,
        worktreeMode: config.worktreeMode,
        worktreeRoot: config.worktreeMode === "mcp" ? config.worktreeRoot : null,
        worktreeBaseRef: config.worktreeBaseRef,
        maxWorktrees: config.maxWorktrees,
        maxReadBytes: config.maxReadBytes,
        maxWriteBytes: config.maxWriteBytes,
        maxImportBytes: config.maxImportBytes,
        maxOutputBytes: config.maxOutputBytes,
        maxSearchResults: config.maxSearchResults,
        blockedGlobs: config.blockedGlobs,
        registeredTools: ctx.registeredToolNames(),
        registeredToolCount: ctx.registeredToolNames().length
      };
      return textResult(`# CodexPro Server Config\n\n${JSON.stringify(safeConfig, null, 2)}`, safeConfig);
    }
  );


  ctx.register(

    "activity_list",
    {
      title: "List CodexPro Debug Activity",
      description:
        "List bounded debug-namespace codexpro.action.v1 engineering records. Omit after_sequence to tail the most recent actions, or pass the last acknowledged sequence to consume forward without scanning ChatGPT history or Git logs. These are diagnostics, not day-to-day operations. Public action records omit payload bodies, command/query text, tokens, and raw output; exact Bash scripts are retained only for the authenticated activity dashboard and are stripped from activity_list, activity_get, and activity_export.",
      inputSchema: {
        after_sequence: z.number().int().min(0).optional().describe("Read actions after this durable source sequence. Omit to tail the latest actions."),
        limit: z.number().int().min(1).max(500).optional().describe("Maximum matching actions. Default: 100."),
        mutating_only: z.boolean().optional().describe("Return only project/workspace/file/command mutations."),
        tool_name: z.string().max(120).optional().describe("Exact effective CodexPro tool-name filter."),
        operation_class: z.enum(ACTION_OPERATION_CLASSES).optional().describe("Operation-class filter."),
        status: z.enum(ACTION_STATUSES).optional().describe("Outcome filter."),
        project_id: z.string().max(160).optional().describe("Exact project-id filter."),
        workspace_id: z.string().max(160).optional().describe("Exact workspace-id filter.")
      },
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("activity_list")
    },
    async (args) => {
      const activity = ctx.auditJournal().list({
        afterSequence: args.after_sequence,
        limit: args.limit,
        mutatingOnly: args.mutating_only,
        toolName: args.tool_name,
        operationClass: args.operation_class,
        status: args.status,
        projectId: args.project_id,
        workspaceId: args.workspace_id
      });
      const summary = activity.enabled
        ? `${activity.actions.length} action(s); next_sequence=${activity.next_sequence}; earliest_sequence=${activity.earliest_sequence}; latest_sequence=${activity.latest_sequence}; has_more=${activity.has_more}.`
        : "CodexPro metadata auditing is disabled. Set CODEXPRO_AUDIT_MODE=metadata or start with --audit metadata.";
      return textResult(`# CodexPro Debug Activity\n\n${summary}`, { ...activity });
    }
  );


  ctx.register(

    "activity_get",
    {
      title: "Get CodexPro Debug Action",
      description: "Get one debug-namespace codexpro.action.v1 engineering record by its stable source-owned action id.",
      inputSchema: {
        action_id: z.string().regex(/^cpa_[a-f0-9]{32}$/).describe("Stable CodexPro action id returned by activity_list.")
      },
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("activity_get")
    },
    async (args) => {
      const journal = ctx.auditJournal();
      if (!journal.enabled) {
        return textResult(
          "# CodexPro Debug Action\n\nCodexPro debug activity is disabled. Set CODEXPRO_AUDIT_MODE=metadata or start with --audit metadata.",
          { enabled: false, mode: config.auditMode, namespace: ACTION_NAMESPACE, schema_version: ACTION_SCHEMA_VERSION, action: null }
        );
      }
      const action = journal.get(args.action_id);
      if (!action) throw new CodexProError(`Unknown CodexPro action_id: ${args.action_id}.`);
      return textResult(`# CodexPro Debug Action\n\n${action.sequence} · ${action.tool_name} · ${action.status}`, {
        enabled: true,
        mode: config.auditMode,
        namespace: ACTION_NAMESPACE,
        schema_version: ACTION_SCHEMA_VERSION,
        action
      });
    }
  );


  ctx.register(

    "activity_status",
    {
      title: "CodexPro Debug Activity Status",
      description: "Read the debug action-journal cursor/status boundary, including retained and latest sequences, malformed records, and explicit gap detection.",
      inputSchema: {},
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("activity_status")
    },
    async () => {
      const status = ctx.auditJournal().status();
      const summary = status.enabled
        ? `retained_from_sequence=${status.retained_from_sequence}; latest_sequence=${status.latest_sequence}; next_sequence=${status.next_sequence}; gap_detected=${status.gap_detected}.`
        : "CodexPro metadata auditing is disabled. Set CODEXPRO_AUDIT_MODE=metadata or start with --audit metadata.";
      return textResult(`# CodexPro Debug Activity Status\n\n${summary}`, { ...status });
    }
  );


  ctx.register(

    "activity_export",
    {
      title: "Export CodexPro Debug Activity",
      description:
        "Export a bounded page of debug-namespace codexpro.action.v1 engineering records as JSONL or JSON for a diagnostic consumer such as Ops Inbox. The export uses the same durable sequence cursor and metadata-only redaction boundary as activity_list, and never exposes the journal filesystem path.",
      inputSchema: {
        after_sequence: z.number().int().min(0).describe("Last acknowledged source sequence. The export starts after this sequence."),
        limit: z.number().int().min(1).max(500).optional().describe("Maximum matching actions before the byte budget is applied. Default: 100."),
        format: z.enum(["jsonl", "json"]).optional().describe("Export encoding. Default: jsonl."),
        mutating_only: z.boolean().optional().describe("Export only project/workspace/file/command mutations."),
        tool_name: z.string().max(120).optional().describe("Exact effective CodexPro tool-name filter."),
        operation_class: z.enum(ACTION_OPERATION_CLASSES).optional().describe("Operation-class filter."),
        status: z.enum(ACTION_STATUSES).optional().describe("Outcome filter."),
        project_id: z.string().max(160).optional().describe("Exact project-id filter."),
        workspace_id: z.string().max(160).optional().describe("Exact workspace-id filter.")
      },
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("activity_export")
    },
    async (args) => {
      const journal = ctx.auditJournal();
      if (!journal.enabled) {
        return textResult(
          "CodexPro metadata auditing is disabled. Set CODEXPRO_AUDIT_MODE=metadata or start with --audit metadata.",
          {
            enabled: false,
            mode: config.auditMode,
            namespace: ACTION_NAMESPACE,
            schema_version: ACTION_SCHEMA_VERSION,
            export_format: args.format ?? "jsonl",
            export_bytes: 0,
            action_count: 0
          }
        );
      }

      const activity = journal.list({
        afterSequence: args.after_sequence,
        limit: args.limit,
        mutatingOnly: args.mutating_only,
        toolName: args.tool_name,
        operationClass: args.operation_class,
        status: args.status,
        projectId: args.project_id,
        workspaceId: args.workspace_id
      });
      const format = args.format ?? "jsonl";
      const exportBudget = Math.max(1_024, Math.min(100_000, Math.floor(config.maxOutputBytes * 0.75)));
      let included: typeof activity.actions = [];
      let exportText = format === "json" ? "[]\n" : "";

      for (const action of activity.actions) {
        const candidate = [...included, action];
        const candidateText = format === "json"
          ? `${JSON.stringify(candidate)}\n`
          : `${candidate.map((item) => JSON.stringify(item)).join("\n")}\n`;
        if (Buffer.byteLength(candidateText, "utf8") > exportBudget) {
          if (!included.length) {
            throw new CodexProError(
              `The first matching action exceeds the ${exportBudget}-byte activity export budget. Narrow the filters or raise CODEXPRO_MAX_OUTPUT_BYTES.`
            );
          }
          break;
        }
        included = candidate;
        exportText = candidateText;
      }

      const safeExportText = redactSensitiveText(exportText);
      const truncatedByBytes = included.length < activity.actions.length;
      const nextSequence = included.length
        ? included[included.length - 1].sequence
        : activity.next_sequence;
      const metadata = {
        enabled: true,
        mode: config.auditMode,
        namespace: ACTION_NAMESPACE,
        schema_version: ACTION_SCHEMA_VERSION,
        export_format: format,
        export_bytes: Buffer.byteLength(safeExportText, "utf8"),
        export_sha256: createHash("sha256").update(safeExportText).digest("hex"),
        action_count: included.length,
        next_sequence: nextSequence,
        earliest_sequence: activity.earliest_sequence,
        latest_sequence: activity.latest_sequence,
        has_more: truncatedByBytes || activity.has_more,
        truncated_by_bytes: truncatedByBytes,
        malformed_records: activity.malformed_records,
        gap_detected: activity.gap_detected
      };
      return {
        content: [{ type: "text", text: safeExportText }],
        structuredContent: redactStructured(metadata)
      };
    }
  );


  ctx.register(

    "codexpro_self_test",
    {
      title: "CodexPro Self Test",
      description:
        "Diagnostic only: checks modes, the registered tool set, workspace access, skills and git without touching the repository. Optional probes (off by default) write one .ai-bridge file, run safe bash commands, or build a Pro context bundle in memory. Not part of normal coding work.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        write_probe: z.boolean().optional().describe("Create/edit only .ai-bridge/codexpro-self-test.md. Default: false."),
        bash_probe: z.boolean().optional().describe("Check bash policy with safe local commands only. Default: false."),
        pro_context_probe: z.boolean().optional().describe("Build a selected-only Pro context bundle in memory without writing pro-context.md. Default: false."),
        include_global_skills: z.boolean().optional().describe("Include user/plugin skill discovery in the inventory check. Default: true."),
        max_skills: z.number().int().min(1).max(120).optional().describe("Maximum skills to inspect during the inventory check. Default: 40.")
      },
      annotations: HANDOFF_WRITE_ANNOTATIONS,
      _meta: toolMeta("codexpro_self_test")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const started = Date.now();
      const checks: Array<{ name: string; status: "pass" | "warn" | "fail"; detail: string }> = [];
      const filesTouched: string[] = [];
      const probePath = `${config.contextDir}/codexpro-self-test.md`;

      const check = (name: string, status: "pass" | "warn" | "fail", detail: string) => {
        checks.push({ name, status, detail: cleanOneLine(detail, detail, 260) });
      };

      check("workspace", "pass", workspace.root);
      check("tool mode", config.toolMode === "full" ? "pass" : "warn", `${config.toolMode}; expected tools: ${toolNamesForMode(config).length}`);
      check("write mode", config.writeMode === "off" ? "warn" : "pass", config.writeMode);
      check("bash mode", config.bashMode === "full" ? "warn" : "pass", config.bashMode);
      check(
        "http auth",
        "pass",
        config.authToken
          ? "token configured"
          : config.requireHttpToken
            ? "token required when serving HTTP"
            : "token auth explicitly disabled"
      );
      const expectedTools = toolNamesForMode(config).sort();
      const actualTools = ctx.registeredToolNames().sort();
      const missingTools = expectedTools.filter((name) => !actualTools.includes(name));
      const extraTools = actualTools.filter((name) => !expectedTools.includes(name));
      check(
        "registered tool set",
        missingTools.length || extraTools.length ? "fail" : "pass",
        missingTools.length || extraTools.length
          ? `missing: ${missingTools.join(", ") || "none"}; extra: ${extraTools.join(", ") || "none"}`
          : `${actualTools.length} tools registered for ${config.toolMode} mode`
      );

      try {
        const inventory = await codexproInventory(config, workspace, {
          includeGlobalSkills: parseBool(args.include_global_skills, true),
          includeMcpServers: true,
          maxSkills: limitInt(args.max_skills, 40, 1, 120)
        });
        check("inventory", "pass", `${inventory.skills.length} skills inspected, ${inventory.mcpServers.length} MCP server names visible`);
      } catch (error) {
        check("inventory", "fail", errorText(error));
      }

      try {
        const status = gitStatus(config, workspace);
        const gitFailed = looksLikeGitError(status);
        const changed = gitFailed ? 0 : changedStatusLines(status).length;
        check("git status", gitFailed ? "warn" : "pass", gitFailed ? status : `${changed} changed entries`);
      } catch (error) {
        check("git status", "fail", errorText(error));
      }

      if (parseBool(args.write_probe, false)) {
        if (config.writeMode === "off") {
          check("write/edit probe", "warn", "skipped because CODEXPRO_WRITE_MODE=off");
        } else {
          try {
            assertWriteToolAllowed(config, probePath);
            const content = [
              "# CodexPro Self Test",
              "",
              `Updated: ${new Date().toISOString()}`,
              `Workspace: ${workspace.root}`,
              "marker: before",
              ""
            ].join("\n");
            await writeTextFile(config, guard, workspace, probePath, content, { createDirs: true, overwrite: true });
            const editRead = await readTextFile(config, guard, workspace, probePath, {
              maxBytes: 20_000,
              editSnapshots
            });
            await editTextFileByLines(
              config,
              guard,
              workspace,
              probePath,
              [{ op: "replace", startLine: 5, content: "marker: after" }],
              editSnapshots,
              editRead.editTag
            );
            const readBack = await readTextFile(config, guard, workspace, probePath, { maxBytes: 20_000 });
            if (!readBack.text.includes("marker: after")) throw new CodexProError("self-test edit marker was not found after edit.");
            const scopedStatus = gitStatus(config, workspace, guard, probePath);
            const scopedFiles = changedStatusLines(scopedStatus);
            filesTouched.push(probePath);
            check(
              "write/edit probe",
              scopedFiles.length && scopedFiles.every((line) => line.includes(probePath)) ? "pass" : "warn",
              scopedFiles.length ? `path-scoped status: ${scopedFiles.join(", ")}` : "path-scoped status clean after write/edit"
            );
          } catch (error) {
            check("write/edit probe", "fail", errorText(error));
          }
        }
      } else {
        check("write/edit probe", "warn", "skipped by request");
      }

      if (parseBool(args.pro_context_probe, false)) {
        try {
          if (!filesTouched.includes(probePath)) {
            check("selected-only pro context", "warn", "skipped because write probe did not create the selected file");
          } else {
            const context = await buildProContext(config, guard, workspace, {
              title: "CodexPro Self Test Context",
              selectedPaths: [probePath],
              includeImportantFiles: false,
              includeChangedFiles: false,
              includeDiff: false,
              includeAiBridge: false,
              maxFiles: 4,
              maxTotalBytes: 80_000
            });
            const exactOnly = context.filesIncluded.length === 1 && context.filesIncluded[0] === probePath;
            check(
              "selected-only pro context",
              exactOnly ? "pass" : "fail",
              exactOnly ? `included only ${probePath}` : `included ${context.filesIncluded.join(", ") || "no files"}`
            );
          }
        } catch (error) {
          check("selected-only pro context", "fail", errorText(error));
        }
      } else {
        check("selected-only pro context", "warn", "skipped by request");
      }

      if (parseBool(args.bash_probe, false)) {
        try {
          if (config.bashMode === "off") {
            check("bash policy", "warn", "bash disabled");
          } else {
            const bashProbeOptions = { timeoutMs: 10_000, sessionId: config.bashSessionId };
            const pwd = await runBash(config, guard, workspace, "pwd", bashProbeOptions);
            if (config.bashMode === "safe") {
              try {
                await runBash(config, guard, workspace, "ls $HOME", bashProbeOptions);
                check("bash policy", "fail", "safe bash allowed environment expansion unexpectedly");
              } catch {
                check("bash policy", pwd.exitCode === 0 ? "pass" : "warn", "safe bash allowed pwd and blocked environment expansion");
              }
            } else {
              check("bash policy", pwd.exitCode === 0 ? "warn" : "fail", "full bash is enabled; use only for trusted local repos");
            }
          }
        } catch (error) {
          check("bash policy", "fail", errorText(error));
        }
      } else {
        check("bash policy", "warn", "skipped by request");
      }

      check(
        "terms boundary",
        "pass",
        "local workspace bridge only; does not provide models, proxy model access, bypass quotas, or execute remote/local agents from MCP"
      );

      const failed = checks.filter((item) => item.status === "fail").length;
      const warned = checks.filter((item) => item.status === "warn").length;
      const passed = checks.filter((item) => item.status === "pass").length;
      const status = failed ? "fail" : warned ? "warn" : "pass";
      const text = [
        "# CodexPro Self Test",
        "",
        `Status: ${status}`,
        `Workspace: ${workspace.root}`,
        `Mode: tools=${config.toolMode}, write=${config.writeMode}, handoff=${config.handoffMode}, debug_activity=${config.auditMode}, bash=${config.bashMode}${config.bashSessionId ? `, bash_session=${config.bashSessionId}${config.requireBashSession ? " required" : ""}` : ""}`,
        `Expected tools: ${expectedTools.length}`,
        `Registered tools: ${actualTools.length}`,
        `Duration: ${Date.now() - started} ms`,
        "",
        "## Checks",
        "",
        ...checks.map((item) => `- ${item.status.toUpperCase()} ${item.name}: ${item.detail}`),
        "",
        "## Terms Boundary",
        "",
        "CodexPro exposes local repo tools to the ChatGPT session the user controls. It does not provide models, proxy model access, resell access, modify quotas, bypass limits, or run local implementation agents through remote MCP tools."
      ].join("\n");

      return textResult(text, {
        workspace_id: workspace.id,
        root: workspace.root,
        status,
        passed,
        warned,
        failed,
        duration_ms: Date.now() - started,
        expected_tools: expectedTools,
        expected_tool_count: expectedTools.length,
        registered_tools: actualTools,
        registered_tool_count: actualTools.length,
        bash_mode: config.bashMode,
        bash_session_id: config.bashSessionId ?? null,
        require_bash_session: config.requireBashSession,
        write_mode: config.writeMode,
        handoff_mode: config.handoffMode,
        debug_activity_mode: config.auditMode,
        tool_mode: config.toolMode,
        files_touched: filesTouched,
        checks,
        terms_boundary: {
          local_workspace_bridge: true,
          provides_models: false,
          proxies_model_access: false,
          bypasses_quotas: false,
          remote_agent_execution: false
        }
      });
    }
  );


  ctx.register(

    "codexpro_inventory",
    {
      title: "CodexPro Inventory",
      description:
        "List CodexPro modes plus discovered skill names and configured MCP server names. Use this early when planning needs local agent capabilities.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        include_global_skills: z.boolean().optional().describe("Include user and plugin skill folders. Default: true."),
        include_mcp_servers: z.boolean().optional().describe("Include configured MCP server names from safe config files. Default: true."),
        max_skills: z.number().int().min(1).max(500).optional().describe("Maximum skills to list. Default: 120.")
      },
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("codexpro_inventory")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const inventory = await codexproInventory(config, workspace, {
        includeGlobalSkills: parseBool(args.include_global_skills, true),
        includeMcpServers: parseBool(args.include_mcp_servers, true),
        maxSkills: limitInt(args.max_skills, 120, 1, 500)
      });
      return textResult(inventory.text, {
        workspace_id: workspace.id,
        root: workspace.root,
        bash_mode: config.bashMode,
        write_mode: config.writeMode,
        tool_mode: config.toolMode,
        skills: inventory.skills,
        skill_count: inventory.skills.length,
        mcp_servers: inventory.mcpServers,
        mcp_server_count: inventory.mcpServers.length,
        widget_uri: TOOL_CARD_URI
      });
    }
  );


  ctx.register(

    "load_skill",
    {
      title: "Load Skill",
      description:
        "Load the bounded SKILL.md body for a discovered workspace, user, or plugin skill by name. Does not accept arbitrary paths; use after open_current_workspace/open_workspace shows skill_inventory.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        name: z.string().describe("Exact skill name from skill_inventory or codexpro_inventory."),
        source: z.enum(["workspace", "user", "plugin", "other"]).optional().describe("Optional source override. Without it, the highest-precedence skill is loaded."),
        path: z.string().optional().describe("Optional exact sanitized path override for diagnostics or an explicitly selected suppressed duplicate."),
        include_global_skills: z.boolean().optional().describe("Also scan installed user/plugin skills. Default: auto when source/path is not workspace."),
        max_skills: z.number().int().min(1).max(500).optional().describe("Maximum skills to scan while resolving the requested skill. Default: 500."),
        max_bytes: z.number().int().min(1000).max(100000).optional().describe("Maximum bytes to return from SKILL.md. Default: 40000.")
      },
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("load_skill")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const requestedPath = typeof args.path === "string" ? args.path : undefined;
      const includeGlobalDefault =
        args.source === undefined ||
        (args.source !== undefined && args.source !== "workspace") ||
        Boolean(requestedPath && !requestedPath.startsWith("$WORKSPACE/"));
      const loaded = await loadSkill(workspace, {
        name: String(args.name ?? ""),
        source: args.source,
        path: requestedPath,
        includeGlobal: parseBool(args.include_global_skills, includeGlobalDefault),
        maxSkills: limitInt(args.max_skills, 500, 1, 500),
        maxBytes: limitInt(args.max_bytes, 40_000, 1_000, 100_000)
      });
      const truncated = loaded.truncated ? "\n\n[truncated: increase max_bytes if more context is required]" : "";
      const text = `# Load Skill\n\nName: ${loaded.skill.name}\nSource: ${loaded.skill.source}\nPath: ${loaded.skill.path}\nBytes: ${loaded.bytes}/${loaded.totalBytes}\n\n\`\`\`markdown\n${loaded.text}${truncated}\n\`\`\``;
      return textResult(text, {
        workspace_id: workspace.id,
        root: workspace.root,
        skill: loaded.skill,
        bytes: loaded.bytes,
        total_bytes: loaded.totalBytes,
        truncated: loaded.truncated,
        text: loaded.text
      });
    }
  );
}
