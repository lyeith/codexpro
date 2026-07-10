import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { createHash } from "node:crypto";
import { CodexProError, isSubpath } from "../guard.js";
import type { RepositoryInfo } from "./types.js";

interface GitResult {
  stdout: string;
  stderr: string;
}

function repositoryId(commonDir: string, scopeRelativePath: string): string {
  return createHash("sha256")
    .update(commonDir)
    .update("\0")
    .update(scopeRelativePath)
    .digest("hex")
    .slice(0, 24);
}

function validateRef(ref: string): string {
  const value = ref.trim();
  if (!value || value.length > 256 || value.startsWith("-") || /[\0-\x20\x7f]/.test(value)) {
    throw new CodexProError("base_ref must be a non-empty Git ref without whitespace, control characters, or a leading dash.");
  }
  return value;
}

export class GitWorktreeDriver {
  constructor(
    private readonly maxOutputBytes = 120_000,
    private readonly timeoutMs = 180_000
  ) {}

  private run(cwd: string, args: string[]): Promise<GitResult> {
    return new Promise((resolve, reject) => {
      const child = spawn("git", args, {
        cwd,
        shell: false,
        env: { ...process.env, NO_COLOR: "1", GIT_TERMINAL_PROMPT: "0" },
        stdio: ["ignore", "pipe", "pipe"]
      });
      let stdout = "";
      let stderr = "";
      let overflow = false;
      let timedOut = false;
      const timer = setTimeout(() => {
        timedOut = true;
        child.kill("SIGTERM");
      }, this.timeoutMs);
      timer.unref();
      const collect = (target: "stdout" | "stderr", chunk: unknown) => {
        const next = (target === "stdout" ? stdout : stderr) + String(chunk);
        if (Buffer.byteLength(next, "utf8") > this.maxOutputBytes) {
          overflow = true;
          child.kill("SIGTERM");
          return;
        }
        if (target === "stdout") stdout = next;
        else stderr = next;
      };
      child.stdout.on("data", (chunk) => collect("stdout", chunk));
      child.stderr.on("data", (chunk) => collect("stderr", chunk));
      child.on("error", (error) => reject(new CodexProError(`Could not run git: ${error.message}`)));
      child.on("close", (code) => {
        clearTimeout(timer);
        if (timedOut) {
          reject(new CodexProError(`Git command timed out after ${this.timeoutMs} ms.`));
          return;
        }
        if (overflow) {
          reject(new CodexProError(`Git output exceeded ${this.maxOutputBytes} bytes.`));
          return;
        }
        if (code !== 0) {
          reject(new CodexProError((stderr || stdout || `git exited with status ${code}`).trim()));
          return;
        }
        resolve({ stdout, stderr });
      });
    });
  }

  async inspectRepository(scopeRoot: string): Promise<RepositoryInfo> {
    const canonicalScope = fs.realpathSync(scopeRoot);
    const topResult = await this.run(canonicalScope, ["rev-parse", "--show-toplevel"]);
    const topLevel = fs.realpathSync(topResult.stdout.trim());
    if (!isSubpath(canonicalScope, topLevel)) {
      throw new CodexProError("Configured workspace is not inside its Git top-level directory.");
    }
    const commonResult = await this.run(topLevel, ["rev-parse", "--git-common-dir"]);
    const rawCommon = commonResult.stdout.trim();
    const commonCandidate = path.isAbsolute(rawCommon) ? rawCommon : path.resolve(topLevel, rawCommon);
    const commonDir = fs.realpathSync(commonCandidate);
    const scopeRelativePath = path.relative(topLevel, canonicalScope);
    return {
      scopeRoot: canonicalScope,
      topLevel,
      commonDir,
      scopeRelativePath,
      repositoryId: repositoryId(commonDir, scopeRelativePath)
    };
  }

  async resolveCommit(repository: RepositoryInfo, ref: string): Promise<string> {
    const safeRef = validateRef(ref);
    const result = await this.run(repository.topLevel, ["rev-parse", "--verify", `${safeRef}^{commit}`]);
    const commit = result.stdout.trim();
    if (!/^[0-9a-f]{40,64}$/i.test(commit)) throw new CodexProError(`Git returned an invalid commit for ${safeRef}.`);
    return commit;
  }

  async create(repository: RepositoryInfo, checkoutRoot: string, branch: string, baseCommit: string): Promise<void> {
    await this.run(repository.topLevel, ["worktree", "add", "-b", branch, checkoutRoot, baseCommit]);
  }

  async head(root: string): Promise<string> {
    const result = await this.run(root, ["rev-parse", "HEAD"]);
    return result.stdout.trim();
  }

  async isDirty(root: string): Promise<boolean> {
    const result = await this.run(root, ["status", "--porcelain=v1", "-z", "--untracked-files=all"]);
    return result.stdout.length > 0;
  }

  async remove(repository: RepositoryInfo, checkoutRoot: string): Promise<void> {
    await this.run(repository.topLevel, ["worktree", "remove", checkoutRoot]);
  }
}
