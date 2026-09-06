import { CodexProError } from "./guard.js";
const OPENAI_SECRET_PATTERN = /\bsk-[A-Za-z0-9_-]{10,}\b/g;
const COMMON_TOKEN_PATTERN = /\b(?:sk-ant-[A-Za-z0-9_-]{10,}|gh[opsru]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|npm_[A-Za-z0-9_-]{20,})\b/g;
const BEARER_TOKEN_PATTERN = /\b(Authorization\s*:\s*Bearer\s+)[A-Za-z0-9._~+/=-]{12,}/gi;
const CLI_TOKEN_PATTERN = /((?:\bngrok\s+config\s+add-authtoken|\bcloudflared\s+service\s+install|--(?:token|access-token|auth-token|api[_-]?key|authtoken))(?:=|\s+))[A-Za-z0-9._~+/=-]{8,}/gi;
const QUERY_TOKEN_PATTERN = /([?&](?:codexpro_token|token|access_token|auth_token|api[_-]?key)=)[^&\s"'`<>]{8,}/gi;
const CODEXPRO_TOKEN_ASSIGNMENT_PATTERN = /\b(codexpro_token\s*=\s*)(?:"[^"\r\n]{8,512}"|'[^'\r\n]{8,512}'|`[^`\r\n]{8,512}`|[A-Za-z0-9_./+=-]{8,512})/gi;
const CODEXPRO_TOKEN_FIELD_PATTERN = /(["']?codexpro_token["']?\s*:\s*)(?:"[^"\r\n]{8,512}"|'[^'\r\n]{8,512}'|`[^`\r\n]{8,512}`|[A-Za-z0-9_./+=-]{8,512})/gi;
const SECRET_ASSIGNMENT_PATTERN = /\b[A-Za-z0-9_]{0,64}(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|PRIVATE[_-]?KEY)[A-Za-z0-9_]{0,64}\s*=\s*(?:"[^"\r\n]{12,512}"|'[^'\r\n]{12,512}'|`[^`\r\n]{12,512}`|[A-Za-z0-9_./+=-]{20,512})/gi;
const SECRET_FIELD_PATTERN = /(["']?[A-Za-z0-9_]{0,64}(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|PRIVATE[_-]?KEY)[A-Za-z0-9_]{0,64}["']?\s*:\s*)(?:"[^"\r\n]{12,512}"|'[^'\r\n]{12,512}'|`[^`\r\n]{12,512}`|[A-Za-z0-9_./+=-]{20,512})/gi;
const SECRET_PATTERNS = [OPENAI_SECRET_PATTERN, COMMON_TOKEN_PATTERN, BEARER_TOKEN_PATTERN, CLI_TOKEN_PATTERN, QUERY_TOKEN_PATTERN, CODEXPRO_TOKEN_ASSIGNMENT_PATTERN, CODEXPRO_TOKEN_FIELD_PATTERN, SECRET_ASSIGNMENT_PATTERN, SECRET_FIELD_PATTERN];

// The generic NAME_TOKEN = "value" patterns also match harmless constants such
// as ACTION_TOKEN = "io.example.widget.TOGGLE". A literal with no digit that is
// built from dotted/underscored/dashed segments is an identifier, path or name,
// not a credential, so it does not trip the write block (output redaction is
// unaffected).
const GENERIC_SECRET_PATTERNS = new Set<RegExp>([SECRET_ASSIGNMENT_PATTERN, SECRET_FIELD_PATTERN]);

function looksLikeIdentifierLiteral(match: string): boolean {
  if (/\d/.test(match)) return false;
  const separator = match.search(/[=:]/);
  const literal = (separator >= 0 ? match.slice(separator + 1) : match).trim().replace(/^["'`]|["'`]$/g, "");
  return /[./:_\-\s]/.test(literal);
}

function secretValueCounts(text: string): Map<string, number> {
  const counts = new Map<string, number>();
  for (const pattern of SECRET_PATTERNS) {
    pattern.lastIndex = 0;
    let match: RegExpExecArray | null;
    while ((match = pattern.exec(text)) !== null) {
      if (isPlaceholderSecret(match[0])) continue;
      if (GENERIC_SECRET_PATTERNS.has(pattern) && looksLikeIdentifierLiteral(match[0])) continue;
      counts.set(match[0], (counts.get(match[0]) ?? 0) + 1);
    }
  }
  return counts;
}

/** Bounded, value-free descriptions of what tripped the secret check (identifier or token family). */
export function describeSecretMatches(text: string, limit = 3): string[] {
  const seen = new Set<string>();
  for (const value of secretValueCounts(text).keys()) {
    const separator = value.search(/[=:]/);
    const description = separator > 0
      ? `${value.slice(0, separator + 1).trim()} "…"`
      : /^sk-/.test(value) ? "an sk-… API key"
        : /^(?:sk-ant-|gh[opsru]_|github_pat_|npm_)/.test(value) ? "a provider token (sk-ant-/gh*_/github_pat_/npm_)"
          : /bearer/i.test(value) ? "an Authorization: Bearer token"
            : "a token-like value";
    seen.add(description.slice(0, 80));
    if (seen.size >= limit) break;
  }
  return [...seen];
}

/** Error for write/edit/patch content that looks like a credential. Explains that this is a content check, not a permission mode. */
export function secretContentBlockedError(operation: "write" | "edit" | "apply_patch", text: string): CodexProError {
  const matches = describeSecretMatches(text);
  return new CodexProError(
    `Refusing to ${operation}: the content contains what looks like a credential (${matches.join("; ") || "a token-like value"}). ` +
      "This is a content check, not a permission problem: the workspace stays writable. " +
      "If it is a real secret, keep it in an untracked env file and reference it. If it is not (an action name, id, or test fixture), " +
      "rename the identifier so it does not end in TOKEN/SECRET/KEY/PASSWORD, or use a placeholder such as [REDACTED_SECRET].",
    { code: "secret_content_blocked", retryUnchanged: false, details: { secret_matches: matches } }
  );
}

export function hasSecretValue(text: string): boolean {
  return secretValueCounts(text).size > 0;
}

export function introducesSecretValue(before: string, after: string): boolean {
  const beforeCounts = secretValueCounts(before);
  for (const [value, count] of secretValueCounts(after)) {
    if (count > (beforeCounts.get(value) ?? 0)) return true;
  }
  return false;
}

export function redactSensitiveText(text: string): string {
  return text
    .replace(CODEXPRO_TOKEN_ASSIGNMENT_PATTERN, (_match, prefix) => `${prefix}[REDACTED_SECRET]`)
    .replace(CODEXPRO_TOKEN_FIELD_PATTERN, (_match, prefix) => `${prefix}[REDACTED_SECRET]`)
    .replace(CLI_TOKEN_PATTERN, (match, prefix) => isPlaceholderSecret(match) ? match : `${prefix}[REDACTED_SECRET]`)
    .replace(SECRET_ASSIGNMENT_PATTERN, (match) => isPlaceholderSecret(match) ? match : redactSecretAssignment(match))
    .replace(SECRET_FIELD_PATTERN, (match, prefix) => isPlaceholderSecret(match) ? match : `${prefix}[REDACTED_SECRET]`)
    .replace(BEARER_TOKEN_PATTERN, (_match, prefix) => `${prefix}[REDACTED_SECRET]`)
    .replace(QUERY_TOKEN_PATTERN, (_match, prefix) => `${prefix}[REDACTED_SECRET]`)
    .replace(OPENAI_SECRET_PATTERN, (match) => isPlaceholderSecret(match) ? match : "[REDACTED_SECRET]")
    .replace(COMMON_TOKEN_PATTERN, (match) => isPlaceholderSecret(match) ? match : "[REDACTED_SECRET]");
}

export function redactStructured<T>(value: T, depth = 0): T {
  if (depth > 8) return value;
  if (typeof value === "string") return redactSensitiveText(value) as T;
  if (!value || typeof value !== "object") return value;
  if (Array.isArray(value)) return value.map((item) => redactStructured(item, depth + 1)) as T;

  const out: Record<string, unknown> = {};
  for (const [key, item] of Object.entries(value)) {
    out[key] = redactStructured(item, depth + 1);
  }
  return out as T;
}

function isPlaceholderSecret(value: string): boolean {
  const normalized = value.toLowerCase();
  return (
    normalized.includes("[redacted_secret]") ||
    normalized.includes("replace-me") ||
    normalized.includes("replace-with-long-random-token") ||
    normalized.includes("keep-this-codexpro-token-stable") ||
    normalized.includes("keep-this-stable-token") ||
    normalized.includes("your-ngrok-token") ||
    normalized.includes("your-token") ||
    normalized.includes("your-api-key-here") ||
    normalized.includes("<openai_api_key>") ||
    normalized.includes("process.env.") ||
    normalized.includes("import.meta.env.") ||
    normalized.includes("os.environ") ||
    normalized.includes("getenv(") ||
    normalized === "sk-..." ||
    normalized.endsWith("=sk-...")
  );
}

function redactSecretAssignment(value: string): string {
  const index = value.indexOf("=");
  if (index < 0) return "[REDACTED_SECRET]";
  return `${value.slice(0, index).trimEnd()}= [REDACTED_SECRET]`;
}
