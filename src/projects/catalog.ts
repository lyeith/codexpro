import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import type { ProjectCatalog, ProjectCreationRoot, ProjectDefinition } from "./types.js";

export const MAX_CATALOG_BYTES = 1_000_000;
export const MAX_PROJECTS = 256;
export const MAX_CREATION_ROOTS = 256;
const PROJECT_ID = /^[a-z0-9][a-z0-9._-]{0,63}$/;

function expandHome(input: string): string {
  if (!input || input === "~") return os.homedir();
  if (input.startsWith("~/")) return path.join(os.homedir(), input.slice(2));
  return input;
}

function objectValue(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${label} must be a JSON object.`);
  }
  return value as Record<string, unknown>;
}

function assertKnownKeys(value: Record<string, unknown>, allowed: string[], label: string): void {
  const unknown = Object.keys(value).filter((key) => !allowed.includes(key));
  if (unknown.length) throw new Error(`${label} contains unknown field${unknown.length === 1 ? "" : "s"}: ${unknown.join(", ")}`);
}

function canonicalDirectory(input: string, catalogDir: string, label: string): string {
  const expanded = expandHome(input);
  const resolved = path.isAbsolute(expanded) || path.win32.isAbsolute(expanded)
    ? path.resolve(expanded)
    : path.resolve(catalogDir, expanded);
  if (!fs.existsSync(resolved)) throw new Error(`${label} does not exist: ${resolved}`);
  if (!fs.statSync(resolved).isDirectory()) throw new Error(`${label} is not a directory: ${resolved}`);
  return fs.realpathSync(resolved);
}

export function projectIdFrom(value: unknown, label = "project_id"): string {
  if (typeof value !== "string" || !PROJECT_ID.test(value)) {
    throw new Error(`${label} must be 1-64 lowercase letters, numbers, dots, underscores, or dashes.`);
  }
  return value;
}

export function projectLabelFrom(value: unknown, projectId: string, label = "label"): string {
  const displayLabel = value === undefined ? projectId : value;
  if (
    typeof displayLabel !== "string" ||
    !displayLabel.trim() ||
    displayLabel.trim().length > 120 ||
    /[\0-\x1f\x7f]/.test(displayLabel)
  ) {
    throw new Error(`${label} must be 1-120 characters without control characters.`);
  }
  return displayLabel.trim();
}

export function projectBaseRefFrom(value: unknown, label = "base_ref"): string | undefined {
  if (value === undefined) return undefined;
  if (typeof value !== "string") throw new Error(`${label} must be a string.`);
  const ref = value.trim();
  if (!ref || ref.length > 256 || ref.startsWith("-") || /[\0-\x20\x7f]/.test(ref)) {
    throw new Error(`${label} must be a Git ref without whitespace, control characters, or a leading dash.`);
  }
  return ref;
}

export function projectMaxWorktreesFrom(value: unknown, label = "max_worktrees"): number | undefined {
  if (value === undefined) return undefined;
  if (!Number.isInteger(value) || Number(value) < 1 || Number(value) > 512) {
    throw new Error(`${label} must be an integer from 1 to 512.`);
  }
  return Number(value);
}

function projectFrom(value: unknown, index: number, catalogDir: string): ProjectDefinition {
  const label = `projects[${index}]`;
  const raw = objectValue(value, label);
  assertKnownKeys(raw, ["id", "label", "root", "baseRef", "maxWorktrees"], label);
  const id = projectIdFrom(raw.id, `${label}.id`);
  if (typeof raw.root !== "string" || !raw.root.trim()) throw new Error(`${label}.root must be a non-empty path.`);
  return {
    id,
    label: projectLabelFrom(raw.label, id, `${label}.label`),
    root: canonicalDirectory(raw.root, catalogDir, `${label}.root`),
    baseRef: projectBaseRefFrom(raw.baseRef, `${label}.baseRef`),
    maxWorktrees: projectMaxWorktreesFrom(raw.maxWorktrees, `${label}.maxWorktrees`)
  };
}

function creationRootFrom(value: unknown, index: number, catalogDir: string): ProjectCreationRoot {
  const label = `creationRoots[${index}]`;
  const raw = objectValue(value, label);
  assertKnownKeys(raw, ["id", "label", "root"], label);
  const id = projectIdFrom(raw.id, `${label}.id`);
  if (typeof raw.root !== "string" || !raw.root.trim()) throw new Error(`${label}.root must be a non-empty path.`);
  return {
    id,
    label: projectLabelFrom(raw.label, id, `${label}.label`),
    root: canonicalDirectory(raw.root, catalogDir, `${label}.root`)
  };
}

export function loadProjectCatalog(fileInput: string): ProjectCatalog {
  const expanded = path.resolve(expandHome(fileInput));
  if (!fs.existsSync(expanded)) throw new Error(`Projects file does not exist: ${expanded}`);
  if (!fs.statSync(expanded).isFile()) throw new Error(`Projects file is not a file: ${expanded}`);
  const filePath = fs.realpathSync(expanded);
  const stat = fs.statSync(filePath);
  if (stat.size > MAX_CATALOG_BYTES) throw new Error(`Projects file exceeds ${MAX_CATALOG_BYTES} bytes.`);
  let parsed: unknown;
  try {
    parsed = JSON.parse(fs.readFileSync(filePath, "utf8"));
  } catch (error) {
    throw new Error(`Could not parse projects file ${filePath}: ${error instanceof Error ? error.message : String(error)}`);
  }
  const raw = objectValue(parsed, "Projects file");
  assertKnownKeys(raw, ["version", "defaultProject", "projects", "creationRoots"], "Projects file");
  if (raw.version !== 1) throw new Error("Projects file version must be 1.");
  if (!Array.isArray(raw.projects) || raw.projects.length < 1 || raw.projects.length > MAX_PROJECTS) {
    throw new Error(`Projects file must contain 1-${MAX_PROJECTS} projects.`);
  }
  const rawCreationRoots = raw.creationRoots ?? [];
  if (!Array.isArray(rawCreationRoots) || rawCreationRoots.length > MAX_CREATION_ROOTS) {
    throw new Error(`Projects file must contain 0-${MAX_CREATION_ROOTS} creationRoots.`);
  }
  const catalogDir = path.dirname(filePath);
  const projects = raw.projects.map((value, index) => projectFrom(value, index, catalogDir));
  const creationRoots = rawCreationRoots.map((value, index) => creationRootFrom(value, index, catalogDir));
  const ids = new Set<string>();
  const roots = new Set<string>();
  for (const project of projects) {
    if (ids.has(project.id)) throw new Error(`Duplicate project id: ${project.id}`);
    if (roots.has(project.root)) throw new Error(`Duplicate canonical project root: ${project.root}`);
    ids.add(project.id);
    roots.add(project.root);
  }
  for (const creationRoot of creationRoots) {
    if (ids.has(creationRoot.id)) throw new Error(`Duplicate project or creation-root id: ${creationRoot.id}`);
    if (roots.has(creationRoot.root)) throw new Error(`Duplicate canonical project or creation-root path: ${creationRoot.root}`);
    ids.add(creationRoot.id);
    roots.add(creationRoot.root);
  }
  const projectIds = new Set(projects.map((project) => project.id));
  const defaultProjectId = raw.defaultProject === undefined ? projects[0].id : raw.defaultProject;
  if (typeof defaultProjectId !== "string" || !projectIds.has(defaultProjectId)) {
    throw new Error("defaultProject must name one of the configured project ids.");
  }
  return { version: 1, filePath, defaultProjectId, projects, creationRoots };
}

export function singleProjectCatalog(root: string): ProjectCatalog {
  return {
    version: 1,
    defaultProjectId: "default",
    projects: [{ id: "default", label: path.basename(root) || "Default project", root }],
    creationRoots: []
  };
}
