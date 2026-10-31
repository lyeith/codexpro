#!/usr/bin/env node
// Evaluate the exact reference policy through Node's native TypeScript support.
// No Go policy or expected-name list participates in oracle generation.
import fs from 'node:fs';
import path from 'node:path';
import {createHash} from 'node:crypto';
import {pathToFileURL} from 'node:url';

if (process.argv.length !== 4) throw new Error('usage: capture_registry_capabilities.mjs SOURCE.ts OUTPUT.json');
const source = path.resolve(process.argv[2]);
const output = path.resolve(process.argv[3]);
const bytes = fs.readFileSync(source);
const sourceHash = createHash('sha256').update(bytes).digest('hex');
if (sourceHash !== '12bc54021a1ba8282e886b01e4d753bfac9c27adb2a53f3647151a485d4b795c') {
  throw new Error('reference source differs from the verified44bb76b registry');
}
const registry = await import(pathToFileURL(source).href);
const cases = [];
for (const worktreeMode of ['off', 'mcp']) {
  for (const toolMode of ['minimal', 'standard', 'full']) {
    for (const count of [0, 1, 2]) {
      for (const analysisEnabled of [false, true]) {
        for (const auditMode of ['off', 'metadata']) {
          for (const connectionTest of [false, true]) {
            const options = {worktree_mode: worktreeMode, tool_mode: toolMode, project_count: count,
              analysis_enabled: analysisEnabled, audit_mode: auditMode, connection_test: connectionTest};
            const config = {worktreeMode, toolMode, analysisEnabled, auditMode, connectionTest,
              projects: Array.from({length: count}, (_, i) => ({id: 'fixture-' + i})),
              projectsFile: '/private/reference-projects.json', writeMode: 'workspace', bashMode: 'full',
              handoffMode: 'off', codexSessions: 'off', work: {enabled: true}};
            cases.push({options, source_tool_names: registry.toolNamesForMode(config)});
          }
        }
      }
    }
  }
}
if (cases.length !== 144) throw new Error('incomplete reference capability matrix');
const oracle = {schema_version: 1, source_commit: '44bb76bfa671e4aad540a8976f0fa6e6d5c2507a',
  source_file: 'src/tools/registry.ts', source_sha256: sourceHash, node_version: process.version,
  generated_by: 'unmodified reference toolNamesForMode through native TypeScript import',
  fixed_config: {writeMode: 'workspace', bashMode: 'full', projectsFile: 'configured', handoffMode: 'off',
    codexSessions: 'off', work: {enabled: true}},
  approval_note: 'Source work is enabled to retain work_status as historical read. Go tests apply the eleven APPROVAL.md retirements independently.',
  descriptors: registry.TOOL_DESCRIPTORS, cases};
fs.writeFileSync(output, JSON.stringify(oracle, null, 2) + '\n', {flag: 'wx', mode: 0o600});
console.log(JSON.stringify({cases: cases.length, descriptors: Object.keys(registry.TOOL_DESCRIPTORS).length,
  source_sha256: sourceHash, output_sha256: createHash('sha256').update(fs.readFileSync(output)).digest('hex')}));
