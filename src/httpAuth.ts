import { timingSafeEqual } from "node:crypto";
import type { AuthInfo } from "@modelcontextprotocol/sdk/server/auth/types.js";
import { createRemoteJWKSet, jwtVerify, type JWTPayload } from "jose";
import type { CodexProConfig, CloudflareAccessConfig } from "./config.js";

const MAX_ACCESS_ASSERTION_BYTES = 32_768;

export type HttpAuthMethod = "none" | "static-token" | "cloudflare-access";

export interface CloudflareAccessVerifier {
  verify(assertion: string): Promise<AuthInfo>;
}

export interface HttpAuthRequestLike {
  headers: Record<string, unknown>;
  query?: Record<string, unknown>;
}

export type HttpAuthenticationResult =
  | { authenticated: true; method: HttpAuthMethod; authInfo?: AuthInfo }
  | { authenticated: false };

export interface HttpAuthenticator {
  authenticate(request: HttpAuthRequestLike): Promise<HttpAuthenticationResult>;
}

function oneHeader(value: unknown): string | undefined {
  if (typeof value === "string") return value.trim() || undefined;
  if (Array.isArray(value) && value.length === 1 && typeof value[0] === "string") {
    return value[0].trim() || undefined;
  }
  return undefined;
}

function staticCredentialFrom(request: HttpAuthRequestLike): string | undefined {
  const authorization = oneHeader(request.headers.authorization);
  const bearer = authorization?.match(/^Bearer\s+(.+)$/i)?.[1]?.trim();
  if (bearer) return bearer;
  const queryCredential = request.query?.codexpro_token ?? request.query?.token;
  return typeof queryCredential === "string" ? queryCredential : undefined;
}

function credentialMatches(expectedValue: string | undefined, candidate: string | undefined): boolean {
  if (!expectedValue || typeof candidate !== "string") return false;
  const expected = Buffer.from(expectedValue);
  const actual = Buffer.from(candidate);
  return expected.length === actual.length && timingSafeEqual(expected, actual);
}

function scopesFrom(payload: JWTPayload): string[] {
  const raw = payload.scope;
  if (typeof raw === "string") return [...new Set(raw.split(/\s+/).filter(Boolean))];
  if (Array.isArray(raw)) return [...new Set(raw.filter((scope): scope is string => typeof scope === "string" && Boolean(scope)))];
  return [];
}

export function createCloudflareAccessVerifier(config: CloudflareAccessConfig): CloudflareAccessVerifier {
  const jwks = createRemoteJWKSet(new URL(config.jwksUri), {
    timeoutDuration: 5_000,
    cooldownDuration: 30_000,
    cacheMaxAge: 10 * 60_000
  });

  return {
    async verify(assertion: string): Promise<AuthInfo> {
      if (!assertion || Buffer.byteLength(assertion, "utf8") > MAX_ACCESS_ASSERTION_BYTES) {
        throw new Error("Invalid Cloudflare Access assertion.");
      }
      const { payload } = await jwtVerify(assertion, jwks, {
        issuer: config.teamDomain,
        audience: config.audience,
        algorithms: ["RS256"],
        clockTolerance: 5
      });
      const subject = typeof payload.sub === "string" ? payload.sub.trim() : "";
      if (!subject || typeof payload.exp !== "number" || typeof payload.iss !== "string" || payload.type !== "app") {
        throw new Error("Cloudflare Access assertion is missing required application-token claims.");
      }
      const email = typeof payload.email === "string" ? payload.email.trim() : "";
      return {
        token: assertion,
        clientId: "cloudflare-access",
        scopes: scopesFrom(payload),
        expiresAt: payload.exp,
        extra: {
          iss: payload.iss,
          sub: subject,
          auth_method: "cloudflare-access",
          ...(email ? { email } : {})
        }
      };
    }
  };
}

export function staticTokenAuthEnabled(config: CodexProConfig): boolean {
  return (config.authMode === "static-token" || config.authMode === "either") && Boolean(config.authToken);
}

export function cloudflareAccessAuthEnabled(config: CodexProConfig): boolean {
  return config.authMode === "cloudflare-access" || config.authMode === "either";
}

export function httpAuthEnabled(config: CodexProConfig): boolean {
  return staticTokenAuthEnabled(config) || cloudflareAccessAuthEnabled(config);
}

export function httpAuthMethods(config: CodexProConfig): HttpAuthMethod[] {
  const methods: HttpAuthMethod[] = [];
  if (staticTokenAuthEnabled(config)) methods.push("static-token");
  if (cloudflareAccessAuthEnabled(config)) methods.push("cloudflare-access");
  if (!methods.length) methods.push("none");
  return methods;
}

export function createHttpAuthenticator(
  config: CodexProConfig,
  cloudflareVerifier?: CloudflareAccessVerifier
): HttpAuthenticator {
  const acceptsStatic = staticTokenAuthEnabled(config);
  const acceptsCloudflare = cloudflareAccessAuthEnabled(config);
  const verifier = acceptsCloudflare
    ? cloudflareVerifier ?? (config.cloudflareAccess ? createCloudflareAccessVerifier(config.cloudflareAccess) : undefined)
    : undefined;
  if (acceptsCloudflare && !verifier) {
    throw new Error("Cloudflare Access authentication is enabled without verifier configuration.");
  }

  return {
    async authenticate(request: HttpAuthRequestLike): Promise<HttpAuthenticationResult> {
      if (!acceptsStatic && !acceptsCloudflare) {
        return { authenticated: true, method: "none" };
      }
      if (acceptsCloudflare && verifier) {
        const assertion = oneHeader(request.headers["cf-access-jwt-assertion"]);
        if (assertion) {
          try {
            return {
              authenticated: true,
              method: "cloudflare-access",
              authInfo: await verifier.verify(assertion)
            };
          } catch {
            // Authentication failures intentionally collapse to one response so JWT/JWKS
            // diagnostics and claims are not exposed to remote callers.
          }
        }
      }
      if (acceptsStatic && credentialMatches(config.authToken, staticCredentialFrom(request))) {
        // Keep the legacy static-token principal unchanged by omitting AuthInfo. Existing
        // durable worktree leases therefore remain owned by the connector identity. A valid
        // Cloudflare assertion wins when both migration credentials are present.
        return { authenticated: true, method: "static-token" };
      }
      return { authenticated: false };
    }
  };
}
