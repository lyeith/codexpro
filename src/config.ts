import { createHash, randomBytes } from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { DEFAULT_ANALYSIS_LIMITS, type AnalysisLimits } from "./analysis/types.js";
import { loadProjectCatalog, singleProjectCatalog } from "./projects/catalog.js";
import type { ProjectCreationRoot, ProjectDefinition } from "./projects/types.js";

export type BashMode = "off" | "safe" | "full";
export type BashTranscriptMode = "compact" | "full";
export type CodexSessionsMode = "off" | "metadata" | "read";
export type WriteMode = "off" | "handoff" | "workspace";
export type ToolMode = "minimal" | "standard" | "full";
export type WorktreeMode = "off" | "mcp";
export type AuditMode = "off" | "metadata";
export type HttpAuthMode = "static-token" | "cloudflare-access" | "either";
export interface CloudflareAccessConfig {
  teamDomain: string;
  audience: string;
  jwksUri: string;
}
export const MIN_HTTP_TOKEN_BYTES = 24;

export interface CodexProConfig {
  defaultRoot: string;
  allowedRoots: string[];
  projects: ProjectDefinition[];
  projectCreationRoots: ProjectCreationRoot[];
  defaultProjectId: string;
  projectsFile?: string;
  connectorId: string;
  host: string;
  port: number;
  widgetDomain: string;
  authMode: HttpAuthMode;
  authToken?: string;
  cloudflareAccess?: CloudflareAccessConfig;
  requireHttpToken: boolean;
  bashMode: BashMode;
  bashTranscript: BashTranscriptMode;
  bashSessionId?: string;
  requireBashSession: boolean;
  codexSessions: CodexSessionsMode;
  codexDir: string;
  writeMode: WriteMode;
  toolMode: ToolMode;
  exposeAbsolutePaths: boolean;
  inheritEnv: boolean;
  maxReadBytes: number;
  maxWriteBytes: number;
  maxOutputBytes: number;
  maxBashTimeoutMs: number;
  maxImportBytes: number;
  maxSearchResults: number;
  maxHttpSessions: number;
  httpSessionTtlMs: number;
  blockedGlobs: string[];
  contextDir: string;
  toolCards: boolean;
  auditMode: AuditMode;
  auditLogPath: string;
  auditMaxBytes: number;
  auditRetainActions: number;
  connectionTest: boolean;
  analysisEnabled: boolean;
  analysisLimits: AnalysisLimits;
  worktreeMode: WorktreeMode;
  worktreeRoot: string;
  worktreeBaseRef: string;
  maxWorktrees: number;
}

const DEFAULT_BLOCKED_GLOBS = [
  ".git",
  ".git/**",
  "**/.git/**",
  "node_modules",
  "node_modules/**",
  "**/node_modules/**",
  ".env",
  ".env/**",
  ".env.*",
  ".env.*/**",
  "**/.env",
  "**/.env/**",
  "**/.env.*",
  "**/.env.*/**",
  "**/*.pem",
  "**/*.key",
  "**/id_rsa",
  "**/id_rsa.*",
  "**/id_ed25519",
  "**/id_ed25519.*",
  "**/.ssh/**",
  ".codexpro",
  ".codexpro/**",
  "**/.codexpro/**",
  "dist",
  "dist/**",
  "**/dist/**",
  "build",
  "build/**",
  "**/build/**",
  ".next",
  ".next/**",
  "**/.next/**",
  "coverage",
  "coverage/**",
  "**/coverage/**",
  ".cache",
  ".cache/**",
  "**/.cache/**"
];

function parseArgs(argv: string[]): Record<string, string | string[] | boolean> {
  const out: Record<string, string | string[] | boolean> = {};
  for (let i = 0; i < argv.length; i += 1) {
    const raw = argv[i];
    if (!raw.startsWith("--")) continue;
    const withoutPrefix = raw.slice(2);
    const eqIndex = withoutPrefix.indexOf("=");
    let key: string;
    let value: string | boolean;
    if (eqIndex >= 0) {
      key = withoutPrefix.slice(0, eqIndex);
      value = withoutPrefix.slice(eqIndex + 1);
    } else {
      key = withoutPrefix;
      const next = argv[i + 1];
      if (next && !next.startsWith("--")) {
        value = next;
        i += 1;
      } else {
        value = true;
      }
    }

    if (key === "allow-root") {
      const prev = out[key];
      if (Array.isArray(prev)) prev.push(String(value));
      else if (prev) out[key] = [String(prev), String(value)];
      else out[key] = [String(value)];
    } else {
      out[key] = value;
    }
  }
  return out;
}

export function expandHome(input: string): string {
  if (!input || input === "~") return os.homedir();
  if (input.startsWith("~/")) return path.join(os.homedir(), input.slice(2));
  return input;
}

function splitList(value: string | undefined, delimiter: string = path.delimiter): string[] {
  if (!value) return [];
  return value
    .split(delimiter)
    .map((part) => part.trim())
    .filter(Boolean);
}

function splitRoots(value: string | undefined): string[] {
  return splitList(value, path.delimiter);
}

function toRealDir(input: string): string {
  const expanded = expandHome(input);
  const resolved = path.resolve(expanded);
  if (!fs.existsSync(resolved)) {
    throw new Error(`Directory does not exist: ${resolved}`);
  }
  const stat = fs.statSync(resolved);
  if (!stat.isDirectory()) {
    throw new Error(`Not a directory: ${resolved}`);
  }
  return fs.realpathSync.native(resolved);
}

function numberFrom(value: string | undefined, fallback: number, min: number, max: number): number {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) return fallback;
  return Math.max(min, Math.min(max, Math.floor(parsed)));
}

function bashModeFrom(value: string | undefined): BashMode {
  if (value === "off" || value === "safe" || value === "full") return value;
  return "safe";
}

function bashTranscriptFrom(value: string | undefined): BashTranscriptMode {
  if (value === "compact" || value === "full") return value;
  return "compact";
}

function codexSessionsFrom(value: string | undefined): CodexSessionsMode {
  if (value === "metadata" || value === "read") return value;
  if (value === "1" || value === "true" || value === "yes" || value === "on") return "metadata";
  return "off";
}

function bashSessionIdFrom(value: string | undefined): string | undefined {
  const trimmed = value?.trim();
  if (!trimmed) return undefined;
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(trimmed)) {
    throw new Error("CODEXPRO_BASH_SESSION_ID must be 1-64 characters using letters, numbers, dot, underscore, or dash, and must start with a letter or number.");
  }
  return trimmed;
}

function writeModeFrom(value: string | undefined): WriteMode {
  if (value === "off" || value === "handoff" || value === "workspace") return value;
  return "workspace";
}

function toolModeFrom(value: string | undefined): ToolMode {
  if (value === "minimal" || value === "standard" || value === "full") return value;
  return "standard";
}

function worktreeModeFrom(value: string | undefined): WorktreeMode {
  return value === "mcp" ? "mcp" : "off";
}

function auditModeFrom(value: string | undefined): AuditMode {
  const normalized = value?.trim().toLowerCase();
  if (!normalized || normalized === "off" || normalized === "0" || normalized === "false") return "off";
  if (normalized === "metadata" || normalized === "1" || normalized === "true" || normalized === "on") return "metadata";
  throw new Error("CODEXPRO_AUDIT_MODE must be off or metadata.");
}

function escapeGlobLiteral(value: string): string {
  return value.replace(/([*?\[\]{}()!+@])/g, "\\$1");
}

function auditBlockedGlobs(auditLogPath: string, allowedRoots: string[]): string[] {
  const globs = new Set<string>();
  for (const allowedRoot of allowedRoots) {
    const relative = path.relative(allowedRoot, auditLogPath);
    if (relative === "") {
      throw new Error("CODEXPRO_AUDIT_LOG must name a file, not an allowed workspace root.");
    }
    if (relative.startsWith("..") || path.isAbsolute(relative)) continue;
    const normalized = relative.split(path.sep).join("/");
    const escaped = escapeGlobLiteral(normalized);
    globs.add(escaped);
    globs.add(`${escaped}.lock`);
    globs.add(`${escaped}.index.json`);
    globs.add(`${escaped}.index.json.*`);
    globs.add(`${escaped}.compact-*`);
    globs.add(`${escaped}.backup-*`);
  }
  return [...globs];
}

function httpAuthModeFrom(value: string | undefined): HttpAuthMode {
  const mode = value?.trim() || "static-token";
  if (mode === "static-token" || mode === "cloudflare-access" || mode === "either") return mode;
  throw new Error("CODEXPRO_AUTH_MODE must be static-token, cloudflare-access, or either.");
}

function cloudflareTeamDomainFrom(value: string | undefined): string {
  const raw = value?.trim();
  if (!raw) throw new Error("CODEXPRO_CF_ACCESS_TEAM_DOMAIN is required for Cloudflare Access authentication.");
  let parsed: URL;
  try {
    parsed = new URL(raw.includes("://") ? raw : `https://${raw}`);
  } catch {
    throw new Error("CODEXPRO_CF_ACCESS_TEAM_DOMAIN must be a valid HTTPS origin.");
  }
  if (parsed.protocol !== "https:" || parsed.username || parsed.password || parsed.pathname !== "/" || parsed.search || parsed.hash) {
    throw new Error("CODEXPRO_CF_ACCESS_TEAM_DOMAIN must be an HTTPS origin such as https://team.cloudflareaccess.com.");
  }
  return parsed.origin;
}

function cloudflareAudienceFrom(value: string | undefined): string {
  const audience = value?.trim() || "";
  if (!audience || audience.length > 512 || /[\0-\x20\x7f]/.test(audience)) {
    throw new Error("CODEXPRO_CF_ACCESS_AUDIENCE must be a non-empty Access application AUD tag without whitespace or control characters.");
  }
  return audience;
}

function cloudflareJwksUriFrom(value: string | undefined, teamDomain: string): string {
  const raw = value?.trim() || `${teamDomain}/cdn-cgi/access/certs`;
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    throw new Error("CODEXPRO_CF_ACCESS_JWKS_URI must be a valid URL.");
  }
  const loopback = parsed.hostname === "localhost" || parsed.hostname === "127.0.0.1" || parsed.hostname === "[::1]";
  if ((parsed.protocol !== "https:" && !(parsed.protocol === "http:" && loopback)) || parsed.username || parsed.password || parsed.search || parsed.hash) {
    throw new Error("CODEXPRO_CF_ACCESS_JWKS_URI must use HTTPS without credentials, query parameters, or fragments, except that loopback HTTP is allowed for local tests.");
  }
  return parsed.href;
}

function cloudflareAccessConfigFrom(mode: HttpAuthMode): CloudflareAccessConfig | undefined {
  if (mode === "static-token") return undefined;
  const teamDomain = cloudflareTeamDomainFrom(process.env.CODEXPRO_CF_ACCESS_TEAM_DOMAIN);
  return {
    teamDomain,
    audience: cloudflareAudienceFrom(process.env.CODEXPRO_CF_ACCESS_AUDIENCE),
    jwksUri: cloudflareJwksUriFrom(process.env.CODEXPRO_CF_ACCESS_JWKS_URI, teamDomain)
  };
}

function widgetDomainFrom(value: string | undefined): string {
  const raw = value?.trim() || "https://rebel0789.github.io";
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    throw new Error(`CODEXPRO_WIDGET_DOMAIN must be a valid origin URL, got: ${raw}`);
  }
  if (parsed.protocol !== "https:") {
    throw new Error("CODEXPRO_WIDGET_DOMAIN must use https.");
  }
  if (parsed.pathname !== "/" || parsed.search || parsed.hash) {
    throw new Error("CODEXPRO_WIDGET_DOMAIN must be an origin only, for example https://widgets.example.com.");
  }
  return parsed.origin;
}

function contextDirFrom(value: string | undefined): string {
  const raw = (value?.trim() || ".ai-bridge").replaceAll("\\", "/");
  if (path.isAbsolute(raw) || path.win32.isAbsolute(raw)) {
    throw new Error("CODEXPRO_CONTEXT_DIR must be a workspace-relative hidden directory, for example .ai-bridge.");
  }

  const normalized = path.posix.normalize(raw);
  if (!normalized || normalized === "." || normalized === ".." || normalized.startsWith("../")) {
    throw new Error("CODEXPRO_CONTEXT_DIR must stay inside the workspace.");
  }

  const parts = normalized.split("/");
  if (parts.some((part) => !part || part === "." || part === "..")) {
    throw new Error("CODEXPRO_CONTEXT_DIR must be a simple relative directory path.");
  }
  if (!parts[0].startsWith(".")) {
    throw new Error("CODEXPRO_CONTEXT_DIR must start with a hidden directory such as .ai-bridge.");
  }

  const blocked = new Set([".git", ".ssh", ".gnupg", ".cache", "node_modules", "src", "dist", "build", ".next", "coverage"]);
  if (parts.some((part) => blocked.has(part))) {
    throw new Error("CODEXPRO_CONTEXT_DIR cannot point at source, dependency, build, cache, or credential directories.");
  }
  return normalized;
}

function boolFrom(value: string | undefined, fallback = false): boolean {
  if (value === undefined) return fallback;
  return ["1", "true", "yes", "y", "on"].includes(value.toLowerCase());
}

function isLoopbackHost(host: string): boolean {
  return host === "127.0.0.1" || host === "localhost" || host === "::1";
}

function persistentConnectorId(worktreeRoot: string): string {
  fs.mkdirSync(worktreeRoot, { recursive: true, mode: 0o700 });
  const identityPath = path.join(worktreeRoot, "connector-id");
  const readIdentity = (): string => {
    const value = fs.readFileSync(identityPath, "utf8").trim();
    if (!/^[0-9a-f]{48}$/.test(value)) {
      throw new Error(`Invalid persistent connector identity: ${identityPath}`);
    }
    return value;
  };
  if (fs.existsSync(identityPath)) return readIdentity();
  const created = randomBytes(24).toString("hex");
  try {
    fs.writeFileSync(identityPath, `${created}\n`, { mode: 0o600, flag: "wx" });
    return created;
  } catch (error) {
    if (error && typeof error === "object" && "code" in error && error.code === "EEXIST") return readIdentity();
    throw error;
  }
}

export function loadConfig(argv = process.argv.slice(2)): CodexProConfig {
  const args = parseArgs(argv);

  const rootFromArgs = typeof args.root === "string" ? args.root : undefined;
  const projectsFileArg = typeof args["projects-file"] === "string" ? args["projects-file"] : undefined;
  const projectsFileInput = projectsFileArg ?? process.env.CODEXPRO_PROJECTS_FILE;
  if (projectsFileInput && rootFromArgs) {
    throw new Error("--projects-file cannot be combined with --root. Put the desired defaultProject in the catalog.");
  }
  const root = rootFromArgs ?? process.env.CODEXPRO_ROOT ?? process.env.CODEBASE_BRIDGE_REPO_ROOT ?? process.cwd();
  const catalog = projectsFileInput ? loadProjectCatalog(projectsFileInput) : singleProjectCatalog(toRealDir(root));
  const defaultProject = catalog.projects.find((project) => project.id === catalog.defaultProjectId);
  if (!defaultProject) throw new Error(`Default project is missing from the project catalog: ${catalog.defaultProjectId}`);
  const defaultRoot = defaultProject.root;

  const allowRootArgs = Array.isArray(args["allow-root"])
    ? args["allow-root"]
    : typeof args["allow-root"] === "string"
      ? [args["allow-root"]]
      : [];
  const envAllowedRoots = [
    ...splitRoots(process.env.CODEXPRO_ALLOWED_ROOTS),
    ...splitRoots(process.env.CODEBASE_BRIDGE_ALLOWED_ROOTS)
  ];

  const allowHome = process.env.CODEXPRO_ALLOW_HOME === "1" || args["allow-home"] === true;
  const requestedAllowed = [
    ...catalog.projects.map((project) => project.root),
    ...allowRootArgs,
    ...envAllowedRoots,
    ...(allowHome ? [os.homedir()] : [])
  ];
  const allowedRoots = [...new Set(requestedAllowed.map(toRealDir))];

  const portArg = typeof args.port === "string" ? args.port : undefined;
  const hostArg = typeof args.host === "string" ? args.host : undefined;
  const bashArg = typeof args.bash === "string" ? args.bash : undefined;
  const bashTranscriptArg = typeof args["bash-transcript"] === "string" ? args["bash-transcript"] : undefined;
  const bashSessionArg = typeof args["bash-session"] === "string" ? args["bash-session"] : undefined;
  const codexSessionsArg = typeof args["codex-sessions"] === "string" ? args["codex-sessions"] : undefined;
  const codexDirArg = typeof args["codex-dir"] === "string" ? args["codex-dir"] : undefined;
  const requireBashSessionArg =
    args["require-bash-session"] === true
      ? "true"
      : typeof args["require-bash-session"] === "string"
        ? args["require-bash-session"]
        : undefined;
  const writeArg = typeof args.write === "string" ? args.write : undefined;
  const toolModeArg = typeof args["tool-mode"] === "string" ? args["tool-mode"] : undefined;
  const worktreeModeArg = typeof args["worktree-mode"] === "string" ? args["worktree-mode"] : undefined;
  const worktreeRootArg = typeof args["worktree-root"] === "string" ? args["worktree-root"] : undefined;
  const worktreeBaseArg = typeof args["worktree-base"] === "string" ? args["worktree-base"] : undefined;
  const widgetDomainArg = typeof args["widget-domain"] === "string" ? args["widget-domain"] : undefined;
  const auditArg = typeof args.audit === "string" ? args.audit : args.audit === true ? "metadata" : undefined;
  const auditLogArg = typeof args["audit-log"] === "string" ? args["audit-log"] : undefined;
  const auditMaxBytesArg = typeof args["audit-max-bytes"] === "string" ? args["audit-max-bytes"] : undefined;
  const auditRetainActionsArg = typeof args["audit-retain-actions"] === "string" ? args["audit-retain-actions"] : undefined;
  const toolCardsArg =
    args["tool-cards"] === true
      ? "true"
      : typeof args["tool-cards"] === "string"
        ? args["tool-cards"]
        : undefined;
  const extraBlockedGlobs = splitList(process.env.CODEXPRO_BLOCKED_GLOBS, ",");
  const host = hostArg ?? process.env.CODEXPRO_HOST ?? process.env.HOST ?? "127.0.0.1";
  const authMode = httpAuthModeFrom(process.env.CODEXPRO_AUTH_MODE);
  const cloudflareAccess = cloudflareAccessConfigFrom(authMode);
  const configuredCredential = process.env.CODEXPRO_HTTP_TOKEN ?? process.env.CODEBASE_BRIDGE_HTTP_TOKEN;
  const authToken = authMode === "cloudflare-access" ? undefined : configuredCredential;
  if (authToken && Buffer.byteLength(authToken, "utf8") < MIN_HTTP_TOKEN_BYTES) {
    throw new Error(
      `CODEXPRO_HTTP_TOKEN must be at least ${MIN_HTTP_TOKEN_BYTES} bytes. ` +
      "Use `codexpro start` to generate a strong token."
    );
  }
  const allowNoToken = boolFrom(process.env.CODEXPRO_ALLOW_NO_HTTP_TOKEN, false) && isLoopbackHost(host);
  const forceHttpToken = boolFrom(process.env.CODEXPRO_REQUIRE_HTTP_TOKEN, false);
  if (forceHttpToken && authMode === "cloudflare-access") {
    throw new Error("CODEXPRO_REQUIRE_HTTP_TOKEN cannot be combined with CODEXPRO_AUTH_MODE=cloudflare-access. Use either mode during migration.");
  }
  const requireHttpToken =
    forceHttpToken ||
    (authMode === "static-token" && (
      (!authToken && !allowNoToken) ||
      boolFrom(process.env.CODEXPRO_TUNNEL_MODE, false) ||
      (!isLoopbackHost(host) && !allowNoToken)
    ));
  const bashSessionId = bashSessionIdFrom(bashSessionArg ?? process.env.CODEXPRO_BASH_SESSION_ID);
  const requireBashSession = boolFrom(requireBashSessionArg ?? process.env.CODEXPRO_REQUIRE_BASH_SESSION, false);
  if (requireBashSession && !bashSessionId) {
    throw new Error("CODEXPRO_REQUIRE_BASH_SESSION requires CODEXPRO_BASH_SESSION_ID or --bash-session.");
  }

  const codexProHome = expandHome(process.env.CODEXPRO_HOME || path.join(os.homedir(), ".codexpro"));
  const worktreeRoot = path.resolve(
    expandHome(worktreeRootArg || process.env.CODEXPRO_WORKTREE_ROOT || path.join(codexProHome, "worktrees"))
  );
  const auditMode = auditModeFrom(auditArg ?? process.env.CODEXPRO_AUDIT_MODE);
  const auditLogPath = path.resolve(
    expandHome(auditLogArg || process.env.CODEXPRO_AUDIT_LOG || path.join(codexProHome, "audit", "tool-calls.jsonl"))
  );
  const auditMaxBytes = numberFrom(
    auditMaxBytesArg ?? process.env.CODEXPRO_AUDIT_MAX_BYTES,
    64 * 1024 * 1024,
    4 * 1024,
    1024 * 1024 * 1024
  );
  const auditRetainActions = numberFrom(
    auditRetainActionsArg ?? process.env.CODEXPRO_AUDIT_RETAIN_ACTIONS,
    50_000,
    1,
    1_000_000
  );
  const protectedAuditGlobs = auditBlockedGlobs(auditLogPath, allowedRoots);
  const bashMode = bashModeFrom(bashArg ?? process.env.CODEXPRO_BASH_MODE);
  const worktreeMode = worktreeModeFrom(worktreeModeArg ?? process.env.CODEXPRO_WORKTREE_MODE);
  if (worktreeMode === "mcp" && bashMode === "full") {
    throw new Error("CODEXPRO_WORKTREE_MODE=mcp requires bash mode safe or off because full bash can leave the isolated worktree.");
  }

  return {
    defaultRoot,
    allowedRoots,
    projects: catalog.projects,
    projectCreationRoots: catalog.creationRoots,
    defaultProjectId: catalog.defaultProjectId,
    projectsFile: catalog.filePath,
    connectorId: worktreeMode === "mcp"
      ? persistentConnectorId(worktreeRoot)
      : createHash("sha256").update(defaultRoot).digest("hex").slice(0, 48),
    host,
    port: numberFrom(portArg ?? process.env.CODEXPRO_PORT ?? process.env.PORT, 8787, 1, 65535),
    widgetDomain: widgetDomainFrom(widgetDomainArg ?? process.env.CODEXPRO_WIDGET_DOMAIN),
    authMode,
    authToken,
    cloudflareAccess,
    requireHttpToken,
    bashMode,
    bashTranscript: bashTranscriptFrom(bashTranscriptArg ?? process.env.CODEXPRO_BASH_TRANSCRIPT),
    bashSessionId,
    requireBashSession,
    codexSessions: codexSessionsFrom(codexSessionsArg ?? process.env.CODEXPRO_CODEX_SESSIONS),
    codexDir: expandHome(codexDirArg || process.env.CODEXPRO_CODEX_DIR || path.join(os.homedir(), ".codex")),
    writeMode: writeModeFrom(writeArg ?? process.env.CODEXPRO_WRITE_MODE),
    toolMode: toolModeFrom(toolModeArg ?? process.env.CODEXPRO_TOOL_MODE),
    exposeAbsolutePaths: boolFrom(process.env.CODEXPRO_EXPOSE_ABSOLUTE_PATHS, false),
    inheritEnv: process.env.CODEXPRO_INHERIT_ENV === "1",
    maxReadBytes: numberFrom(process.env.CODEXPRO_MAX_READ_BYTES, 180_000, 4_000, 2_000_000),
    maxWriteBytes: numberFrom(process.env.CODEXPRO_MAX_WRITE_BYTES, 1_000_000, 1_000, 10_000_000),
    maxOutputBytes: numberFrom(process.env.CODEXPRO_MAX_OUTPUT_BYTES, 120_000, 4_000, 2_000_000),
    // Default hard cap is 10 minutes. Operators can raise up to 15 minutes.
    maxBashTimeoutMs: numberFrom(process.env.CODEXPRO_MAX_BASH_TIMEOUT_MS, 600_000, 1_000, 900_000),
    maxImportBytes: numberFrom(process.env.CODEXPRO_MAX_IMPORT_BYTES, 5_000_000, 1_000, 50_000_000),
    maxSearchResults: numberFrom(process.env.CODEXPRO_MAX_SEARCH_RESULTS, 200, 5, 2_000),
    maxHttpSessions: numberFrom(process.env.CODEXPRO_MAX_HTTP_SESSIONS, 64, 1, 512),
    httpSessionTtlMs: numberFrom(process.env.CODEXPRO_HTTP_SESSION_TTL_MS, 30 * 60_000, 60_000, 24 * 60 * 60_000),
    blockedGlobs: [...DEFAULT_BLOCKED_GLOBS, ...extraBlockedGlobs, ...protectedAuditGlobs],
    contextDir: contextDirFrom(process.env.CODEXPRO_CONTEXT_DIR),
    toolCards: boolFrom(toolCardsArg ?? process.env.CODEXPRO_TOOL_CARDS, false),
    auditMode,
    auditLogPath,
    auditMaxBytes,
    auditRetainActions,
    connectionTest: boolFrom(process.env.CODEXPRO_CONNECTION_TEST, false),
    analysisEnabled: boolFrom(process.env.CODEXPRO_ANALYSIS, true),
    analysisLimits: {
      maxInventoryFiles: numberFrom(process.env.CODEXPRO_ANALYSIS_MAX_INVENTORY_FILES, DEFAULT_ANALYSIS_LIMITS.maxInventoryFiles, 100, 100_000),
      maxAnalyzedFiles: numberFrom(process.env.CODEXPRO_ANALYSIS_MAX_ANALYZED_FILES, DEFAULT_ANALYSIS_LIMITS.maxAnalyzedFiles, 10, 50_000),
      maxScannedBytes: numberFrom(process.env.CODEXPRO_ANALYSIS_MAX_SCANNED_BYTES, DEFAULT_ANALYSIS_LIMITS.maxScannedBytes, 1_000_000, 512 * 1024 * 1024),
      maxSymbols: numberFrom(process.env.CODEXPRO_ANALYSIS_MAX_SYMBOLS, DEFAULT_ANALYSIS_LIMITS.maxSymbols, 100, 1_000_000),
      maxRelationships: numberFrom(process.env.CODEXPRO_ANALYSIS_MAX_RELATIONSHIPS, DEFAULT_ANALYSIS_LIMITS.maxRelationships, 100, 2_000_000)
    },
    worktreeMode,
    worktreeRoot,
    worktreeBaseRef: (worktreeBaseArg ?? process.env.CODEXPRO_WORKTREE_BASE ?? "HEAD").trim() || "HEAD",
    maxWorktrees: numberFrom(process.env.CODEXPRO_MAX_WORKTREES, 64, 1, 512)
  };
}
