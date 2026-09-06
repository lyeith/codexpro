import path from "node:path";
import type { PathGuard } from "../guard.js";
import { normalizeRelPath } from "../guard.js";
import type { ActivityDashboardAction } from "./types.js";

export function escapeHtml(value: unknown): string {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

export function plural(value: number, singular: string, pluralValue = `${singular}s`): string {
  return `${value} ${value === 1 ? singular : pluralValue}`;
}

export function humanBytes(value: number): string {
  const absolute = Math.abs(value);
  if (absolute < 1024) return `${value} B`;
  if (absolute < 1024 * 1024) return `${(value / 1024).toFixed(absolute < 10 * 1024 ? 1 : 0)} KiB`;
  return `${(value / (1024 * 1024)).toFixed(absolute < 10 * 1024 * 1024 ? 1 : 0)} MiB`;
}

export function humanDuration(value: number): string {
  if (value < 1000) return `${value} ms`;
  if (value < 60_000) return `${(value / 1000).toFixed(value < 10_000 ? 1 : 0)} s`;
  return `${(value / 60_000).toFixed(1)} min`;
}

export function statusTone(status: ActivityDashboardAction["status"]): string {
  if (status === "succeeded") return "good";
  if (status === "cancelled" || status === "blocked") return "warn";
  return "bad";
}

export function unique(values: string[]): string[] {
  return [...new Set(values)];
}

export function normalizeGitPath(value: string): string {
  return normalizeRelPath(value).replace(/^\.\//, "");
}

export function isSafeDashboardPath(guard: PathGuard, value: string): boolean {
  const normalized = normalizeGitPath(value);
  return Boolean(
    normalized &&
    normalized !== "." &&
    !path.isAbsolute(normalized) &&
    !normalized.startsWith("../") &&
    !guard.isBlockedRelativePath(normalized)
  );
}
