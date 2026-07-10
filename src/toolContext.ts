import { createHash } from "node:crypto";
import { AsyncLocalStorage } from "node:async_hooks";
import type { RequestHandlerExtra } from "@modelcontextprotocol/sdk/shared/protocol.js";
import type { ServerNotification, ServerRequest } from "@modelcontextprotocol/sdk/types.js";
import type { CodexProConfig } from "./config.js";

export interface ToolCallContext {
  principalId: string;
  requestId: string;
  transportSessionId?: string;
  signal: AbortSignal;
}

const storage = new AsyncLocalStorage<ToolCallContext>();

function digest(value: string): string {
  return createHash("sha256").update(value).digest("hex").slice(0, 32);
}

export function contextFromRequest(
  config: CodexProConfig,
  extra: RequestHandlerExtra<ServerRequest, ServerNotification>
): ToolCallContext {
  const auth = extra.authInfo;
  const subject = typeof auth?.extra?.sub === "string" ? auth.extra.sub.trim() : "";
  const issuer = typeof auth?.extra?.iss === "string" ? auth.extra.iss.trim() : "";
  const principalId = subject
    ? `oauth_${digest(`${issuer}\0${subject}`)}`
    : auth?.clientId
      ? `client_${digest(auth.clientId)}`
      : `connector_${digest(config.defaultRoot)}`;

  return {
    principalId,
    requestId: String(extra.requestId),
    transportSessionId: extra.sessionId,
    signal: extra.signal
  };
}

export function runWithToolContext<T>(context: ToolCallContext, fn: () => T): T {
  return storage.run(context, fn);
}

export function currentToolContext(): ToolCallContext | undefined {
  return storage.getStore();
}
