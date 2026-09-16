import { z } from "zod";

export const id = z.string().min(1).max(160);
export const short = z.string().min(1).max(2000);
export const revision = z.number().int().min(1);
export const todo = z.object({ id, title: z.string().min(1).max(500), status: z.enum(["pending", "in_progress", "blocked", "done", "skipped"]), acceptance: short.optional(), reason: short.optional(), evidence_ids: z.array(id).max(30).default([]) });
export const acceptance = z.object({ id, description: short, command: z.string().min(1).max(8000).optional(), required: z.boolean().default(true) });
const notes = z.array(short).max(30);
export const documentFields = {
  document_id: id.optional(), document_revision: revision.optional(), kind: z.enum(["note", "decision", "question", "project_memory"]).optional(),
  title: z.string().min(1).max(300), content: z.string().max(131072),
  reference_path: z.string().min(1).max(2000).optional().describe("Existing workspace file to register with its source observation."), todo_ids: z.array(id).max(100).optional()
};
export const checkpointFields = {
  summary: short, next_action: short, blockers: notes.optional(), decisions: notes.optional(), failed_approaches: notes.optional(), evidence_ids: z.array(id).max(50).optional(),
  todos: z.array(todo).max(200).optional(),
  todo_updates: z.array(todo).max(200).optional().describe("Upsert this page of todos by id, preserving all others. Use instead of todos for larger plans; there is no total-plan count limit."),
  documents: z.array(z.object(documentFields).strict()).max(12).optional().describe("Save up to twelve memory documents atomically with todos and handoff; existing documents require document_revision.")
};
// No run identifiers, credentials, request keys or lifecycle actions belong in
// this payload. The batch supplies its authenticated run and durable key.
export const batchCheckpointSchema = z.object({ expected_revision: revision, ...checkpointFields }).strict();
