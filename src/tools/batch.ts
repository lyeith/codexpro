import fsp from "node:fs/promises";
import path from "node:path";
import { z } from "zod";
import { CodexProError } from "../guard.js";
import { currentToolContext } from "../toolContext.js";
import { digest } from "../work/coordinator.js";
import { assertVerificationCommand } from "../bashOps.js";
import { attachActionDashboardMetadata } from "../audit.js";
import {
  BATCH_DEFINITION_VERSION,
  BATCH_STORE_LIMIT,
  loadBatchDefinition,
  maintainLoadedBatchDefinition,
  materializeBatchDefinition,
  type StoredBatchDefinition,
  type StoredBatchOperation
} from "../batchStore.js";
import type { ToolContext } from "./context.js";
import {
  BATCH_ALLOWED_CHILD_TOOLS,
  BATCH_EXECUTION_TOOLS,
  BATCH_FILE_MUTATION_TOOLS,
  BATCH_MUTATING_CHILD_TOOLS,
  BATCH_PARALLEL_CHILD_TOOLS
} from "./registry.js";
import {
  auditStructuredResult,
  boundedBatchStructuredContent,
  errorResult,
  errorText,
  parseBool,
  textResult,
  truncateUtf8WithMarker,
  workspaceIdSchema
} from "./shared.js";

const BATCH_OPERATION_SCHEMA = z.object({
  id: z.string().regex(/^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$/).optional(),
  tool: z.enum([
    "tree",
    "search",
    "ast_grep",
    "read",
    "inspect_workspace",
    "show_changes",
    "write",
    "edit",
    "apply_patch",
    "bash"
  ]),
  args: z.record(z.unknown()).optional()
}).strict();

const BATCH_OPERATIONS_SCHEMA = z.array(BATCH_OPERATION_SCHEMA).min(1).max(12);

const STORED_BATCH_DEFINITION_SCHEMA = z.object({
  version: z.literal(BATCH_DEFINITION_VERSION),
  mode: z.enum(["serial", "parallel"]),
  continue_on_error: z.boolean(),
  operations: BATCH_OPERATIONS_SCHEMA
}).strict();

export function registerBatchTools(ctx: ToolContext): void {
  const { config, workspaces, guard } = ctx;

  ctx.register(

    "batch",
    {
      title: "Batch Operations",
      description:
        "Run a meaningful multi-step workflow, not a wrapper around ordinary calls. Use direct tools for one or two simple read-only calls and for a one-file mutation followed only by read/show_changes. A serial batch may contain several write/edit children only when each targets a distinct file; combine all same-file hunks into one edit. apply_patch remains exclusive because one patch may already span files. Use batch for three or more related reads, coordinated distinct-file mutations, actual Bash verification, or a sequence deliberately retained for resume. Inline verification workflows persist by default; other inline batches are one-shot unless persist=true.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        operations: BATCH_OPERATIONS_SCHEMA.optional().describe("One to twelve inline operations. Cannot be combined with path. Use direct tools for 1-2 simple read-only calls. Serial write/edit children must target distinct files; combine same-file changes into one edit."),
        path: z.string().optional().describe("Workspace-relative JSON batch file to execute. Cannot be combined with operations; mode and continue_on_error come from the file."),
        from: z.string().regex(/^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$/).optional().describe("Stored batches only: start at this operation id, inclusive."),
        from_index: z.number().int().min(0).max(11).optional().describe("Stored batches only: start at this zero-based operation index, inclusive. Cannot be combined with from."),
        mode: z.enum(["serial", "parallel"]).optional().describe("serial by default. parallel is for 3+ independent read-only operations and is limited to parallel-safe tools."),
        continue_on_error: z.boolean().optional().describe("Continue after child failures. Read-only batches only; default false."),
        persist: z.boolean().optional().describe("Inline batches only. Defaults true when the batch contains Bash verification and false for 1-2 read-only operations or other non-verification workflows.")
      },
      annotations: config.writeMode === "workspace" || config.bashMode !== "off"
        ? { readOnlyHint: false, openWorldHint: false, destructiveHint: true, idempotentHint: false }
        : { readOnlyHint: true, openWorldHint: false, destructiveHint: false, idempotentHint: false },

    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const inlineSupplied = Array.isArray(args.operations);
      const pathSupplied = typeof args.path === "string" && Boolean(args.path.trim());
      if (inlineSupplied === pathSupplied) {
        throw new CodexProError("Provide exactly one of operations or path.", { code: "batch_args_invalid", retryUnchanged: false });
      }
      if (args.from !== undefined && args.from_index !== undefined) {
        throw new CodexProError("Provide from or from_index, not both.", { code: "batch_args_invalid", retryUnchanged: false });
      }
      if (inlineSupplied && (args.from !== undefined || args.from_index !== undefined)) {
        throw new CodexProError("from and from_index apply only when executing a stored batch path.", { code: "batch_args_invalid", retryUnchanged: false });
      }
      if (pathSupplied && args.persist !== undefined) {
        throw new CodexProError("persist applies only to inline operations. A stored path is already persistent.", { code: "batch_args_invalid", retryUnchanged: false });
      }

      let batchSource: "inline" | "file" = inlineSupplied ? "inline" : "file";
      let batchPath: string | undefined;
      let batchTag: string | undefined;
      let autoStored = false;
      let persisted = false;
      let gitExcluded = false;
      let prunedBatchPaths: string[] = [];
      let sourceOperations: any[];
      let mode: "serial" | "parallel";
      let continueOnError: boolean;
      let persistenceDefault = false;
      let persistenceRequested = false;
      let efficiencyHint: string | undefined;

      if (pathSupplied) {
        if (args.mode !== undefined || args.continue_on_error !== undefined) {
          throw new CodexProError("Stored batch mode and continue_on_error come from the JSON file. Edit the file instead of overriding them.", { code: "batch_args_invalid", retryUnchanged: false });
        }
        const loaded = await loadBatchDefinition(config, guard, workspace, String(args.path));
        const parsed = STORED_BATCH_DEFINITION_SCHEMA.safeParse(loaded.definition);
        if (!parsed.success) {
          const details = parsed.error.issues
            .map((issue) => `${issue.path.length ? issue.path.join(".") : "definition"}: ${issue.message}`)
            .join("; ");
          throw new CodexProError(`Invalid stored batch ${loaded.path}: ${details}`, { code: "batch_file_invalid", retryUnchanged: false });
        }
        sourceOperations = parsed.data.operations;
        mode = parsed.data.mode;
        continueOnError = parsed.data.continue_on_error;
        batchPath = loaded.path;
        batchTag = loaded.autoStored
          ? loaded.path.match(/(?:^|\/)([0-9A-F]{4})\.json$/i)?.[1]?.toUpperCase()
          : undefined;
        autoStored = loaded.autoStored;
        persisted = true;
      } else {
        sourceOperations = args.operations;
        mode = args.mode ?? "serial";
        continueOnError = parseBool(args.continue_on_error, false);
      }

      const allOperations = sourceOperations.map((operation: any, index: number) => ({
        index,
        id: operation.id ?? `op_${index + 1}`,
        tool: String(operation.tool),
        args: operation.args && typeof operation.args === "object" && !Array.isArray(operation.args)
          ? { ...operation.args }
          : {},
        validatedArgs: undefined as any
      }));

      const ids = new Set<string>();
      for (const operation of allOperations) {
        if (ids.has(operation.id)) throw new CodexProError(`Duplicate batch operation id: ${operation.id}`, { code: "batch_duplicate_id", retryUnchanged: false });
        ids.add(operation.id);
        if (!BATCH_ALLOWED_CHILD_TOOLS.has(operation.tool)) {
          throw new CodexProError(`Tool ${operation.tool} is not allowed inside batch.`, { code: "batch_child_not_allowed", retryUnchanged: false });
        }
        if (Object.prototype.hasOwnProperty.call(operation.args, "workspace_id")) {
          throw new CodexProError(`Operation ${operation.id} must not provide workspace_id; use the outer batch workspace_id.`, { code: "batch_args_invalid", retryUnchanged: false });
        }
        if (!ctx.registeredToolHandler(operation.tool)) {
          throw new CodexProError(`Tool ${operation.tool} is not available in the current CodexPro mode.`, { code: "batch_child_not_allowed", retryUnchanged: false });
        }
        const validator = ctx.registeredToolValidator(operation.tool);
        if (!validator) {
          throw new CodexProError(`Tool ${operation.tool} has no registered batch validator.`, { code: "batch_child_not_allowed", retryUnchanged: false });
        }
        operation.validatedArgs = validator({ ...operation.args, workspace_id: args.workspace_id });
      }

      const fileMutations = allOperations.filter((operation: any) => BATCH_FILE_MUTATION_TOOLS.has(operation.tool));
      const verificationCommands = allOperations.filter((operation: any) => BATCH_EXECUTION_TOOLS.has(operation.tool));
      const controlledOperations = [...fileMutations, ...verificationCommands];
      const canPersist = config.writeMode === "workspace" && !config.connectionTest;
      persistenceDefault = verificationCommands.length > 0;
      persistenceRequested = pathSupplied || parseBool(args.persist, persistenceDefault);
      if (inlineSupplied && args.persist === true && !canPersist) {
        throw new CodexProError("persist=true requires workspace write mode and is unavailable in connection-test mode.", { code: "batch_persist_disabled", retryUnchanged: false });
      }
      if (inlineSupplied && allOperations.length <= 2 && verificationCommands.length === 0) {
        efficiencyHint = fileMutations.length === 1
          ? "Prefer the direct mutation tool for a one-off one-file change; its result already includes the diff. Add batch for coordinated distinct-file mutations, actual Bash verification, or a resumable workflow."
          : fileMutations.length === 0
            ? "Prefer direct tool calls for one or two ordinary reads. Use one consolidated parallel batch for three or more independent reads instead of several tiny batches."
            : undefined;
      }

      const patchMutations = fileMutations.filter((operation: any) => operation.tool === "apply_patch");
      if (patchMutations.length && fileMutations.length > 1) {
        throw new CodexProError(
          `apply_patch batch operation ${patchMutations[0].id} must be the only file-mutation child because one patch may already span several files. ` +
          "Use separate write/edit children only for distinct single-file targets.",
          { code: "batch_mutation_conflict", retryUnchanged: false }
        );
      }
      const canonicalMutationPath = async (absolutePath: string): Promise<string> => {
        let probe = path.resolve(absolutePath);
        const missingSegments: string[] = [];
        while (true) {
          try {
            const real = await fsp.realpath(probe);
            return path.resolve(real, ...missingSegments);
          } catch (error) {
            const code = error && typeof error === "object" && "code" in error ? String(error.code) : "";
            if (code !== "ENOENT" && code !== "ENOTDIR") throw error;
            const parent = path.dirname(probe);
            if (parent === probe) return path.resolve(absolutePath);
            missingSegments.unshift(path.basename(probe));
            probe = parent;
          }
        }
      };
      const mutationTargets = new Map<string, any>();
      for (const operation of fileMutations.filter((candidate: any) => candidate.tool === "write" || candidate.tool === "edit")) {
        const requestedPath = String(operation.validatedArgs.path ?? "");
        const resolved = guard.resolve(workspace, requestedPath, { forWrite: true });
        const canonicalPath = await canonicalMutationPath(resolved.absPath);
        const key = process.platform === "win32" ? canonicalPath.toLowerCase() : canonicalPath;
        const previous = mutationTargets.get(key);
        if (previous) {
          throw new CodexProError(
            `Batch operations ${previous.id} and ${operation.id} both mutate ${resolved.relPath}. ` +
            "Combine all changes to one file into one tagged edit or one write operation.",
          { code: "batch_mutation_conflict", retryUnchanged: false }
        );
        }
        mutationTargets.set(key, operation);
      }
      if (controlledOperations.length && mode !== "serial") {
        throw new CodexProError("A batch containing a file mutation or Bash verification must use mode=serial.", { code: "batch_mode_serial_required", retryUnchanged: false });
      }
      if (controlledOperations.length && continueOnError) {
        throw new CodexProError("continue_on_error is allowed only for batches containing read-only child tools.", { code: "batch_args_invalid", retryUnchanged: false });
      }
      for (const operation of verificationCommands) {
        assertVerificationCommand(config, String(operation.validatedArgs.command ?? ""));
        if (operation.validatedArgs.background === true) {
          throw new CodexProError(`Batch operation ${operation.id}: background bash is not allowed inside a batch; start it with the bash tool directly.`, { code: "args_invalid", retryUnchanged: false });
        }
        // A verification step that has not finished has not verified anything.
        operation.validatedArgs.on_timeout = "kill";
      }
      if (fileMutations.length) {
        const finalMutationIndex = Math.max(...fileMutations.map((operation: any) => allOperations.indexOf(operation)));
        const earlyVerification = verificationCommands.find((operation: any) => allOperations.indexOf(operation) < finalMutationIndex);
        if (earlyVerification) {
          throw new CodexProError(
            `Verification Bash operation ${earlyVerification.id} appears before the final file mutation. Put all verification commands after every write/edit/apply_patch operation.`,
          { code: "batch_verification_order", retryUnchanged: false }
        );
        }
      }
      if (mode === "parallel") {
        const unsafe = allOperations.filter((operation: any) => !BATCH_PARALLEL_CHILD_TOOLS.has(operation.tool));
        if (unsafe.length) {
          throw new CodexProError(
            `Parallel batch contains non-parallel-safe tools: ${unsafe.map((operation: any) => operation.tool).join(", ")}. Use mode=serial.`,
          { code: "batch_parallel_unsafe_child", retryUnchanged: false }
        );
        }
      }

      let startIndex = 0;
      if (typeof args.from === "string") {
        const requestedId = args.from.trim();
        const foundIndex = allOperations.findIndex((operation: any) => operation.id === requestedId);
        if (foundIndex < 0) {
          throw new CodexProError(
            `Batch operation id not found: ${requestedId}. Available ids: ${allOperations.map((operation: any) => operation.id).join(", ")}.`,
          { code: "batch_resume_invalid", retryUnchanged: false }
        );
        }
        startIndex = foundIndex;
      } else if (typeof args.from_index === "number") {
        if (args.from_index >= allOperations.length) {
          throw new CodexProError(
            `from_index ${args.from_index} is outside this ${allOperations.length}-operation batch.`,
          { code: "batch_resume_invalid", retryUnchanged: false }
        );
        }
        startIndex = args.from_index;
      }

      if (pathSupplied && batchPath) {
        const maintained = await maintainLoadedBatchDefinition(config, guard, workspace, batchPath);
        gitExcluded = maintained.gitExcluded;
        prunedBatchPaths = maintained.prunedPaths;
      }
      if (inlineSupplied && persistenceRequested && canPersist) {
        const definition: StoredBatchDefinition = {
          version: BATCH_DEFINITION_VERSION,
          mode,
          continue_on_error: continueOnError,
          operations: allOperations.map((operation: any): StoredBatchOperation => ({
            id: operation.id,
            tool: operation.tool,
            args: operation.args
          }))
        };
        const stored = await materializeBatchDefinition(config, guard, workspace, definition);
        batchPath = stored.path;
        batchTag = stored.batchTag;
        autoStored = true;
        persisted = true;
        gitExcluded = stored.gitExcluded;
        prunedBatchPaths = stored.prunedPaths;
      }

      const operations = allOperations.slice(startIndex);
      const executedControlledOperations = operations.filter((operation: any) =>
        BATCH_MUTATING_CHILD_TOOLS.has(operation.tool)
      );

      const aggregateTextBudget = config.maxOutputBytes;
      const textHeaderReserve = Math.min(
        Math.floor(aggregateTextBudget / 2),
        512 + operations.length * 96
      );
      const childTextBudget = Math.max(
        64,
        Math.floor(Math.max(0, aggregateTextBudget - textHeaderReserve) / operations.length)
      );
      const childStructuredBudget = Math.max(
        256,
        Math.floor(config.maxOutputBytes / Math.max(2, operations.length * 2))
      );

      type BatchChildResult = {
        id: string;
        index: number;
        tool: string;
        ok: boolean;
        skipped?: boolean;
        text?: string;
        textTruncated?: boolean;
        structured?: unknown;
        structuredTruncated?: boolean;
        changedPaths?: string[];
        error?: string;
      };
      const resultText = (raw: any): { value?: string; truncated: boolean } => {
        if (!Array.isArray(raw?.content)) return { truncated: false };
        const text = raw.content
          .filter((item: any) => item?.type === "text" && typeof item.text === "string")
          .map((item: any) => item.text)
          .join("\n");
        if (!text) return { truncated: false };
        const bounded = truncateUtf8WithMarker(text, childTextBudget, "\n...[batch child output truncated]");
        return { value: bounded.value, truncated: bounded.truncated };
      };
      const childStructured = (raw: any): { value?: unknown; truncated: boolean } => {
        if (!raw || typeof raw !== "object") return { truncated: false };
        const structured = raw.structuredContent && typeof raw.structuredContent === "object"
          ? raw.structuredContent
          : undefined;
        if (structured === undefined) return { truncated: false };
        const bounded = boundedBatchStructuredContent(structured, childStructuredBudget);
        return { value: bounded.value, truncated: bounded.truncated };
      };
      const structuredChangedPaths = (structured: Record<string, unknown>): string[] => {
        const paths = new Set<string>();
        const candidates: unknown[] = [structured.path];
        for (const key of ["paths", "changed_paths", "changed_files"]) {
          const value = structured[key];
          if (Array.isArray(value)) candidates.push(...value);
        }
        for (const candidate of candidates) {
          if (typeof candidate === "string") paths.add(candidate);
          else if (candidate && typeof candidate === "object" && typeof (candidate as any).path === "string") {
            paths.add((candidate as any).path);
          }
        }
        return [...paths];
      };
      const runOperation = async (operation: any): Promise<BatchChildResult> => {
        const handler = ctx.registeredToolHandler(operation.tool);
        if (!handler) return { id: operation.id, index: operation.index, tool: operation.tool, ok: false, error: "Tool became unavailable." };
        try {
          let raw: any;
          try {
            const envelope = currentToolContext()?.workEnvelope;
            raw = await handler({ ...operation.validatedArgs, ...(envelope ? { execution: { ...envelope, operation_key: digest([envelope.operation_key, operation.id]) } } : {}) });
          } catch (error) {
            raw = errorResult(error);
          }
          const rawStructured = auditStructuredResult(raw);
          const bashExitCode = typeof rawStructured.exit_code === "number" ? rawStructured.exit_code : undefined;
          const bashSignal = typeof rawStructured.signal === "string" && rawStructured.signal ? rawStructured.signal : undefined;
          const bashTimedOut = rawStructured.timed_out === true;
          const bashFailed = operation.tool === "bash" && (bashTimedOut || bashSignal !== undefined || (bashExitCode !== undefined && bashExitCode !== 0));
          const ok = raw?.isError !== true && !bashFailed;
          const childError = bashTimedOut
            ? "Bash command timed out."
            : bashSignal
              ? `Bash command terminated by signal ${bashSignal}.`
              : bashExitCode !== undefined && bashExitCode !== 0
                ? `Bash command exited with code ${bashExitCode}.`
                : raw?.isError === true
                  ? errorText(rawStructured.error ?? "Child tool returned an error.")
                  : undefined;
          const childText = resultText(raw);
          const childData = childStructured(raw);
          return {
            id: operation.id,
            index: operation.index,
            tool: operation.tool,
            ok,
            text: childText.value,
            textTruncated: childText.truncated || undefined,
            structured: childData.value,
            structuredTruncated: childData.truncated || undefined,
            changedPaths: BATCH_MUTATING_CHILD_TOOLS.has(operation.tool)
              ? structuredChangedPaths(rawStructured)
              : undefined,
            error: childError
          };
        } catch (error) {
          return { id: operation.id, index: operation.index, tool: operation.tool, ok: false, error: errorText(error) };
        }
      };

      const results: BatchChildResult[] = [];
      if (mode === "parallel") {
        results.push(...await Promise.all(operations.map(runOperation)));
      } else {
        for (let index = 0; index < operations.length; index += 1) {
          const result = await runOperation(operations[index]);
          results.push(result);
          if (!result.ok && !continueOnError) {
            for (const skipped of operations.slice(index + 1)) {
              results.push({
                id: skipped.id,
                index: skipped.index,
                tool: skipped.tool,
                ok: false,
                skipped: true,
                error: "Skipped after an earlier operation failed."
              });
            }
            break;
          }
        }
      }

      const changedPaths = new Set<string>();
      for (const result of results) {
        if (!result.ok) continue;
        for (const changedPath of result.changedPaths ?? []) changedPaths.add(changedPath);
      }

      const succeeded = results.filter((result) => result.ok).length;
      const skipped = results.filter((result) => result.skipped).length;
      const failed = results.length - succeeded - skipped;
      const failedResult = results.find((result) => !result.ok && !result.skipped);
      const sections = results.map((result) => {
        const marker = result.ok ? "✓" : result.skipped ? "–" : "✗";
        const body = result.ok
          ? result.text ?? "Completed."
          : [result.error ?? "Failed.", result.text].filter(Boolean).join("\n\n");
        return `## ${marker} [${result.index}] ${result.id} — ${result.tool}\n\n${body}`;
      });
      const resumeLine = batchPath && failedResult
        ? `Resume: batch(path="${batchPath}", from="${failedResult.id}")`
        : batchPath
          ? `Stored definition: ${batchPath}`
          : undefined;
      const assembledText = [
        "# Batch Operations",
        "",
        ...(batchPath ? [`Batch file: ${batchPath}${batchTag ? ` (${batchTag})` : ""}`] : []),
        `Source: ${batchSource}${persisted ? " · persisted" : " · one-shot"}`,
        `Mode: ${mode}`,
        `Start: ${startIndex} (${operations[0].id})`,
        `Operations: ${operations.length} of ${allOperations.length}`,
        `Succeeded: ${succeeded}`,
        `Failed: ${failed}`,
        `Skipped: ${skipped}`,
        ...(efficiencyHint ? ["", `Efficiency: ${efficiencyHint}`] : []),
        ...(resumeLine ? ["", resumeLine] : []),
        "",
        ...sections
      ].join("\n");
      const boundedText = truncateUtf8WithMarker(
        assembledText,
        aggregateTextBudget,
        "\n...[batch aggregate output truncated]"
      );
      const publicResults = results.map(({ text, textTruncated, structuredTruncated, changedPaths: childChangedPaths, ...result }) => ({
        ...result,
        text_in_content: Boolean(text),
        text_truncated: textTruncated || undefined,
        structured_truncated: structuredTruncated || undefined,
        changed_paths: childChangedPaths?.length ? childChangedPaths : undefined
      }));
      const visiblePrunedBatchPaths = prunedBatchPaths.slice(0, BATCH_STORE_LIMIT);
      const prunedBatchPathsTruncated = visiblePrunedBatchPaths.length < prunedBatchPaths.length;
      const response = textResult(boundedText.value, {
        workspace_id: args.workspace_id,
        batch_source: batchSource,
        batch_path: batchPath,
        batch_tag: batchTag,
        persisted,
        persistence_default: persistenceDefault,
        persistence_requested: persistenceRequested,
        efficiency_hint: efficiencyHint,
        auto_stored: autoStored,
        git_excluded: gitExcluded,
        retention_limit: BATCH_STORE_LIMIT,
        pruned_batch_count: prunedBatchPaths.length,
        pruned_batch_paths: visiblePrunedBatchPaths,
        pruned_batch_paths_truncated: prunedBatchPathsTruncated,
        mode,
        mutating: executedControlledOperations.length > 0 || (inlineSupplied && persisted),
        total_operation_count: allOperations.length,
        start_index: startIndex,
        start_operation_id: operations[0].id,
        operation_count: operations.length,
        executed_operation_count: results.filter((result) => !result.skipped).length,
        succeeded_count: succeeded,
        failed_count: failed,
        skipped_count: skipped,
        failed_operation_id: failedResult?.id,
        failed_index: failedResult?.index,
        resumable_from: batchPath && failedResult ? failedResult.id : undefined,
        succeeded: failed === 0,
        output_truncated: boundedText.truncated,
        child_text_truncated_count: results.filter((result) => result.textTruncated).length,
        child_structured_truncated_count: results.filter((result) => result.structuredTruncated).length,
        changed_paths: [...changedPaths],
        results: publicResults
      });
      const executedBashIds = new Set(
        results
          .filter((result) => result.tool === "bash" && !result.skipped)
          .map((result) => result.id)
      );
      const shellScripts = operations
        .filter((operation: any) => operation.tool === "bash" && executedBashIds.has(operation.id))
        .map((operation: any) => ({
          operation_id: operation.id,
          script: String(operation.validatedArgs.command ?? "")
        }));
      if (shellScripts.length) {
        attachActionDashboardMetadata(response, { shell_scripts: shellScripts });
      }
      if (failed > 0) response.isError = true;
      return response;
    }
  );
}
