import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import type { ProjectCatalog, ProjectDefinition } from "./types.js";

const MAX_CATALOG_BYTES = 1_000_000;
const MAX_PROJECTS = 256;
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

function baseRef(value: unknown, label: string): string | undefined {
  if (value === undefined) return undefined;
  if (typeof value !== "string") throw new Error(`${label} must be a string.`);
  const ref = value.trim();
  if (!ref || ref.length > 256 || ref.startsWith("-") || /[\0-\x20\x7f]/.test(ref)) {
    throw new Error(`${label} must be a Git ref without whitespace, control characters, or a leading dash.`);
  }
  return ref;
}

function projectFrom(value: unknown, index: number, catalogDir: string): ProjectDefinition {
  const label = `projects[${index}]`;
  const raw = objectValue(value, label);
  assertKnownKeys(raw, ["id", "label", "root", "baseRef", "maxWorktrees"], label);
  if (typeof raw.id !== "string" || !PROJECT_ID.test(raw.id)) {
    throw new Error(`${label}.id must be 1-64 lowercase letters, numbers, dots, underscores, or dashes.`);
  }
  if (typeof raw.root !== "string" || !raw.root.trim()) throw new Error(`${label}.root must be a non-empty path.`);
  const displayLabel = raw.label === undefined ? raw.id : raw.label;
  if (typeof displayLabel !== "string" || !displayLabel.trim() || displayLabel.trim().length > 120 || /[\0-\x1f\x7f]/.test(displayLabel)) {
    throw new Error(`${label}.label must be 1-120 characters without control characters.`);
  }
  if (raw.maxWorktrees !== undefined && (!Number.isInteger(raw.maxWorktrees) || Number(raw.maxWorktrees) < 1 || Number(raw.maxWorktrees) > 512)) {
    throw new Error(`${label}.maxWorktrees must be an integer from 1 to 512.`);
  }
  return {
    id: raw.id,
    label: displayLabel.trim(),
    root: canonicalDirectory(raw.root, catalogDir, `${label}.root`),
    baseRef: baseRef(raw.baseRef, `${label}.baseRef`),
    maxWorktrees: raw.maxWorktrees === undefined ? undefined : Number(raw.maxWorktrees)
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
  assertKnownKeys(raw, ["version", "defaultProject", "projects"], "Projects file");
  if (raw.version !== 1) throw new Error("Projects file version must be 1.");
  if (!Array.isArray(raw.projects) || raw.projects.length < 1 || raw.projects.length > MAX_PROJECTS) {
    throw new Error(`Projects file must contain 1-${MAX_PROJECTS} projects.`);
  }
  const projects = raw.projects.map((value, index) => projectFrom(value, index, path.dirname(filePath)));
  const ids = new Set<string>();
  const roots = new Set<string>();
  for (const project of projects) {
    if (ids.has(project.id)) throw new Error(`Duplicate project id: ${project.id}`);
    if (roots.has(project.root)) throw new Error(`Duplicate canonical project root: ${project.root}`);
    ids.add(project.id);
    roots.add(project.root);
  }
  const defaultProjectId = raw.defaultProject === undefined ? projects[0].id : raw.defaultProject;
  if (typeof defaultProjectId !== "string" || !ids.has(defaultProjectId)) {
    throw new Error("defaultProject must name one of the configured project ids.");
  }
  return { version: 1, filePath, defaultProjectId, projects };
}

export function singleProjectCatalog(root: string): ProjectCatalog {
  return {
    version: 1,
    defaultProjectId: "default",
    projects: [{ id: "default", label: path.basename(root) || "Default project", root }]
  };
}
