package activity

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Node is a development-only syntax checker already used by the side-by-side
// TypeScript contract harness. The Go service does not invoke it.
func TestEmbeddedDashboardScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable for development-only JavaScript syntax verification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--check", "--input-type=commonjs")
	command.Stdin = strings.NewReader(dashboardScript)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("dashboard JavaScript does not parse: %v\n%s", err, output)
	}
}

func TestDashboardAgentFilterRefreshBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable for development-only dashboard DOM verification")
	}
	domModule, err := filepath.Abs("../../../node_modules/happy-dom/lib/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(domModule); err != nil {
		t.Skip("happy-dom is unavailable; run npm ci for development-only dashboard DOM verification")
	}
	h := testHandler(t, nil)
	record(t, h, "read", "labeled", map[string]any{"agent": "SP-1", "path": "a.txt"}, map[string]any{"bytes": 1})
	snapshot, err := h.CollectDashboard(context.Background(), "alpha", "SP-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.History["beforeSequence"] = 90
	snapshot.History["nextBeforeSequence"] = 10
	page := RenderDashboardPage(snapshot)
	data1 := RenderDashboardData(snapshot)
	snapshot.History["agent"] = "SP-2"
	data2 := RenderDashboardData(snapshot)
	fixtures, err := json.Marshal(map[string]any{"page": page, "data1": data1, "data2": data2, "live1": renderLive(map[string]any{"generatedAt": "2026-10-31T16:00:01.000Z", "calls": []any{}, "jobs": []any{}})})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--input-type=module", "-", domModule)
	command.Stdin = strings.NewReader("const fixtures = " + string(fixtures) + ";\n" + agentFilterDOMTest)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("dashboard agent filter behavior failed: %v\n%s", err, output)
	}
}

const agentFilterDOMTest = `
import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';
const { Window } = await import(pathToFileURL(process.argv[2]).href);
const window = new Window({ url: 'https://example.test/activity?project_id=alpha&agent=SP-1&before_sequence=90' });
window.document.body.innerHTML = fixtures.page;
window.setInterval = () => 0;
let resolveLive;
const requested = [];
window.fetch = async (url) => {
  const source = new URL(url, 'https://example.test');
  requested.push(source);
  if (source.pathname === '/activity/data') return { ok: true, json: async () => source.searchParams.get('agent') === 'SP-2' ? fixtures.data2 : fixtures.data1 };
  if (source.pathname === '/activity/live') return new Promise(resolve => { resolveLive = resolve; });
  return { ok: false, status: 503, json: async () => ({}) };
};
window.eval(fixtures.page.match(/<script>([\s\S]*?)<\/script>/)[1]);
const input = () => window.document.querySelector('[data-agent-filter]');
const older = () => new URL([...window.document.querySelectorAll('.history-navigation a')].find(link => link.textContent.includes('Older')).href);
assert.equal(input().value, 'SP-1');
assert.equal(input().maxLength, 4);
input().value = 'ABCDE';
assert.equal(input().checkValidity(), false, 'five-character labels must fail browser validation');
input().dispatchEvent(new window.Event('change', { bubbles: true }));
assert.equal(new URL(window.location.href).searchParams.get('agent'), 'SP-1');
assert.equal(older().searchParams.get('agent'), 'SP-1');
input().focus();
input().value = 'SP-D';
assert.equal(await window.refreshDashboard(undefined, true), true);
assert.equal(input().value, 'SP-D', 'background refresh must preserve a label being typed');
const stale = window.refreshLive();
await Promise.resolve();
assert.equal(requested.find(url => url.pathname === '/activity/live').searchParams.get('agent'), 'SP-1');
input().value = 'SP-2';
input().dispatchEvent(new window.Event('change', { bubbles: true }));
await window.happyDOM.waitUntilComplete();
// waitUntilComplete excludes our unresolved fetch promise, so the caller change
// completes independently of the older live refresh.
assert.equal(new URL(window.location.href).searchParams.get('agent'), 'SP-2');
assert.equal(new URL(window.location.href).searchParams.has('before_sequence'), false);
assert.equal(new URL(window.location.href).searchParams.get('project_id'), 'alpha');
assert.equal(input().value, 'SP-2');
assert.equal(older().searchParams.get('agent'), 'SP-2');
resolveLive({ ok: true, text: async () => '<p data-stale-label>stale SP-1 response</p>' });
await stale;
assert.equal(window.document.querySelector('[data-stale-label]'), null);
assert.equal(await window.refreshDashboard(undefined, true), true);
assert.equal(requested.filter(url => url.pathname === '/activity/data').at(-1).searchParams.get('agent'), 'SP-2');
assert.equal(input().value, 'SP-2');
const fresh = window.refreshLive();
await Promise.resolve();
assert.equal(requested.filter(url => url.pathname === '/activity/live').at(-1).searchParams.get('agent'), 'SP-2');
resolveLive({ ok: true, text: async () => fixtures.live1 });
await fresh;
input().value = '';
input().dispatchEvent(new window.Event('change', { bubbles: true }));
await window.happyDOM.waitUntilComplete();
assert.equal(new URL(window.location.href).searchParams.has('agent'), false);
assert.equal(input().value, '');
await window.happyDOM.close();
`
