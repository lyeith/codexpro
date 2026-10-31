# Managed workspace differential lane

This driver starts and stops **its own new private pair**. It refuses either
occupied port 7210/7211 before creating fixture state. It never stops an existing
listener, uses a service manager, changes the live connector, or sends Bash/job
commands. Run it only after the parent has finished and stopped its earlier
private direct-mode pair.

## Launch configuration

The immutable TypeScript 0.31.0/44bb76b release uses MCP workspace mode,
`bash=safe`, `write=workspace` and standard tools on **7211**. That combination
is required by the source guard in `src/config.ts:571–572`. Go uses MCP workspace
mode, **full Bash**, workspace writes and standard tools on **7210**. No Go
approval mode or reduced-permission machinery is introduced.

Every catalog, managed root, token, state/home, job/audit/work directory, limits
file and codex directory is private and explicitly selected. The managed roots
are outside each service's config home, exercising the configured-root
correction. The driver strips inherited CodexPro, Codex, proxy and Git override
variables. It preserves the actual HOME and creates independent mode-0600
tokens. Tokens are read into process memory/environment, never argv or evidence.

Both repositories are newly created. Their fixed Git identity, timestamps,
hooks setting, contents and branch sequence produce identical main and
alternate commit objects. The second base is a distinct commit. The one catalog
project has a worktree cap of one; the global cap is eight, isolating the project
quota case.

## Twelve ordered business cases

| Case | Required result |
| --- | --- |
| MANAGED-01 | MCP catalog shape, no direct workspace ID, exact four workspace descriptors |
| MANAGED-02 | Missing required workspace_id is a schema error |
| MANAGED-03 | One new managed checkout with source-formatted ID, branch, base and private nonce |
| MANAGED-04 | Explicit retry key reuses the same workspace in the same and a second session |
| MANAGED-05 | Reusing the key for the distinct alternate base fails without allocation |
| MANAGED-06 | A second key hits the project cap without allocation |
| MANAGED-07 | Both sessions open the same owned workspace and get exact summaries |
| MANAGED-08 | File write/read preserves fixed full-file bytes and hash |
| MANAGED-09 | Release preserves checkout, dirty work and ownership; second session reopens/reads |
| MANAGED-10 | Dirty removal fails and leaves checkout, branch and file unchanged |
| MANAGED-11 | Native delete patch removes only the owned proof file and leaves a clean checkout |
| MANAGED-12 | Clean removal unregisters/deletes the checkout, preserves its branch, and rejects reopening its old ID |

The process/launch checks reuse `validate_launch`. Before any MCP mutation, the
recorded Popen PID/start ticks must own the expected private listening socket,
and the service must report the declared modes. Catalog bytes are bound to the
newly created repository and checked for unexpected changes before every call.

After creation the driver checks the persisted TypeScript lease JSON or Go lease
row, its principal and retry hash, and actual Git worktree inventory. Source
formulas are validated explicitly: 24 random bytes in a `wt_` base64url ID;
repository ID SHA256(commonDir + NUL + scopeRelativePath)[:24]; checkout directory
SHA256(workspaceId)[:32]; branch `codexpro/mcp/` plus SHA256(workspaceId)[:24].
The fixture scope is the Git top level. Only then does it write and read a
side/PID-specific nonce, verifying its full-file SHA256 before removing it.
Roots are never derived from a redacted public label.

Changing bearer-token bytes is not a foreign-principal test. The driver derives
the source static MCP principal from the private persistent connector identity.
The second session must retain that owner, and its audit actor must match.
Foreign-principal rejection remains in the separate ownership tests.

## Strict comparison and explicit fixture difference

Raw requests/results are retained in bounded, credential-checked gzip JSONL.
The comparator preserves the complete HTTP/JSON-RPC/result envelope, text,
arrays, errors, optional versus null fields and result shapes. Request IDs and
client elapsed time are validated before normalization. Workspace IDs, hashes,
branches and own roots receive aliases only after the persisted/Git/nonce checks.

Managed summaries attach recent activity before their own audit completion.
The driver reads adjacent audit watermarks and requires exactly one committed
action for the summary call. It validates the recent projection using only
durable mutating receipts at or before the earlier watermark; it does not
incorrectly include a create call's own just-completed action. Action IDs,
actors and timestamps remain causally bound to the originating request and
receipt. **Sequence numbers are retained** in this controlled fresh pair.

The sole permission fixture normalization is the independently verified
`bash_mode` field in open_workspace and the exact one-line summary text
`Bash mode: safe` versus `Bash mode: full`. A missing, duplicate or unexpected
mode fails. Other fields/text are unchanged. Such cases count as
`matched_with_declared_permission_fixture`, separately from `unchanged`.
Approved retired descriptors are separately enumerated with approval commit
`acf6b9b`; this lane does not silently treat them as unchanged tools.

The report always leaves `stage4_complete:false` and
`managed_operational_migration_proven:false`. This is fresh private lifecycle
equivalence, not adoption of historical leases or proof of every other stage-4
gate.

## Cleanup and bounds

The driver has a five-minute deadline, bounded Git calls, private request
timeouts, at most six HTTP sessions per reference and bounded raw evidence.
Services run under nice 10. It closes its own sessions and signals only its
verified Popen process groups, created with start_new_session. Before an
escalation after the anchor exits, a remaining process must match a previously
witnessed owner identity. Unknown/reused identities are not signalled. Any
remaining owned group or unresolved cleanup makes the lane fail.

The reproduction command permits 90 seconds after an external TERM for the
bounded MCP session closes and both owned process-group stops. A hard KILL can
prevent final evidence publication; preserve the private state for inspection
if that outer bound is reached.

Successful case 12 removes the checkout and preserves the managed branch.
Failed or unsettled runs retain their private workspace/state/evidence for
review; the driver does not force-remove dirty work or guess that a timed-out
request had no effect. Token files and server logs remain private; do not copy
raw logs into public evidence without credential checks.

## Invocation

From `/home/spite/Projects/codexpro-go`, after the earlier private pair has
stopped, use a new state parent and new output filename:

```bash
nice -n 10 timeout --signal=TERM --kill-after=90s 360s \
  python3 docs/go-rewrite/managed_contract_suite.py \
  --release "$(readlink -f /home/spite/apps/codexpro/current)" \
  --binary /home/spite/.codexpro-go/bin/codexpro-go \
  --state-parent /home/spite/.codexpro-go-proof/managed-pair-20261031-01 \
  --output /home/spite/Projects/codexpro-go/docs/go-rewrite/evidence/managed-contracts-01.json
```

Resolving the current symlink is read-only. The driver still rejects a release
whose immutable directory/version is not the captured 0.31.0/44bb76b build.
An explicit immutable release path may be supplied instead.

Required adjacent Python modules: `contract_suite.py`,
`paired_contract_suite.py`, `contract_cases.py`,
`process_contract_suite.py` and `admin_catalog_contract_suite.py`.

```bash
python3 -m unittest discover -s docs/go-rewrite -p test_managed_contract_suite.py -v
```

At staging, syntax checks and **14 offline tests** pass, including real local
deterministic Git fixtures. Socket-bind refusal and process-signal guards use
mocks in the offline tests because the local scratch runtime disallows sockets.
**The actual paired runtime lane has not yet executed.**
