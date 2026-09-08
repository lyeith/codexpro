import assert from "node:assert/strict";
import { toolCardWidgetHtml } from "../dist/toolCardWidget.js";

// These harnesses realpath and stat the paths CodexPro returns, so they need the raw
// absolute form. Production defaults to redacted labels; see the redaction assertions
// at the end of scripts/smoke.mjs for coverage of the default behaviour.
process.env.CODEXPRO_EXPOSE_ABSOLUTE_PATHS = '1';

const scripts = [...toolCardWidgetHtml.matchAll(/<script>([\s\S]*?)<\/script>/g)];
const widgetScript = scripts.at(-1)?.[1];
if (!widgetScript) throw new Error("tool-card widget script missing");

class FakeElement {
  constructor() {
    this.innerHTML = "";
    this.textContent = "";
    this.listeners = new Map();
  }

  addEventListener(name, listener) {
    this.listeners.set(name, listener);
  }

  closest(selector) {
    return selector === "[data-copy-card-output]" ? this : null;
  }
}

function mount(openai = {}) {
  const root = new FakeElement();
  const timers = [];
  const listeners = new Map();
  const document = {
    documentElement: { dataset: {} },
    getElementById(id) {
      return id === "root" ? root : null;
    }
  };
  const window = {
    openai,
    setTimeout(callback, delay) {
      timers.push({ callback, delay });
      return timers.length;
    },
    clearTimeout() {},
    addEventListener(name, listener) {
      listeners.set(name, listener);
    }
  };
  let copied = "";
  const navigator = {
    clipboard: {
      async writeText(value) {
        copied = value;
      }
    }
  };
  new Function("window", "document", "navigator", "Element", widgetScript)(window, document, navigator, FakeElement);
  return {
    root,
    timers,
    listeners,
    document,
    copied: () => copied
  };
}

const bashPayload = {
  codexpro_tool: "bash",
  command: "npm run check",
  cwd: "/tmp/workspace",
  exit_code: 0,
  duration_ms: 437,
  stdout: "✓ checks passed",
  stderr: ""
};

const nested = mount({
  theme: "light",
  toolOutput: { result: { payload: { structuredContent: bashPayload } } }
});
assert.match(nested.root.innerHTML, /Command completed/);
assert.match(nested.root.innerHTML, /npm run check/);
assert.match(nested.root.innerHTML, /Passed/);
assert.equal(nested.document.documentElement.dataset.theme, "light");

const copyButton = new FakeElement();
await nested.root.listeners.get("click")({ target: copyButton });
assert.match(nested.copied(), /\$ npm run check/);
assert.equal(copyButton.textContent, "Copied");

const delayed = mount();
assert.match(delayed.root.innerHTML, /Preparing result/);
delayed.listeners.get("openai:set_globals")({
  detail: {
    globals: {
      theme: "dark",
      mcp_tool_result: {
        structuredContent: {
          codexpro_tool: "open_workspace",
          root: "/tmp/workspace",
          agents_loaded: true,
          tool_mode: "standard",
          write_mode: "handoff",
          bash_mode: "safe",
          git_status: "working tree clean"
        }
      }
    }
  }
});
assert.match(delayed.root.innerHTML, /Connected workspace/);
assert.equal(delayed.document.documentElement.dataset.theme, "dark");

const multi = mount({
  theme: "light",
  toolOutput: {
    structuredContent: {
      codexpro_tool: "open_workspace",
      primary_workspace_id: "ws_alpha",
      include_tree: false,
      tool_mode: "standard",
      write_mode: "workspace",
      bash_mode: "safe",
      workspaces: [
        {
          project_id: "alpha",
          workspace_id: "ws_alpha",
          root: "/tmp/alpha",
          already_open: false,
          git_status: "working tree clean"
        },
        {
          project_id: "beta",
          workspace_id: "ws_beta",
          root: "/tmp/beta",
          already_open: true,
          git_status: " M src/index.ts"
        }
      ]
    }
  }
});
assert.match(multi.root.innerHTML, /Connected workspaces/);
assert.match(multi.root.innerHTML, /2 workspaces connected/);
assert.match(multi.root.innerHTML, /ws_alpha/);
assert.match(multi.root.innerHTML, /ws_beta/);
assert.match(multi.root.innerHTML, /primary/);
assert.match(multi.root.innerHTML, /already open/);

const unavailable = mount();
assert.equal(unavailable.timers.length, 1);
unavailable.timers[0].callback();
assert.match(unavailable.root.innerHTML, /Result unavailable/);

console.log("✓ widget smoke test passed");

// Lifecycle and commit receipts must retain their meaning when expanded.
const runningCard = mount({toolOutput:{codexpro_tool:'bash', job_id:'job_aabbccdd', job_status:'running', exit_code:null, command:'sleep 30'}});
assert.match(runningCard.root.innerHTML,/Command running/);
assert.match(runningCard.root.innerHTML,/job_aabbccdd/);
assert.doesNotMatch(runningCard.root.innerHTML,/Verification completed|>Passed</);
const repeatedReview = mount({toolOutput:{codexpro_tool:'show_changes', review_checkpoint_hit:true, changed:false, changed_files:[], status:' M dirty.txt'}});
assert.match(repeatedReview.root.innerHTML,/Unchanged review/);
assert.doesNotMatch(repeatedReview.root.innerHTML,/>Clean</);
const manyChanges = mount({toolOutput:{codexpro_tool:'show_changes',changed:true,changed_files:Array.from({length:30},(_,i)=>' M file'+i)}});
assert.match(manyChanges.root.innerHTML,/>30</);
assert.match(manyChanges.root.innerHTML,/Showing 12 of 30/);
const receipt = mount({toolOutput:{codexpro_tool:'commit_changes',commit:'a'.repeat(40),branch:'main',working_tree_clean:false,status:'?? leftover.txt',files:['doc.md'],file_count:1}});
assert.match(receipt.root.innerHTML,/Commit created/); assert.match(receipt.root.innerHTML,/Changes remain/); assert.match(receipt.root.innerHTML,/leftover.txt/);
const unknownReceipt = mount({toolOutput:{codexpro_tool:'commit_changes',commit:'b'.repeat(40),working_tree_clean:null,status_error:'status read failed'}});
assert.match(unknownReceipt.root.innerHTML,/Status unavailable/); assert.doesNotMatch(unknownReceipt.root.innerHTML,/>Clean</);
const jobCard = mount({toolOutput:{codexpro_tool:'jobs',waited_ms:2,requested_wait_ms:30000,all_finished:true,all_succeeded:false,jobs:[{job_id:'job_aabbccdd',status:'failed',exit_code:3,stdout_tail:'<script>bad</script>',output_truncated:true,output_mode:'tail'}]}});
assert.match(jobCard.root.innerHTML,/Needs attention/); assert.match(jobCard.root.innerHTML,/job_aabbccdd/); assert.match(jobCard.root.innerHTML,/Output truncated/);
assert.match(jobCard.root.innerHTML,/&lt;script&gt;/); assert.doesNotMatch(jobCard.root.innerHTML,/<script>bad/);
const missingCard=mount(); missingCard.timers.find(timer=>timer.delay===1200).callback();
assert.match(missingCard.root.innerHTML,/do not repeat it blindly/);
const failedCommit=mount({toolOutput:{codexpro_tool:'commit_changes',error:'nothing to commit',error_code:'nothing_to_commit'}});
assert.match(failedCommit.root.innerHTML,/>Failed</); assert.doesNotMatch(failedCommit.root.innerHTML,/Commit created/);
console.log('widget lifecycle, commit, checkpoint and completeness checks passed');
