import { applyWorkspacePatch } from "../patchOps.js";
import { z } from "zod";
import type { CodexProConfig } from "../config.js";
import { CodexProError } from "../guard.js";
import { repoTree, readTextFile, writeTextFile, editTextFileByLines, type AnchoredLineEdit } from "../fsOps.js";
import { viewWorkspaceImage } from "../imageOps.js";
import { importAttachmentFile } from "../importOps.js";
import { searchWorkspace } from "../searchOps.js";
import { astGrepWorkspace } from "../astGrepOps.js";
import { redactSensitiveText, redactStructured } from "../redact.js";
import { invalidateWorkspaceAnalysis } from "../analysis/index.js";
import type { ToolContext } from "./context.js";
import {
  LOCAL_WRITE_ANNOTATIONS,
  READ_ONLY_ANNOTATIONS,
  assertWriteToolAllowed,
  diffBlock,
  limitInt,
  parseBool,
  textResult,
  toolMeta,
  workspaceIdSchema
} from "./shared.js";

export function registerFileTools(ctx: ToolContext): void {
  const { config, workspaces, guard, editSnapshots } = ctx;

  ctx.register(

    "tree",
    {
      title: "File Tree",
      description: "List files and directories inside the workspace, excluding blocked paths.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        path: z.string().optional().describe("Directory relative to workspace root. Default: ."),
        max_depth: z.number().int().min(1).max(12).optional().describe("Maximum depth. Default: 4."),
        include_hidden: z.boolean().optional().describe("Include dotfiles/dotfolders that are not blocked. Default: false."),
        max_entries: z.number().int().min(1).max(3000).optional().describe("Maximum entries. Default: 800.")
      },
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("tree")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const result = await repoTree(config, guard, workspace, {
        path: args.path ?? ".",
        maxDepth: limitInt(args.max_depth, 4, 1, 12),
        includeHidden: parseBool(args.include_hidden, false),
        maxEntries: limitInt(args.max_entries, 800, 1, 3000)
      });
      return textResult(result.text, {
        workspace_id: workspace.id,
        root: workspace.root,
        text: result.text,
        entries: result.entries,
        truncated: result.truncated
      });
    }
  );


  ctx.register(

    "search",
    {
      title: "Search Files",
      description:
        "Find text (or a regex with regex=true) across the workspace and get each match with surrounding lines. Use it before read to locate code, and instead of bash grep/rg. Results carry an edit_tag when a complete current-file context is shown, so small edits can follow directly. kind=config queries JSON/YAML/TOML paths such as jobs.*.steps[*].uses; scope limits the search to changed files or added/removed diff lines. Paged: when has_more is true, pass next_cursor with the identical query options. For structural code questions use ast_grep.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        query: z.string().min(1).max(4000).describe("Text/regex to find, or a dotted/JSON-pointer configuration path when kind=config."),
        kind: z.enum(["text", "config"]).optional().describe("Text search or structured configuration-path query. Default: text."),
        regex: z.boolean().optional().describe("Treat a text query as a regular expression. Default: false. Not valid for kind=config."),
        path: z.string().optional().describe("Directory or file relative to workspace root. Default: ."),
        glob: z.string().optional().describe("Optional glob, for example src/**/*.ts or **/*.yaml."),
        include_hidden: z.boolean().optional().describe("Include hidden files that are not blocked. Default: false."),
        max_results: z.number().int().min(1).max(2000).optional().describe("Maximum matches in this page. Default from config."),
        context_before: z.number().int().min(0).max(20).optional().describe("Lines before each match. Default: 2."),
        context_after: z.number().int().min(0).max(20).optional().describe("Lines after each match. Default: 2."),
        group_by_file: z.boolean().optional().describe("Merge overlapping context ranges in each file. Default: true."),
        cursor: z.string().max(4096).optional().describe("Opaque next_cursor from the previous page. All other search options must remain identical."),
        scope: z.enum(["workspace", "changed_files", "diff_added", "diff_removed"]).optional().describe("Search the workspace, current changed files, added diff lines, or removed diff lines. Default: workspace."),
        base_ref: z.string().max(256).optional().describe("Git base ref for non-workspace scopes. Default: HEAD."),
        diff_target: z.enum(["worktree", "staged", "head"]).optional().describe("Compare base_ref to the working tree, index, or HEAD. Default: worktree."),
        include_untracked: z.boolean().optional().describe("Include untracked files for worktree changed_files/diff_added searches. Default: true."),
        config_format: z.enum(["auto", "json", "yaml", "toml"]).optional().describe("Configuration format. Default: infer from extension."),
        intent: z.enum(["auto", "text", "symbol", "references", "impact"]).optional().describe("Optional structured repository-analysis intent for first-page workspace text searches."),
        symbol: z.string().optional().describe("Optional symbol query. Uses repository analysis and overrides query text."),
        include_tests: z.boolean().optional().describe("Include related tests in structured repository-analysis results. Default: false.")
      },
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("search")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const result = await searchWorkspace(config, guard, workspace, {
        query: args.query,
        kind: args.kind ?? "text",
        regex: parseBool(args.regex, false),
        root: args.path ?? ".",
        glob: args.glob,
        includeHidden: parseBool(args.include_hidden, false),
        maxResults: limitInt(args.max_results, config.maxSearchResults, 1, config.maxSearchResults),
        contextBefore: limitInt(args.context_before, 2, 0, 20),
        contextAfter: limitInt(args.context_after, 2, 0, 20),
        groupByFile: parseBool(args.group_by_file, true),
        cursor: args.cursor,
        scope: args.scope ?? "workspace",
        baseRef: args.base_ref,
        diffTarget: args.diff_target ?? "worktree",
        includeUntracked: parseBool(args.include_untracked, true),
        configFormat: args.config_format ?? "auto",
        intent: args.intent,
        symbol: args.symbol,
        includeTests: args.include_tests === undefined ? undefined : parseBool(args.include_tests, false),
        editSnapshots
      });
      const structured: Record<string, unknown> = {
        workspace_id: workspace.id,
        root: workspace.root,
        matches: result.matches,
        contexts: result.contexts,
        truncated: result.truncated,
        has_more: result.hasMore,
        next_cursor: result.nextCursor ?? null,
        query_fingerprint: result.queryFingerprint,
        used: result.used,
        scope: result.scope,
        kind: result.kind,
        warnings: result.warnings
      };
      if (result.analysis) structured.analysis = result.analysis;
      return textResult(result.text, structured);
    }
  );



  ctx.register(


    "ast_grep",
    {
      title: "AST Grep",
      description:
        "Search source structurally with ast-grep/Tree-sitter syntax patterns or node kinds. This is syntax-aware rather than type-aware semantic navigation. Complete current-file contexts return edit_tag provenance and can be edited directly. Reuse next_cursor only with the exact same structural query options.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        pattern: z.string().min(1).max(20000).optional().describe("ast-grep structural pattern, for example console.log($ARG). Provide exactly one of pattern or kind."),
        kind: z.string().min(1).max(256).optional().describe("Tree-sitter node kind, for example function_declaration. Provide exactly one of pattern or kind."),
        language: z.string().min(1).max(80).optional().describe("Optional ast-grep language id such as ts, tsx, js, py, go, rust, or java. Omit to infer from file extensions."),
        selector: z.string().min(1).max(256).optional().describe("Optional sub-node kind to return from a pattern match. Pattern mode only."),
        strictness: z.enum(["cst", "smart", "ast", "relaxed", "signature", "template"]).optional().describe("Pattern matching strictness. Pattern mode only; ast-grep defaults to smart."),
        path: z.string().optional().describe("Directory or file relative to the workspace root. Default: ."),
        globs: z.array(z.string().min(1).max(512)).max(64).optional().describe("Optional ast-grep include/exclude globs. Prefix an exclusion with !. Safety-blocked paths remain excluded."),
        include_hidden: z.boolean().optional().describe("Include hidden files that are not blocked. Default: false."),
        max_results: z.number().int().min(1).max(2000).optional().describe("Maximum structural matches in this page. Default from config."),
        context_before: z.number().int().min(0).max(20).optional().describe("Lines before each structural match. Default: 2."),
        context_after: z.number().int().min(0).max(20).optional().describe("Lines after each structural match. Default: 2."),
        group_by_file: z.boolean().optional().describe("Merge overlapping structural context ranges in each file. Default: true."),
        cursor: z.string().max(4096).optional().describe("Opaque next_cursor from the previous page. Every other query option must remain identical."),
        timeout_ms: z.number().int().min(1000).max(60000).optional().describe("Native ast-grep process timeout. Default: 15000 ms.")
      },
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("ast_grep")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const result = await astGrepWorkspace(config, guard, workspace, {
        pattern: args.pattern,
        kind: args.kind,
        language: args.language,
        selector: args.selector,
        strictness: args.strictness,
        root: args.path ?? ".",
        globs: args.globs ?? [],
        includeHidden: parseBool(args.include_hidden, false),
        maxResults: limitInt(args.max_results, config.maxSearchResults, 1, config.maxSearchResults),
        contextBefore: limitInt(args.context_before, 2, 0, 20),
        contextAfter: limitInt(args.context_after, 2, 0, 20),
        groupByFile: parseBool(args.group_by_file, true),
        cursor: args.cursor,
        timeoutMs: limitInt(args.timeout_ms, 15000, 1000, 60000),
        editSnapshots
      });
      return textResult(result.text, {
        workspace_id: workspace.id,
        root: workspace.root,
        provider: result.provider,
        provider_version: result.providerVersion,
        mode: result.mode,
        matches: result.matches,
        contexts: result.contexts,
        truncated: result.truncated,
        has_more: result.hasMore,
        next_cursor: result.nextCursor ?? null,
        query_fingerprint: result.queryFingerprint,
        warnings: result.warnings
      });
    }
  );


  ctx.register(

    "read",
    {
      title: "Read File",
      description: "Read a specific text file with line numbers and a four-character edit_tag. Before editing, read every range you plan to change, then send all intended same-file changes in one combined multi-hunk edit. Re-read after any mutation before another tagged edit; otherwise avoid redundant rereads when the returned diff is sufficient.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        path: z.string().describe("File path relative to workspace root."),
        start_line: z.number().int().min(1).optional().describe("First line to read. Default: 1."),
        end_line: z.number().int().min(1).optional().describe("Last line to read. Default: end of file."),
        max_bytes: z.number().int().min(1000).max(2000000).optional().describe("Maximum file bytes. Capped by server config.")
      },
      annotations: READ_ONLY_ANNOTATIONS,
      _meta: toolMeta("read")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const result = await readTextFile(config, guard, workspace, args.path, {
        startLine: args.start_line,
        endLine: args.end_line,
        maxBytes: args.max_bytes,
        editSnapshots
      });
      const text = `# Read File\n\nPath: ${result.path}\nLines: ${result.startLine}-${result.endLine} of ${result.totalLines}\nBytes: ${result.bytes}\nSHA-256: ${result.sha256}\nEdit tag: ${result.editTag}\n\nEvery displayed line number belongs to this four-character edit tag. Pass it as edit_tag to edit; all hunks in that call are resolved against these original line numbers.\n\n\`\`\`text\n${result.text}\n\`\`\``;
      return textResult(text, {
        workspace_id: workspace.id,
        root: workspace.root,
        path: result.path,
        text: result.text,
        start_line: result.startLine,
        end_line: result.endLine,
        total_lines: result.totalLines,
        bytes: result.bytes,
        sha256: result.sha256,
        truncated: result.truncated,
        edit_tag: result.editTag
      });
    }
  );


  ctx.register(

    "view_image",
    {
      title: "View Image",
      description: "Inspect a PNG, JPEG, GIF, or WebP image from the active workspace. Returns native MCP image content plus dimensions and SHA-256.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        path: z.string().describe("Image path relative to workspace root."),
        max_bytes: z.number().int().min(4096).max(2000000).optional().describe("Maximum image bytes. Default: at least 1 MB, capped at 2 MB.")
      },
      annotations: READ_ONLY_ANNOTATIONS
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const result = await viewWorkspaceImage(config, guard, workspace, args.path, args.max_bytes);
      const dimensions = result.width && result.height ? `${result.width}x${result.height}` : "unknown";
      return {
        content: [
          {
            type: "text",
            text: `Image: ${result.path}\nType: ${result.mimeType}\nDimensions: ${dimensions}\nBytes: ${result.bytes}\nSHA-256: ${result.sha256}`
          },
          { type: "image", data: result.data, mimeType: result.mimeType }
        ],
        structuredContent: redactStructured({
          workspace_id: workspace.id,
          root: workspace.root,
          path: result.path,
          mime_type: result.mimeType,
          width: result.width ?? null,
          height: result.height ?? null,
          bytes: result.bytes,
          sha256: result.sha256
        })
      };
    }
  );


  ctx.register(

    "write",
    {
      title: "Write File",
      description: "Create a new text file, or replace a whole file when a tagged edit is not practical (prefer edit for changes to an existing file). Returns a unified diff. When overwriting a file another session may have touched, pass expected_sha256 (the SHA-256 printed by read) so the write fails instead of clobbering.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        path: z.string().describe("File path relative to workspace root."),
        content: z.string().describe("Complete file contents to write."),
        create_dirs: z.boolean().optional().describe("Create parent directories if missing. Default: true."),
        overwrite: z.boolean().optional().describe("Allow overwriting existing files. Default: true."),
        expected_sha256: z.string().regex(/^[a-f0-9]{64}$/i).optional().describe("Optional SHA-256 from read. Fails instead of overwriting if another session changed the file.")
      },
      annotations: LOCAL_WRITE_ANNOTATIONS,
      _meta: toolMeta("write")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const resolved = guard.resolve(workspace, args.path, { forWrite: true });
      assertWriteToolAllowed(config, resolved.relPath);
      const result = await writeTextFile(config, guard, workspace, args.path, String(args.content ?? ""), {
        createDirs: args.create_dirs !== false,
        overwrite: args.overwrite !== false,
        expectedSha256: args.expected_sha256
      });
      if (result.diff.changed) invalidateWorkspaceAnalysis(workspace.id);
      const text = `# Write File\n\nPath: ${result.path}\nExisted before: ${result.existed}\nBytes: ${result.bytes}\nSHA-256: ${result.sha256}\nDiff stats: +${result.diff.additions} -${result.diff.deletions}${diffBlock(result.diff.diff)}`;
      return textResult(text, {
        workspace_id: workspace.id,
        root: workspace.root,
        path: result.path,
        existed: result.existed,
        bytes: result.bytes,
        sha256: result.sha256,
        additions: result.diff.additions,
        deletions: result.diff.deletions,
        diff: result.diff.diff
      });
    }
  );


  ctx.register(

    "edit",
    {
      title: "Edit File",
      description:
        "Preferred tool for every one-file change, including many non-adjacent hunks. Immediately before editing, use the four-character edit_tag from a read or from a complete current-file search context that displayed every targeted line. Submit all intended changes for that file in a single tagged multi-hunk call, where every operation addresses the original tagged snapshot. Do not reuse the tag after any mutation. After an error, do not retry unchanged: follow error_code and recovery.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        path: z.string().describe("File path relative to workspace root."),
        edit_tag: z.string().regex(/^[0-9A-F]{4}$/i).describe(
          "Four-character edit tag returned by read. The connector resolves it to retained full content and rejects stale or colliding snapshots."
        ),
        edits: z.array(z.discriminatedUnion("op", [
          z.object({
            op: z.literal("replace"),
            start_line: z.number().int().min(1),
            end_line: z.number().int().min(1).optional(),
            content: z.string()
          }),
          z.object({
            op: z.literal("delete"),
            start_line: z.number().int().min(1),
            end_line: z.number().int().min(1).optional()
          }),
          z.object({
            op: z.literal("insert_before"),
            line: z.number().int().min(1),
            content: z.string()
          }),
          z.object({
            op: z.literal("insert_after"),
            line: z.number().int().min(1),
            content: z.string()
          })
        ])).min(1).max(100).describe(
          "Line operations against the original tagged snapshot. Targets must have been displayed by read."
        )
      },
      annotations: LOCAL_WRITE_ANNOTATIONS,
      _meta: toolMeta("edit")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const resolved = guard.resolve(workspace, args.path, { forWrite: true });
      assertWriteToolAllowed(config, resolved.relPath);
      const edits: AnchoredLineEdit[] = args.edits.map((edit: any) => {
        if (edit.op === "replace") {
          return {
            op: "replace",
            startLine: edit.start_line,
            endLine: edit.end_line,
            content: edit.content
          };
        }
        if (edit.op === "delete") {
          return { op: "delete", startLine: edit.start_line, endLine: edit.end_line };
        }
        return { op: edit.op, line: edit.line, content: edit.content };
      });
      const result = await editTextFileByLines(
        config,
        guard,
        workspace,
        args.path,
        edits,
        editSnapshots,
        args.edit_tag
      );
      invalidateWorkspaceAnalysis(workspace.id);
      const text = `# Edit File\n\nPath: ${result.path}\nOperations applied: ${result.edits}\nBase edit tag: ${result.baseTag}\nNew edit tag: ${result.editTag}\nBytes: ${result.bytes}\nDiff stats: +${result.diff.additions} -${result.diff.deletions}${diffBlock(result.diff.diff)}`;
      return textResult(text, {
        workspace_id: workspace.id,
        root: workspace.root,
        path: result.path,
        mode: "tagged_lines",
        edits_applied: result.edits,
        changed: true,
        bytes: result.bytes,
        base_edit_tag: result.baseTag,
        edit_tag: result.editTag,
        additions: result.diff.additions,
        deletions: result.diff.deletions,
        diff: result.diff.diff
      });
    }
  );


  ctx.register(

    "apply_patch",
    {
      title: "Apply Patch",
      description:
        "Apply a standard unified diff for a deliberate multi-file change or a file that tagged edit cannot handle. Use edit for every single-file change. Accepts raw Git unified diffs or native *** Begin Patch syntax (add/update/delete/move). Both formats use the same guarded preflight. Never resend a failed patch unchanged—read current targets and regenerate it.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        patch: z.string().describe("Git unified diff or native *** Begin Patch text. Paths must stay inside the workspace and avoid blocked paths.")
      },
      annotations: LOCAL_WRITE_ANNOTATIONS,
      _meta: toolMeta("apply_patch")
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const result = await applyWorkspacePatch(config, guard, workspace, String(args.patch ?? ""));
      if (result.changed) invalidateWorkspaceAnalysis(workspace.id);
      const text = [
        "# Apply Patch",
        "",
        `Paths: ${result.paths.join(", ")}`,
        `Diff stats: +${result.additions} -${result.deletions}`,
        result.stderr ? `stderr: ${result.stderr}` : "",
        result.diff ? diffBlock(result.diff) : "No diff output."
      ].filter(Boolean).join("\n");
      return textResult(text, {
        workspace_id: workspace.id,
        root: workspace.root,
        paths: result.paths,
        stdout: result.stdout,
        stderr: result.stderr,
        additions: result.additions,
        deletions: result.deletions,
        changed: result.changed,
        diff: result.diff
      });
    }
  );


  ctx.register(

    "import_file",
    {
      title: "Import Attachment File",
      description:
        "Import a ChatGPT Apps SDK attachment into the workspace. Accepts only a platform file object with download_url and file_id. Not a general URL downloader. Overwrite is off by default.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        file: z
          .object({
            download_url: z.string().describe("Temporary HTTPS download URL provided by ChatGPT."),
            file_id: z.string().describe("ChatGPT file id for this attachment."),
            mime_type: z.string().optional().describe("Optional MIME type declared by ChatGPT."),
            file_name: z.string().optional().describe("Optional original file name declared by ChatGPT.")
          })
          .describe("ChatGPT Apps SDK file reference from openai/fileParams."),
        destination: z.string().describe("Destination path relative to the workspace root."),
        overwrite: z.boolean().optional().describe("Replace an existing destination file. Default: false."),
        expected_sha256: z.string().regex(/^[a-f0-9]{64}$/i).optional().describe("Optional SHA-256 of the attachment bytes. Import fails on mismatch.")
      },
      annotations: LOCAL_WRITE_ANNOTATIONS,
      _meta: { ...toolMeta("import_file"), "openai/fileParams": ["file"] }
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const resolved = guard.resolve(workspace, args.destination, { forWrite: true });
      assertWriteToolAllowed(config, resolved.relPath);
      const result = await importAttachmentFile(config, guard, workspace, {
        file: args.file,
        destination: String(args.destination ?? ""),
        overwrite: args.overwrite === true,
        expectedSha256: args.expected_sha256
      });
      invalidateWorkspaceAnalysis(workspace.id);
      const text = [
        "# Import File",
        "",
        `Path: ${result.path}`,
        `Bytes: ${result.bytes}`,
        `SHA-256: ${result.sha256}`,
        `Declared MIME: ${result.declared_mime_type ?? "unknown"}`,
        `Detected MIME: ${result.detected_mime_type ?? "unknown"}`,
        `MIME status: ${result.mime_type_status}`,
        `Verified: ${result.verified}`,
        `Overwritten: ${result.overwritten}`
      ].join("\n");
      return textResult(text, {
        workspace_id: workspace.id,
        root: workspace.root,
        path: result.path,
        bytes: result.bytes,
        declared_mime_type: result.declared_mime_type,
        detected_mime_type: result.detected_mime_type,
        mime_type_status: result.mime_type_status,
        sha256: result.sha256,
        verified: result.verified,
        file_id: result.file_id,
        file_name: result.file_name,
        overwritten: result.overwritten
      });
    }
  );
}
