import type { Workspace } from "../guard.js";

export type RunMode = "manual" | "ralph";
export type RunState = "draft" | "provisioning" | "ready" | "active" | "closing" | "waiting" | "recovering" | "verifying" | "blocked" | "paused" | "complete" | "cancelled";
export type IterationState = "active" | "closing" | "completed" | "yielded" | "blocked" | "failed" | "abandoned";
export type OperationState = "prepared" | "running" | "succeeded" | "failed" | "unknown" | "cancelled";
export type TodoState = "pending" | "in_progress" | "blocked" | "done" | "skipped";

export interface Todo {
  id: string;
  title: string;
  status: TodoState;
  acceptance?: string;
  reason?: string;
  evidence_ids: string[];
}

export interface AcceptanceCheck {
  id: string;
  description: string;
  /** Command configured in the specification, never inferred from a checkpoint. */
  command?: string;
  required: boolean;
}

export interface WorkLimits {
  idle_ms: number;
  attempt_ms: number;
  max_attempts: number;
  active_ms: number;
  no_progress_attempts: number;
  continuation_ms: number;
}

export interface SourceSnapshot {
  observed_at: string;
  head?: string;
  branch?: string;
  fingerprint?: string;
  dirty_paths: string[];
  complete: boolean;
  error?: string;
}

export interface Checkpoint {
  id: string;
  revision: number;
  iteration_id: string;
  recorded_at: string;
  summary: string;
  next_action: string;
  blockers: string[];
  decisions: string[];
  failed_approaches: string[];
  evidence_ids: string[];
  source?: SourceSnapshot;
  activity_sequence?: number;
}

export interface RunRecord {
  id: string;
  principal_id: string;
  project_id: string;
  mode: RunMode;
  title: string;
  objective: string;
  scope: string;
  acceptance: AcceptanceCheck[];
  todos: Todo[];
  state: RunState;
  revision: number;
  spec_revision: number;
  plan_revision: number;
  generation: number;
  created_at: string;
  updated_at: string;
  limits: WorkLimits;
  attempt_count: number;
  measured_active_ms: number;
  no_progress_count: number;
  workspace?: Workspace;
  base_ref?: string;
  iteration_id?: string;
  checkpoint?: Checkpoint;
  last_change?: SourceSnapshot;
  recovery_reason?: string;
  recovery_target?: "ready" | "blocked" | "paused" | "cancelled" | "draft";
  pending_finish?: FinishRequest;
  verification?: { source: SourceSnapshot; job_ids: string[]; check_ids: string[]; started_at: string; clock: ClockSample; measured_ms: number; };
  completion?: { completed_at: string; source: SourceSnapshot; evidence_ids: string[]; spec_revision: number; };
}

export interface ClockSample { epoch: string; monotonic_ms: number; wall_ms: number; }
export interface WorkClock { sample(): ClockSample; }

export interface IterationRecord {
  id: string;
  run_id: string;
  principal_id: string;
  generation: number;
  token_hash: string;
  session_id: string;
  phase: "plan" | "execute";
  worker_label: string;
  objective: string;
  todo_ids: string[];
  check_plan: string;
  state: IterationState;
  started_at: string;
  finished_at?: string;
  last_contact_at: string;
  last_progress_at: string;
  progress_measured_ms?: number;
  measured_ms: number;
  idle_ms: number;
  clock: ClockSample;
  clock_gap: boolean;
  start_plan_revision: number;
  baseline_plan_hash?: string;
  end_plan_revision?: number;
  baseline?: SourceSnapshot;
  finish_reason?: string;
}

export interface WorkSession {
  id: string;
  run_id: string;
  principal_id: string;
  token_hash: string;
  measured_ms: number;
  clock_gap: boolean;
  created_at: string;
}

export interface OperationRecord {
  id: string;
  run_id: string;
  iteration_id: string;
  generation: number;
  operation_key: string;
  fingerprint: string;
  tool: string;
  state: OperationState;
  started_at: string;
  finished_at?: string;
  job_ids: string[];
  before?: SourceSnapshot;
  after?: SourceSnapshot;
  result?: unknown;
  error?: string;
  child_ids?: string[];
}

export interface WorkDocument {
  id: string;
  run_id: string;
  kind: "note" | "decision" | "question" | "spec" | "project_memory" | "handoff" | "iteration" | "evidence";
  title: string;
  revision: number;
  content: string;
  bytes: number;
  content_hash: string;
  origin: string;
  created_at: string;
  updated_at: string;
  iteration_id?: string;
  todo_ids: string[];
  supersedes?: string;
  reference?: { path: string; source: SourceSnapshot; };
}

export interface FinishRequest {
  outcome: "completed" | "yielded" | "blocked" | "failed";
  reason?: string;
  await_job_ids?: string[];
  finish_run_if_ready?: boolean;
}

export interface ExecutionEnvelope { attempt_token: string; operation_key?: string; }
export interface ExecutionIdentity { run_id: string; iteration_id: string; generation: number; operation_id: string; deadline_ms: number; deadline_monotonic_ms?: number; }
