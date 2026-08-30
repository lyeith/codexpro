#!/usr/bin/env node
import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath, pathToFileURL } from 'node:url';

const projectRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const callerCwd = process.env.CODEXPRO_CALLER_CWD
  ? path.resolve(process.env.CODEXPRO_CALLER_CWD)
  : process.cwd();

function usage() {
  console.log(`CodexPro structural search

Usage:
  codexpro ast-grep --pattern 'console.log($ARG)' --lang ts --path src
  codexpro ast-grep --kind function_declaration --lang ts --path src --json
  codexpro-ast-grep --pattern '$A == null' --root /path/to/repo

Query options:
  --pattern <pattern>         ast-grep structural pattern. Exactly one of pattern/kind.
  --kind <node-kind>         Tree-sitter node kind, for example function_declaration.
  --lang, --language <id>    ast-grep language id such as ts, tsx, js, py, go, rust.
  --selector <node-kind>     Return a sub-node from a pattern match.
  --strictness <mode>        cst|smart|ast|relaxed|signature|template.

Scope and output:
  --root <dir>               Workspace root. Default: caller's current directory.
  --path <path>              Workspace-relative file or directory. Default: .
  --glob <glob>              Include/exclude glob. Repeatable; prefix exclusions with !.
  --include-hidden           Include hidden files that are not safety-blocked.
  --max-results <n>          Matches in this page. Default: configured search limit.
  --context-before <n>       Context lines before each match. Default: 2; max: 20.
  --context-after <n>        Context lines after each match. Default: 2; max: 20.
  --no-group-by-file         Do not merge overlapping context ranges.
  --cursor <cursor>          Continue an identical previous query.
  --timeout-ms <n>           Native process timeout. Default: 15000; max: 60000.
  --json                     Print structured JSON instead of the readable view.
  --help                     Show this help.

The CLI uses the same bounded provider as the MCP ast_grep tool. CLI results do not
establish MCP edit provenance because they are not attached to an authenticated MCP
snapshot store.`);
}

function parseBoolean(value, fallback = true) {
  if (value === undefined) return fallback;
  if (typeof value === 'boolean') return value;
  const normalized = String(value).trim().toLowerCase();
  if (['1', 'true', 'yes', 'y', 'on'].includes(normalized)) return true;
  if (['0', 'false', 'no', 'n', 'off'].includes(normalized)) return false;
  throw new Error(`Invalid boolean value: ${value}`);
}

function parseInteger(value, name, minimum, maximum) {
  if (value === undefined) return undefined;
  const number = Number(value);
  if (!Number.isSafeInteger(number) || number < minimum || number > maximum) {
    throw new Error(`${name} must be an integer from ${minimum} to ${maximum}.`);
  }
  return number;
}

function parseArgs(argv) {
  const args = { globs: [] };
  const valueOption = new Set([
    'pattern', 'kind', 'lang', 'language', 'selector', 'strictness', 'root', 'path',
    'glob', 'globs', 'max-results', 'context-before', 'context-after', 'cursor', 'timeout-ms'
  ]);
  for (let index = 0; index < argv.length; index += 1) {
    const raw = argv[index];
    if (raw === '-h') {
      args.help = true;
      continue;
    }
    if (!raw.startsWith('--')) {
      throw new Error(`Unexpected positional argument: ${raw}`);
    }
    const body = raw.slice(2);
    const equals = body.indexOf('=');
    const key = equals >= 0 ? body.slice(0, equals) : body;
    let value = equals >= 0 ? body.slice(equals + 1) : undefined;
    if (valueOption.has(key) && value === undefined) {
      const next = argv[index + 1];
      if (next === undefined || next.startsWith('--')) throw new Error(`--${key} requires a value.`);
      value = next;
      index += 1;
    }
    if (key === 'glob' || key === 'globs') {
      args.globs.push(String(value));
      continue;
    }
    if (key === 'include-hidden') {
      args.includeHidden = parseBoolean(value, true);
      continue;
    }
    if (key === 'no-include-hidden') {
      args.includeHidden = false;
      continue;
    }
    if (key === 'group-by-file') {
      args.groupByFile = parseBoolean(value, true);
      continue;
    }
    if (key === 'no-group-by-file') {
      args.groupByFile = false;
      continue;
    }
    if (key === 'json') {
      args.json = parseBoolean(value, true);
      continue;
    }
    if (key === 'help') {
      args.help = true;
      continue;
    }
    if (!valueOption.has(key)) throw new Error(`Unknown option: --${key}`);
    args[key] = value;
  }
  return args;
}

function realDirectory(value) {
  const resolved = path.resolve(callerCwd, value || '.');
  const stat = fs.statSync(resolved);
  if (!stat.isDirectory()) throw new Error(`Workspace root is not a directory: ${resolved}`);
  return fs.realpathSync.native(resolved);
}

async function loadRuntime() {
  const paths = ['config.js', 'guard.js', 'astGrepOps.js'].map((file) =>
    pathToFileURL(path.join(projectRoot, 'dist', file)).href
  );
  try {
    const [{ loadConfig }, guardModule, astModule] = await Promise.all(paths.map((url) => import(url)));
    return {
      loadConfig,
      PathGuard: guardModule.PathGuard,
      WorkspaceManager: guardModule.WorkspaceManager,
      astGrepWorkspace: astModule.astGrepWorkspace
    };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    throw new Error(`CodexPro build output is unavailable. Run npm run build or reinstall CodexPro. ${message}`);
  }
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) {
    usage();
    return;
  }
  const pattern = typeof args.pattern === 'string' ? args.pattern : undefined;
  const kind = typeof args.kind === 'string' ? args.kind : undefined;
  if (Boolean(pattern?.trim()) === Boolean(kind?.trim())) {
    throw new Error('Provide exactly one of --pattern or --kind.');
  }
  const root = realDirectory(args.root ?? callerCwd);
  const { loadConfig, PathGuard, WorkspaceManager, astGrepWorkspace } = await loadRuntime();
  const config = loadConfig(['--root', root, '--tool-mode', 'standard', '--bash', 'off', '--write', 'off']);
  const guard = new PathGuard(config);
  const workspace = new WorkspaceManager(config).defaultWorkspace();
  const result = await astGrepWorkspace(config, guard, workspace, {
    pattern,
    kind,
    language: args.language ?? args.lang,
    selector: args.selector,
    strictness: args.strictness,
    root: args.path ?? '.',
    globs: args.globs,
    includeHidden: Boolean(args.includeHidden),
    maxResults: parseInteger(args['max-results'], '--max-results', 1, config.maxSearchResults),
    contextBefore: parseInteger(args['context-before'], '--context-before', 0, 20),
    contextAfter: parseInteger(args['context-after'], '--context-after', 0, 20),
    groupByFile: args.groupByFile !== false,
    cursor: args.cursor,
    timeoutMs: parseInteger(args['timeout-ms'], '--timeout-ms', 1000, 60000)
  });
  if (args.json) {
    const { text: _text, ...structured } = result;
    console.log(JSON.stringify(structured, null, 2));
  } else {
    console.log(result.text);
  }
}

main().catch((error) => {
  const message = error instanceof Error ? error.message : String(error);
  console.error(`CodexPro ast-grep failed: ${message}`);
  process.exitCode = 1;
});
