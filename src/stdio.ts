#!/usr/bin/env node
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { loadConfig } from "./config.js";
import { createCodexProServer } from "./server.js";
import { createWorkspaceAccess } from "./workspaceAccess.js";

async function main(): Promise<void> {
  const config = loadConfig();
  const workspaceAccess = await createWorkspaceAccess(config);
  const server = createCodexProServer(config, workspaceAccess);
  const transport = new StdioServerTransport();
  await server.connect(transport);
}

main().catch((error) => {
  console.error(error instanceof Error ? error.stack ?? error.message : String(error));
  process.exit(1);
});
