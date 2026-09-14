#!/usr/bin/env node
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createHash, randomUUID } from 'node:crypto';
import { pathToFileURL } from 'node:url';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

function option(argv, name, fallback) { const i = argv.indexOf(`--${name}`); const inline = argv.find(a => a.startsWith(`--${name}=`)); return inline ? inline.slice(name.length + 3) : i >= 0 ? argv[i + 1] : fallback; }
const quote = value => `'${String(value).replaceAll("'", "'\\''")}'`;
export async function withWorkClient(argv, operation) {
  const input = option(argv, 'mcp-url', process.env.CODEXPRO_MCP_URL);
  if (!input) throw new Error('Provide --mcp-url or CODEXPRO_MCP_URL for the existing coordinator. This command never starts a second server.');
  const url = new URL(input); if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) throw new Error('Invalid MCP URL.');
  const token = process.env.CODEXPRO_HTTP_TOKEN;
  if (token && url.protocol !== 'https:' && !['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname)) throw new Error('Use HTTPS for a remote authenticated MCP endpoint.');
  const client = new Client({ name: 'codexpro-work-cli', version: '1' });
  const transport = new StreamableHTTPClientTransport(url, { requestInit: { redirect: 'error', ...(token ? { headers: { Authorization: `Bearer ${token}` } } : {}) } });
  try { await client.connect(transport);
    const call = async (name, args) => { const reply = await client.callTool({ name, arguments: args }, undefined, { timeout: 360_000 });
      if (reply.isError) throw new Error(reply.content?.filter(c => c.type === 'text').map(c => c.text).join('\n') ?? 'MCP action failed'); return reply.structuredContent; };
    return await operation(call);
  } finally { await client.close(); }
}
export async function runWorkCommand(argv) {
  if (!argv.length || argv.includes('--help')) { console.log('codexpro work status|manage|claim|update --mcp-url URL --args-file FILE|-\nJSON arguments use the corresponding work_* schema. Authentication: CODEXPRO_HTTP_TOKEN. Replies are JSON; claim replies contain private credentials.'); return; }
  const name = `work_${argv[0]}`; if (!['work_status', 'work_manage', 'work_claim', 'work_update'].includes(name)) throw new Error('Expected status, manage, claim or update.');
  const file = option(argv, 'args-file'); const raw = file ? fs.readFileSync(file === '-' ? 0 : file, 'utf8') : '{}';
  if (Buffer.byteLength(raw) > 256 * 1024) throw new Error('Arguments exceed 256 KiB.');
  const args = JSON.parse(raw); console.log(JSON.stringify(await withWorkClient(argv, call => call(name, args)), null, 2));
}

/** Existing executor/reviewer loop runs INSIDE one supervised server job. The
 * launcher only handles claims/checkpoints; launcher death cannot orphan ownership. */
export async function runManagedLoop(argv) {
  const runId = option(argv, 'run-id'); if (!runId) throw new Error('Managed loop requires --run-id.');
  const key = option(argv, 'claim-key', randomUUID());
  const forwarded = []; const adapterOptions = new Set(['--run-id', '--mcp-url', '--claim-key', '--session-token', '--todo-ids']);
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i].split('=')[0];
    if (adapterOptions.has(flag)) { if (!argv[i].includes('=')) i++; continue; }
    if (['--root', '--context-dir', '--timeout', '--timeout-ms'].includes(flag)) throw new Error(`${flag} is controlled by the managed workspace and server deadline.`);
    forwarded.push(argv[i]);
  }
  return withWorkClient(argv, async rawCall => {
    const cacheRoot = path.resolve(process.env.CODEXPRO_HOME ?? path.join(os.homedir(), '.codexpro'), 'work-clients');
    const identity = createHash('sha256').update(JSON.stringify([option(argv, 'mcp-url', process.env.CODEXPRO_MCP_URL), runId, key])).digest('hex');
    const cachePath = path.join(cacheRoot, `${identity}.json`);
    const signature = createHash('sha256').update(JSON.stringify([forwarded.filter(a => a !== '--dry-run'), option(argv, 'todo-ids'), option(argv, 'session-token')])).digest('hex');
    let cache = { signature, calls: {} };
    if (fs.existsSync(cachePath)) { if (fs.statSync(cachePath).size > 1024 * 1024) throw new Error('CLI receipt file is oversized. Inspect it before resuming.'); cache = JSON.parse(fs.readFileSync(cachePath, 'utf8')); }
    if (cache.signature !== signature) throw new Error('This --claim-key was used with different launcher arguments. Use a fresh key for a different packet.');
    const save = () => { if (argv.includes('--dry-run')) return; fs.mkdirSync(cacheRoot, { recursive: true, mode: 0o700 }); const temporary = `${cachePath}.${process.pid}.tmp`; fs.writeFileSync(temporary, JSON.stringify(cache), { mode: 0o600 }); fs.renameSync(temporary, cachePath); };
    const call = async (name, args) => {
      const key = args.action !== 'heartbeat' ? args.request_key ?? args.execution?.operation_key : undefined;
      if (!key) return rawCall(name, args);
      const callKey = `${name}:${key}`;
      const request = cache.calls[callKey] ??= { args }; save();
      if (request.reply) return request.reply;
      const reply = await rawCall(name, request.args); request.reply = reply; save(); return reply;
    };
    if (cache.final && !argv.includes('--dry-run')) { console.log(JSON.stringify(cache.final, null, 2)); if (cache.failed) process.exitCode = 1; return; }
    if (cache.finish && !argv.includes('--dry-run')) {
      const finish = await call('work_update', cache.finish);
      const claimed = cache.calls[`work_claim:${key}`].reply;
      cache.final = { ...finish, session_token: claimed.session_token }; save(); console.log(JSON.stringify(cache.final, null, 2)); if (cache.failed) process.exitCode = 1; return;
    }
    const read = (section, offset = 0) => call('work_status', { action: 'get', run_id: runId, section, offset, limit: 10 });
    const summary = await read('summary');
    let todos = [], offset = 0;
    do { const page = await read('todos', offset); if (page.return_size?.truncated) throw new Error('Todo page was truncated; narrow the plan before using this adapter.'); todos.push(...page.items); offset = page.next_offset; } while (offset !== null);
    const selected = cache.selected ?? option(argv, 'todo-ids')?.split(',') ?? todos.filter(t => t.status === 'pending').slice(0, 1).map(t => t.id);
    if (!selected.length) throw new Error('No packet selected. Inspect work_status or request finish_run.');
    if (argv.includes('--dry-run')) { console.log(JSON.stringify({ run_id: runId, selected, launcher: 'server-supervised legacy loop', command: ['codexpro', 'loop-handoff', '--root', '.', ...forwarded], mutates: false }, null, 2)); return; }
    cache.selected = selected; cache.todos ??= todos; todos = cache.todos; save();
    const claimed = await call('work_claim', { run_id: runId, expected_revision: summary.revision, request_key: key, phase: 'execute', worker_label: 'loop-handoff CLI',
      objective: selected.map(id => todos.find(t => t.id === id)?.title ?? id).join('; ').slice(0, 2000), todo_ids: selected, check_plan: 'Run the configured executor, tests and reviewer; final run acceptance remains server-owned.', session_token: option(argv, 'session-token') });
    const envelope = { attempt_token: claimed.attempt_token, operation_key: `${claimed.iteration_id}:legacy-loop-job` };
    const contextDir = claimed.context_dir ?? '.ai-bridge';
    const docs = []; offset = 0;
    do { const page = await read('documents', offset); if (page.return_size?.truncated) throw new Error('Document manifest was truncated; use the MCP tools to select a narrower packet.'); docs.push(...page.items); offset = page.next_offset; } while (offset !== null);
    let background = '';
    for (const doc of docs.filter(d => ['spec', 'handoff'].includes(d.kind))) {
      let position = 0, content = '';
      do { const page = await call('work_status', { action: 'read_document', run_id: runId, document_id: doc.id, document_revision: doc.revision, offset: position, max_bytes: 2000 });
        if (page.return_size?.truncated) throw new Error('Document page was unexpectedly truncated.'); content += page.content; position = page.next_offset;
      } while (position !== null && content.length < 16000);
      background += `\n## ${doc.kind}: ${doc.id} revision ${doc.revision}\n${content}${position !== null ? '\n[Remaining document omitted from this projection.]' : ''}\n`;
    }
    const plan = `# Claimed packet\n\nRun ${runId}\n\n${selected.map(id => { const t = todos.find(t => t.id === id); return `- ${id}: ${t?.title}\n  Acceptance: ${t?.acceptance ?? 'See run acceptance criteria'}`; }).join('\n')}\n\nRead applicable AGENTS.md instructions. Keep reviewer follow-up plans in current-plan.md.\n${background}`;
    const project = Buffer.from(plan).toString('base64');
    const planPath = `${contextDir}/current-plan.md`;
    const projectScript = `const fs=require('fs');fs.mkdirSync(${JSON.stringify(contextDir)},{recursive:true});if(fs.existsSync(${JSON.stringify(planPath)}))fs.copyFileSync(${JSON.stringify(planPath)},${JSON.stringify(`${contextDir}/previous-plan-${claimed.iteration_id}.md`)});fs.writeFileSync(${JSON.stringify(planPath)},Buffer.from('${project}','base64'));`;
    const command = `node -e ${quote(projectScript)} && CODEXPRO_MANAGED_LOOP=1 codexpro loop-handoff --root . --context-dir ${quote(contextDir)} --yes ${forwarded.map(quote).join(' ')}`;
    const start = await call('start_jobs', { workspace_id: claimed.workspace_id, execution: envelope, commands: [{ command, label: 'Managed executor/reviewer packet' }] });
    console.error(`Run ${runId}; iteration ${claimed.iteration_id}; jobs ${start.job_ids.join(', ')}; claim key ${key}. Recover with work_status if this launcher exits.`);
    let job;
    for (;;) {
      const state = await read('summary');
      await call('work_update', { action: 'heartbeat', run_id: runId, expected_revision: state.revision, request_key: `${key}:heartbeat`, attempt_token: claimed.attempt_token });
      const collection = await call('jobs', { workspace_id: claimed.workspace_id, job_ids: start.job_ids, wait_ms: 30_000, output: 'none' });
      job = collection.jobs?.[0]; if (!job) throw new Error('Missing job status; inspect work_status before continuing.');
      if (job.status !== 'running') break;
    }
    const succeeded = job.status === 'succeeded' || job.job_status === 'succeeded';
    let state = await read('summary');
    const legacyPlan = await call('read', { workspace_id: claimed.workspace_id, path: planPath, max_bytes: 16000 });
    const memory = await call('work_update', { action: 'put_document', run_id: runId, expected_revision: state.revision, request_key: `${key}:handoff`, attempt_token: claimed.attempt_token,
      title: `Legacy reviewer handoff: ${claimed.iteration_id}`, kind: 'note', reference_path: planPath, todo_ids: selected,
      content: `Job ${job.job_id ?? job.id}; source file SHA-256 ${legacyPlan.sha256}; numbered excerpt${legacyPlan.truncated ? ' (truncated)' : ''}:\n${legacyPlan.text}` });
    state = await read('summary');
    cache.finish = { action: 'finish_iteration', run_id: runId, expected_revision: state.revision, request_key: `${key}:finish`, attempt_token: claimed.attempt_token,
      outcome: succeeded ? 'completed' : 'failed', summary: `Legacy executor/reviewer job ${job.id ?? job.job_id}: ${job.status ?? job.job_status}. Inspect job output and .ai-bridge artifacts.`,
      next_action: succeeded ? 'Select another packet or request whole-run acceptance verification.' : 'Inspect failed job, source changes and the reviewer follow-up plan before retrying.',
      todos: todos.map(t => selected.includes(t.id) ? { ...t, status: succeeded ? 'done' : 'blocked', ...(succeeded ? {} : { reason: 'Executor/reviewer job failed; inspect retained output.' }), evidence_ids: [...(t.evidence_ids ?? []), start.work_receipt.operation_id] } : t),
      evidence_ids: [start.work_receipt.operation_id, memory.document_id], finish_run_if_ready: succeeded };
    cache.failed = !succeeded; save();
    const finish = await call('work_update', cache.finish);
    cache.final = { ...finish, session_token: claimed.session_token, next_packet_hint: 'For another packet in this same worker session, use a fresh --claim-key and pass this --session-token. A fresh independent worker omits it.' }; save();
    console.log(JSON.stringify(cache.final, null, 2));
    if (!succeeded) process.exitCode = 1;
  });
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) runWorkCommand(process.argv.slice(2)).catch(error => { console.error(error.message); process.exitCode = 1; });
