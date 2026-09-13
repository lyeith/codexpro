import fsp from "node:fs/promises";
import { createHash } from "node:crypto";
import { z } from "zod";
import type { CodexProConfig } from "../config.js";
import type { PathGuard, Workspace } from "../guard.js";
import { gitCommit, gitDiff, gitDiffStatus } from "../gitOps.js";
import { reviewWorkspaceChanges } from "../analysis/index.js";
import type { ToolContext } from "./context.js";
import {
  LOCAL_WRITE_ANNOTATIONS,
  READ_ONLY_ANNOTATIONS,
  changedPathsFromStatus,
  changedStatusLines,
  diffBlock,
  diffStats,
  errorText,
  looksLikeGitError,
  normalizeGitOutput,
  parseBool,
  textResult,
  workspaceIdSchema
} from "./shared.js";

function reviewCheckpointKey(workspace: Workspace, options: { path?: string; staged: boolean }): string {
  return `${workspace.id}\0${options.path ?? ""}\0${options.staged ? "staged" : "unstaged"}`;
}

function reviewFingerprint(status: string, diff: string): string {
  return createHash("sha256").update(status).update("\0").update(diff).digest("hex");
}

async function untrackedReviewFingerprint(config: CodexProConfig, guard: PathGuard, workspace: Workspace, changedFiles: string[]): Promise<string> {
  const hash = createHash("sha256");
  for (const line of changedFiles) {
    const match = line.match(/^\?\?\s+(.+)$/);
    if (!match) continue;
    const relPath = match[1];
    hash.update(relPath).update("\0");
    try {
      const resolved = guard.resolve(workspace, relPath);
      const stat = await fsp.stat(resolved.absPath);
      hash.update(String(stat.size)).update("\0").update(String(Math.floor(stat.mtimeMs))).update("\0");
      if (stat.isFile() && stat.size <= config.maxReadBytes) {
        hash.update(await fsp.readFile(resolved.absPath));
      }
    } catch (error) {
      hash.update(errorText(error));
    }
    hash.update("\0");
  }
  return hash.digest("hex");
}

export function registerGitTools(ctx: ToolContext): void {
  const { config, workspaces, guard, reviewCheckpoints } = ctx;

  ctx.register(

    "show_changes",
    {
      title: "Show Changes",
      description: "Review the working tree: git status, diff stats, the unified diff and change-impact analysis in one result. Use it instead of bash git status/diff. Every call reports the full current state; pass since=last_shown to get only what changed since the previous review.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        path: z.string().optional().describe("Optional file path relative to workspace root."),
        staged: z.boolean().optional().describe("Show staged diff. Default: false."),
        include_diff: z.boolean().optional().describe("Include the unified diff. Default: true."),
        since: z.enum(["last_shown", "workspace"]).optional().describe("workspace (default) reports the full current state; last_shown suppresses a diff already shown by the previous review."),
        mark_reviewed: z.boolean().optional().describe("Update the last-shown review checkpoint after this call. Default: true.")
      },
      annotations: READ_ONLY_ANNOTATIONS
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const scopedPath = typeof args.path === "string" ? args.path : undefined;
      const staged = parseBool(args.staged, false);
      const normalizedScopedPath = scopedPath?.trim() ? guard.resolve(workspace, scopedPath).relPath : undefined;
      const status = normalizeGitOutput(gitDiffStatus(config, guard, workspace, normalizedScopedPath, staged));
      const includeDiff = parseBool(args.include_diff, true);
      const rawDiff = normalizeGitOutput(gitDiff(config, guard, workspace, normalizedScopedPath, staged));
      const statusError = looksLikeGitError(status) ? status : "";
      const diffError = rawDiff && looksLikeGitError(rawDiff) ? rawDiff : "";
      const diff = diffError ? "" : rawDiff;
      const stats = diffStats(diff);
      const changedFiles = statusError ? [] : changedStatusLines(status);
      const untrackedFingerprint = statusError ? "" : await untrackedReviewFingerprint(config, guard, workspace, changedFiles);
      const since = args.since === "last_shown" ? "last_shown" : "workspace";
      const markReviewed = parseBool(args.mark_reviewed, true);
      const checkpointKey = reviewCheckpointKey(workspace, { path: normalizedScopedPath, staged });
      const fingerprint = reviewFingerprint(status, `${diff}\0${untrackedFingerprint}`);
      const checkpointHit = includeDiff && since === "last_shown" && reviewCheckpoints.get(checkpointKey) === fingerprint;
      const checkpointWritten = markReviewed && includeDiff;
      if (checkpointWritten) reviewCheckpoints.set(checkpointKey, fingerprint);
      const responseDiff = checkpointHit ? "" : includeDiff ? diff : "";
      const responseStats = checkpointHit ? { additions: 0, deletions: 0, changed: false } : stats;
      const changedPaths = statusError ? [] : changedPathsFromStatus(changedFiles);
      let analysis: Record<string, unknown> | undefined;
      if (config.analysisEnabled && changedPaths.length && !checkpointHit) {
        try {
          const impact = await reviewWorkspaceChanges(config, guard, workspace, { changedPaths });
          analysis = {
            schema_version: impact.schemaVersion,
            changed_paths: impact.changedPaths,
            affected_areas: impact.affectedAreas,
            dependent_files: impact.dependentFiles,
            related_tests: impact.relatedTests,
            risk_signals: impact.riskSignals,
            recommended_commands: impact.recommendedCommands,
            coverage: impact.coverage,
            warnings: impact.warnings,
            cache: impact.cache
          };
        } catch (error) {
          analysis = {
            schema_version: 1,
            changed_paths: changedPaths,
            affected_areas: [],
            dependent_files: [],
            related_tests: [],
            risk_signals: [],
            recommended_commands: [],
            warnings: [`Change analysis unavailable: ${errorText(error)}`]
          };
        }
      }
      const changedText = statusError
        ? `- Git status unavailable: ${statusError}`
        : checkpointHit
          ? "- No changes since last shown review."
          : changedFiles.length
          ? changedFiles.map((line) => `- ${line}`).join("\n")
          : "- No changed files.";
      const diffText = checkpointHit
        ? "\n\nNo new diff since last shown review."
        : includeDiff
        ? diffError
          ? `\n\nGit diff unavailable: ${diffError}`
          : diff
          ? diffBlock(diff)
            : "\n\nNo diff output."
        : "\n\nDiff omitted by request.";
      const analysisText = analysis
        ? `\n\n## Analysis\n\nAffected areas: ${(analysis.affected_areas as string[]).join(", ") || "none"}\nRisks: ${((analysis.risk_signals as Array<{ label?: string }>) ?? []).map((risk) => risk.label).filter(Boolean).join(", ") || "none"}\nRelated tests: ${((analysis.related_tests as Array<{ path?: string }>) ?? []).map((file) => file.path).filter(Boolean).join(", ") || "none"}`
        : "";
      const text = `# Show Changes\n\nWorkspace: ${workspace.root}\n\n## Changed\n\n${changedText}\n\n## Diff stats\n\n+${responseStats.additions} -${responseStats.deletions}${diffText}${analysisText}`;
      return textResult(text, {
        workspace_id: workspace.id,
        root: workspace.root,
        path: args.path ?? "workspace changes",
        status,
        status_error: statusError || undefined,
        diff_error: diffError || undefined,
        changed_files: checkpointHit ? [] : changedFiles,
        staged,
        include_diff: includeDiff,
        additions: responseStats.additions,
        deletions: responseStats.deletions,
        changed: !statusError && (checkpointHit ? false : changedFiles.length > 0 || responseStats.changed),
        diff: responseDiff,
        review_since: since,
        review_marked: checkpointWritten,
        review_checkpoint_hit: checkpointHit,
        ...(analysis ? { analysis } : {})
      });
    }
  );


  ctx.register(

    "commit_changes",
    {
      title: "Commit Changes",
      description:
        "Create a git commit in the workspace. Stages the given paths, or every changed and untracked file when paths is omitted, then commits with the message. Paths blocked by safety rules (secrets, build artifacts) are never staged. Use after show_changes when the user asks for a commit; it does not push. Returns the full commit SHA and a post-commit working-tree status snapshot.",
      inputSchema: {
        workspace_id: workspaceIdSchema(config),
        message: z.string().min(1).max(8_000).describe("Commit message. First line is the subject."),
        paths: z.array(z.string().min(1)).min(1).max(200).optional().describe("Files to stage and commit, relative to the workspace root. Default: every changed and untracked file.")
      },
      annotations: LOCAL_WRITE_ANNOTATIONS
    },
    async (args) => {
      const workspace = workspaces.getWorkspace(args.workspace_id);
      const result = gitCommit(config, guard, workspace, {
        message: String(args.message ?? ""),
        paths: Array.isArray(args.paths) ? args.paths.map((item: unknown) => String(item)) : undefined
      });
      const skipped = result.skipped_blocked.length
        ? `\n\nSkipped ${result.skipped_blocked.length} blocked path${result.skipped_blocked.length === 1 ? "" : "s"} (not staged): ${result.skipped_blocked.join(", ")}`
        : "";
      const verification = result.status_error
        ? `Working-tree status unavailable: ${result.status_error}`
        : result.working_tree_clean
          ? "Working tree: clean."
          : `Working tree: changes remain.\n\n${result.status}`;
      return textResult(`# Commit\n\nCommit: ${result.commit}\nBranch: ${result.branch}\n\n${result.summary}${skipped}\n\n${verification}`, {
        workspace_id: workspace.id,
        root: workspace.root,
        commit: result.commit,
        commit_short: result.commit.slice(0, 12),
        working_tree_clean: result.working_tree_clean,
        status: result.status,
        status_error: result.status_error,
        branch: result.branch,
        files: result.files,
        file_count: result.files.length,
        skipped_blocked_paths: result.skipped_blocked
      });
    }
  );
}
