import fs from "node:fs";
import type { ToolContext } from "./context.js";
import type { RunRecord } from "../work/types.js";
import { workError } from "../work/coordinator.js";

export function validateWorkDocumentReference(ctx: ToolContext): (run: RunRecord, input: string) => string {
  return (run, input) => {
    const workspace = ctx.workspaces.getWorkspace(run.workspace?.id);
    const ref = ctx.guard.resolve(workspace, input);
    if (!fs.statSync(ref.absPath).isFile()) workError("Document reference must name an existing file.");
    return ref.relPath;
  };
}
