import { createHash, createHmac, randomBytes, randomUUID, timingSafeEqual } from "node:crypto";
import fsp from "node:fs/promises";
import path from "node:path";
import type { CodexProConfig } from "./config.js";
import { codexProHome, readRuntimeConnection } from "./profileStore.js";
import { CodexProError, displayPath, isSubpath, type PathGuard, type Workspace } from "./guard.js";

const ASSET_INDEX_VERSION = 1;
const ASSET_ID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const PROCESS_ASSET_SECRET = randomBytes(32).toString("base64url");
const IMAGE_MIME_TYPES = new Set(["image/png", "image/jpeg", "image/webp", "image/gif"]);
const BLOCKED_BINARY_MIME_TYPES = new Set(["text/html", "application/xhtml+xml", "image/svg+xml"]);

export type AssetKind = "image" | "binary";

export interface DownloadAssetOptions {
  path: string;
  kind?: AssetKind;
  filenameHint?: string;
  maxBytes?: number;
  ttlSeconds?: number;
}

export interface AssetMetadata {
  version: number;
  id: string;
  createdAt: string;
  workspaceRoot: string;
  assetDir: string;
  relPath: string;
  absPath: string;
  filename: string;
  kind: AssetKind;
  mimeType: string;
  bytes: number;
  sha256: string;
  sourcePath: string;
}

export interface DownloadAssetResult {
  asset_id: string;
  workspace_id: string;
  path: string;
  filename: string;
  kind: AssetKind;
  mime_type: string;
  bytes: number;
  sha256: string;
  source_path: string;
  asset_url: string;
  relative_url: string;
  expires_at: string;
  expires_unix: number;
}

export interface ResolvedAsset {
  metadata: AssetMetadata;
  absPath: string;
  mimeType: string;
  contentDisposition: string;
}

export class AssetAccessError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
    this.name = "AssetAccessError";
  }
}

function assetIndexDir(): string {
  return path.join(codexProHome(), "assets");
}

function assetIndexPath(assetId: string): string {
  return path.join(assetIndexDir(), `${assetId}.json`);
}

function signingSecret(config: CodexProConfig): string {
  return process.env.CODEXPRO_ASSET_SECRET || config.authToken || PROCESS_ASSET_SECRET;
}

function signAsset(config: CodexProConfig, assetId: string, expiresUnix: number, sha256: string): string {
  return createHmac("sha256", signingSecret(config))
    .update(`${assetId}.${expiresUnix}.${sha256}`)
    .digest("base64url");
}

function constantTimeMatches(a: string, b: string): boolean {
  const left = Buffer.from(a);
  const right = Buffer.from(b);
  return left.length === right.length && timingSafeEqual(left, right);
}

function safeFilename(value: string | undefined, fallback: string): string {
  const base = path.basename(String(value ?? "")).replace(/\.[A-Za-z0-9]{1,12}$/, "");
  const clean = base
    .replace(/[^A-Za-z0-9._-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 80);
  return clean || fallback;
}

function extensionForMime(mimeType: string): string {
  if (mimeType === "image/png") return "png";
  if (mimeType === "image/jpeg") return "jpg";
  if (mimeType === "image/webp") return "webp";
  if (mimeType === "image/gif") return "gif";
  if (mimeType === "application/pdf") return "pdf";
  if (mimeType === "application/zip") return "zip";
  if (mimeType === "application/gzip") return "gz";
  return "bin";
}

function sniffMime(buffer: Buffer): string | undefined {
  if (buffer.length >= 8 && buffer.subarray(0, 8).equals(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]))) {
    return "image/png";
  }
  if (buffer.length >= 3 && buffer[0] === 0xff && buffer[1] === 0xd8 && buffer[2] === 0xff) {
    return "image/jpeg";
  }
  if (buffer.length >= 6 && (buffer.subarray(0, 6).toString("ascii") === "GIF87a" || buffer.subarray(0, 6).toString("ascii") === "GIF89a")) {
    return "image/gif";
  }
  if (buffer.length >= 12 && buffer.subarray(0, 4).toString("ascii") === "RIFF" && buffer.subarray(8, 12).toString("ascii") === "WEBP") {
    return "image/webp";
  }
  if (buffer.length >= 5 && buffer.subarray(0, 5).toString("ascii") === "%PDF-") {
    return "application/pdf";
  }
  if (buffer.length >= 4 && buffer[0] === 0x50 && buffer[1] === 0x4b && buffer[2] === 0x03 && buffer[3] === 0x04) {
    return "application/zip";
  }
  if (buffer.length >= 2 && buffer[0] === 0x1f && buffer[1] === 0x8b) {
    return "application/gzip";
  }
  const sample = buffer.subarray(0, Math.min(buffer.length, 512)).toString("utf8").trimStart().toLowerCase();
  if (sample.startsWith("<!doctype html") || sample.startsWith("<html")) return "text/html";
  if (sample.startsWith("<svg") || sample.startsWith("<?xml") && sample.includes("<svg")) return "image/svg+xml";
  return undefined;
}

function assertAssetMime(kind: AssetKind, mimeType: string, sniffedMime: string | undefined): void {
  if (kind === "image") {
    if (!sniffedMime || !IMAGE_MIME_TYPES.has(mimeType)) {
      throw new CodexProError(
        `Imported asset is not a supported raster image (${mimeType || "unknown"}). Supported image types: png, jpeg, webp, gif.`
      );
    }
    return;
  }
  if (BLOCKED_BINARY_MIME_TYPES.has(mimeType)) {
    throw new CodexProError(`Refusing to cache active or document-like content as a binary asset: ${mimeType}.`);
  }
}

async function readWorkspaceAsset(
  guard: PathGuard,
  workspace: Workspace,
  inputPath: string,
  maxBytes: number
): Promise<{ buffer: Buffer; sourcePath: string }> {
  const resolved = guard.resolve(workspace, inputPath);
  const stat = await fsp.stat(resolved.absPath);
  if (!stat.isFile()) throw new CodexProError(`Not a file: ${resolved.relPath}`);
  if (stat.size > maxBytes) {
    throw new CodexProError(`Asset is too large (${stat.size} bytes). Limit: ${maxBytes} bytes.`);
  }
  return { buffer: await fsp.readFile(resolved.absPath), sourcePath: resolved.relPath };
}

async function writeAssetIndex(metadata: AssetMetadata): Promise<void> {
  await fsp.mkdir(assetIndexDir(), { recursive: true, mode: 0o700 });
  const indexPath = assetIndexPath(metadata.id);
  await fsp.writeFile(indexPath, `${JSON.stringify(metadata, null, 2)}\n`, { mode: 0o600 });
  try {
    await fsp.chmod(indexPath, 0o600);
  } catch {
    // Best-effort permission repair for filesystems that support chmod.
  }
}

async function readAssetIndex(assetId: string): Promise<AssetMetadata> {
  if (!ASSET_ID_PATTERN.test(assetId)) throw new AssetAccessError(404, "Asset not found.");
  let raw: unknown;
  try {
    raw = JSON.parse(await fsp.readFile(assetIndexPath(assetId), "utf8"));
  } catch {
    throw new AssetAccessError(404, "Asset not found.");
  }
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    throw new AssetAccessError(404, "Asset metadata is invalid.");
  }
  const metadata = raw as AssetMetadata;
  if (metadata.version !== ASSET_INDEX_VERSION || metadata.id !== assetId || typeof metadata.absPath !== "string") {
    throw new AssetAccessError(404, "Asset metadata is invalid.");
  }
  return metadata;
}

function assetBaseUrl(config: CodexProConfig): string {
  if (config.assetBaseUrl) return config.assetBaseUrl;
  const runtime = readRuntimeConnection(config.defaultRoot);
  const endpoint = typeof runtime.endpoint === "string" && runtime.endpoint ? runtime.endpoint : "";
  const localBase = typeof runtime.localBase === "string" && runtime.localBase ? runtime.localBase : "";
  const raw = endpoint || localBase || `http://${config.host}:${config.port}`;
  const url = new URL(raw);
  url.search = "";
  url.hash = "";
  const cleanPath = url.pathname.replace(/\/+$/, "");
  if (cleanPath.endsWith("/mcp")) {
    url.pathname = cleanPath.slice(0, -"/mcp".length) || "/";
  }
  return url.toString().replace(/\/$/, "");
}

function signedAssetRoute(config: CodexProConfig, metadata: AssetMetadata, ttlSeconds?: number): { route: string; expiresUnix: number; expiresAt: string } {
  const ttl = Math.max(60, Math.min(24 * 60 * 60, Math.floor(ttlSeconds ?? config.assetTtlSeconds)));
  const expiresUnix = Math.floor(Date.now() / 1000) + ttl;
  const signature = signAsset(config, metadata.id, expiresUnix, metadata.sha256);
  const route = `/assets/${metadata.id}?expires=${expiresUnix}&sig=${encodeURIComponent(signature)}`;
  return { route, expiresUnix, expiresAt: new Date(expiresUnix * 1000).toISOString() };
}

function contentDisposition(metadata: AssetMetadata): string {
  const disposition = metadata.kind === "image" && IMAGE_MIME_TYPES.has(metadata.mimeType) ? "inline" : "attachment";
  const filename = metadata.filename.replace(/["\\\r\n]/g, "_");
  return `${disposition}; filename="${filename}"`;
}

export async function downloadAsset(
  config: CodexProConfig,
  guard: PathGuard,
  workspace: Workspace,
  options: DownloadAssetOptions
): Promise<DownloadAssetResult> {
  const requestedMaxBytes = Number(options.maxBytes);
  const maxBytes = Number.isFinite(requestedMaxBytes)
    ? Math.max(1, Math.min(Math.floor(requestedMaxBytes), config.maxAssetBytes))
    : config.maxAssetBytes;
  const kind: AssetKind = options.kind === "binary" ? "binary" : "image";
  const imported = await readWorkspaceAsset(guard, workspace, options.path, maxBytes);
  const sniffedMime = sniffMime(imported.buffer);
  const mimeType = sniffedMime || "application/octet-stream";
  assertAssetMime(kind, mimeType, sniffedMime);

  const assetId = randomUUID();
  const ext = extensionForMime(mimeType);
  const filenameBase = safeFilename(options.filenameHint ?? imported.sourcePath, assetId);
  const filename = `${filenameBase}.${ext}`;
  const relPath = `${config.assetDir}/${assetId}.${ext}`;
  const resolved = guard.resolve(workspace, relPath, { forWrite: true });
  await fsp.mkdir(path.dirname(resolved.absPath), { recursive: true, mode: 0o700 });
  await fsp.writeFile(resolved.absPath, imported.buffer, { mode: 0o600 });
  try {
    await fsp.chmod(resolved.absPath, 0o600);
  } catch {
    // Best-effort permission repair for filesystems that support chmod.
  }

  const sha = createHash("sha256").update(imported.buffer).digest("hex");
  const metadata: AssetMetadata = {
    version: ASSET_INDEX_VERSION,
    id: assetId,
    createdAt: new Date().toISOString(),
    workspaceRoot: workspace.root,
    assetDir: config.assetDir,
    relPath: resolved.relPath,
    absPath: resolved.absPath,
    filename,
    kind,
    mimeType,
    bytes: imported.buffer.byteLength,
    sha256: sha,
    sourcePath: imported.sourcePath
  };
  await writeAssetIndex(metadata);

  const signed = signedAssetRoute(config, metadata, options.ttlSeconds);
  return {
    asset_id: assetId,
    workspace_id: workspace.id,
    path: resolved.relPath,
    filename,
    kind,
    mime_type: mimeType,
    bytes: metadata.bytes,
    sha256: sha,
    source_path: metadata.sourcePath,
    asset_url: `${assetBaseUrl(config)}${signed.route}`,
    relative_url: signed.route,
    expires_at: signed.expiresAt,
    expires_unix: signed.expiresUnix
  };
}

export async function resolveSignedAsset(
  config: CodexProConfig,
  assetId: string,
  expiresValue: unknown,
  signatureValue: unknown
): Promise<ResolvedAsset> {
  const expiresUnix = Number(String(expiresValue ?? ""));
  const signature = typeof signatureValue === "string" ? signatureValue : "";
  if (!Number.isFinite(expiresUnix) || expiresUnix <= 0 || !signature) {
    throw new AssetAccessError(403, "Invalid asset signature.");
  }
  if (Math.floor(Date.now() / 1000) > expiresUnix) {
    throw new AssetAccessError(410, "Asset URL has expired.");
  }

  const metadata = await readAssetIndex(assetId);
  const expected = signAsset(config, assetId, Math.floor(expiresUnix), metadata.sha256);
  if (!constantTimeMatches(signature, expected)) {
    throw new AssetAccessError(403, "Invalid asset signature.");
  }

  let realWorkspace: string;
  let realAsset: string;
  let realAssetDir: string;
  try {
    realWorkspace = await fsp.realpath(metadata.workspaceRoot);
    realAsset = await fsp.realpath(metadata.absPath);
    realAssetDir = await fsp.realpath(path.join(realWorkspace, metadata.assetDir));
  } catch {
    throw new AssetAccessError(404, "Asset not found.");
  }

  if (!config.allowedRoots.some((allowedRoot) => isSubpath(realWorkspace, allowedRoot))) {
    throw new AssetAccessError(403, "Asset workspace is outside allowed roots.");
  }
  if (!isSubpath(realAsset, realWorkspace) || !isSubpath(realAsset, realAssetDir)) {
    throw new AssetAccessError(403, "Asset path is outside the asset cache.");
  }
  if (displayPath(realAsset, realWorkspace) !== metadata.relPath) {
    throw new AssetAccessError(403, "Asset path metadata does not match the resolved path.");
  }

  const stat = await fsp.stat(realAsset);
  if (!stat.isFile()) throw new AssetAccessError(404, "Asset not found.");
  if (stat.size !== metadata.bytes) throw new AssetAccessError(410, "Asset has changed since signing.");
  const buffer = await fsp.readFile(realAsset);
  const actualSha = createHash("sha256").update(buffer).digest("hex");
  if (actualSha !== metadata.sha256) throw new AssetAccessError(410, "Asset has changed since signing.");

  return {
    metadata,
    absPath: realAsset,
    mimeType: metadata.mimeType,
    contentDisposition: contentDisposition(metadata)
  };
}
