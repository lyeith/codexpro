# D171 exact pending-summary delegation

This continues D169 after ROOT's deployed Agentmail 25e1a886 and CodexPro
6023fc3 pair. David's Option B (gteypzjs, reply aovn2hhx), relayed by ROOT
h7cyqskl and recorded in row 894, authorizes only summary metadata for the
two tuples below. ROOT retains final AUTH review, fresh custody checks,
configuration publication, service transitions, and live acceptance.

| CodexPro label | Actual source principal / agent | Target principal / agent | Canonical mailbox | Logical recipient |
| --- | --- | --- | --- | --- |
| AP1 | sp-fleet / sp-fleet | plans-system / android-ap1 | android/ap1 | AP1 |
| RDP1 | sp-fleet / sp-fleet | plans-system / rdp-rdp1 | rdp/rdp1 | RDP1 |

The label selects within the approved shared transport. It does not identify
or authenticate a separate conversation. No target credential is borrowed,
and no audience, mailbox ownership, original read/offer, receipt/ACK, sender
identity, exchange, lifecycle, binding, or delivery-policy right is added.
Only the count and latest canonical sender are delegated. AP2 is excluded.

The earlier AP1 live appearance check is **failed**, not superseded by a
passing source test. ROOT's wxz4ujl6 reported fixture fnshsose and named reads
cpa_71dd, cpa_2056 without a notice; the owner's later pull was observed,
but appearance and subsequent notice clearing were not proved. A separate
AP1 Agentmail registration with `both` audience did not establish the actual
CodexPro connection identity. The D169 owner-binding proposal is retained
historically in [pending-mail-release.md](pending-mail-release.md).

## Exact source and artifacts

| Candidate | Source commit | SHA-256 |
| --- | --- | --- |
| CodexPro | `819dd7d9d649a47fba7a0d5c61c099118b56bad2` | `281bb8e671d66d97b0b94f5bb17db33f659c3865e8712e8bccecdc15a20271f9` |
| Agentmail | `7a6f45ceb5a3c450c62bb0c5737c6db893ecd07c` | `6264b3d7640b20a024e9e5553eae36328405a79ee0c1281930e368c00419d105` |

Stage: `/home/spite/Projects/sp9-r894-mail-delegation-build-20261117`.

- `codexpro-go-819dd7d9d6`: 29,688,280 bytes.
- `agentmail-7a6f45ceb5`: 19,566,504 bytes.
- `manifest.json`: SHA-256
  `0fa933dbfe94d4f14b6e00473c45fa8359a642a152025fcbee9921a28375ff86`.
  It records clean source/tree/ancestry, compiler, CWD, flags, environment,
  timestamps, binary readback, and verified recovery artifacts.

Agentmail is a direct child of deployed 25e1a886, preserving main 9c4af3d and
both Claude fixes. CodexPro is a child of documentation commit 7cb9561,
which retains deployed runtime 6023fc3. The unrelated dirty TypeScript
checkout and canonical Agentmail checkout were not edited.

CodexPro's binary embeds the exact revision with `vcs.modified=false`.
Installed Go 1.26.8 recognizes a Git `.git` directory but not an isolated
worktree pointer file, so Agentmail has no embedded VCS fields. Its provenance
uses the clean pinned source/tree/ancestry, exact compiler/flags and digest.
No instrumentation or extra rebuild was used to manufacture a stamp.

Immediate recovery artifacts, rehashed during this build, remain in
`/home/spite/Projects/sp9-r894-mail-release-build-20261117`:

| Artifact | Source | SHA-256 |
| --- | --- | --- |
| `codexpro-go-6023fc3999` | `6023fc3999bf472ee408e03998fde16e07162725` | `03ac4b5ad13fd082027449e4e8a419f6999beaae0dd0fbbfbef8fd36b4f92b38` |
| `agentmail-25e1a886e6` | `25e1a886e6f1135554004f4c5b1623544da5cdf4` | `e59622ab50434ea87d5788e4898432d244e77cd926616e5fd7e3ed6dfd658439` |

These are the D169 recovery floor for this continuation. Do not substitute
the older pre-Claude Agentmail binary or restore a historical database.

## Authorization and response contract

The Agentmail-only MailGrants companion gains optional `pending_summaries`.
The shared principals/credentials schema does not change. Each exact grant
requires source principal/agent, target principal/agent, canonical mailbox,
configured nonempty logical recipient, and SP/AP/RDP attribution. Both
registrations must be system leaves. Duplicate source principal/agent/label
tuples, unknown/case-conflicting/duplicate/null fields, and more than
64 grants are invalid.

Every delegated HTTP request uses the original current named bearer and
existing registration pin, and sends exactly `address`,
`logical_recipient` and `attribution` to the existing private summary route.
Agentmail rechecks the current credential and active source/target
registrations/principals. A separate summary context value leaves the
ordinary MailboxScope unchanged; no other route consults it.

Canonical owners retain their existing ordinary/addressed metadata access.
Well-formed duplicate or extra query fields are refused by this route.
Malformed raw query syntax retains the existing 401 authentication boundary.
Missing or broken companion configuration grants nothing and leaves ordinary
policy-allowed owner operations working.

The database query still projects exactly `address`, `logical_recipient`,
`count` and `latest_sender` in one snapshot, without reading message bodies
or changing durable state. CodexPro adds only the binding's exact
`attribution` query field. Binding lookup remains briefly cached; metadata is
revalidated on every named tool call, including the call immediately after
a zero and after an owner pull. The notice remains before unchanged original
delivery preparation, keeping the original envelope last and its existing
receipt observer in control.

## Focused proof and retained failures

- `job_bec4574f`: full Agentmail plansaccess package passed (0.893s).
  The initial store/API fixtures failed: a scoped query correctly returned
  an empty result rather than an error; the imported fleet private REST
  fixture omitted its existing credential pin. CodexPro was not reached.
- `job_2aa47b17`: store passed; API mutation restoration reused a struct,
  retaining omitted revoked/disabled booleans. CodexPro was not reached.
  Each mutation now uses a fresh policy copy and cleanup.
- `job_d0f0914b`: exit 0. Pending-summary store 1.107s, API 2.345s;
  CodexPro config 1.438s and transport 27.760s under `-race`.
- `job_242fac51`: exit 0; both source-pinned binaries built and read back,
  plus both immediate recovery digests verified.

The tests cover exact AP1/RDP1 tuples; same-principal wrong actor; wrong
principal, target, alias, logical recipient and attribution; missing/wrong
credential pins; revoked, rebound and disabled registrations; duplicate/extra
queries; missing/broken/removed grants; exact four-field metadata and latest
canonical sender; unchanged durable state; real owner pull and next-call zero;
ordinary/other-logical mail isolation; and unchanged original, offer,
receipt/ACK, sender impersonation, personal-send and lifecycle refusals.

The shared-transport MCP test checks both labels use the same actual caller
bearer/pin, exact separate target selections, next-call appearance after zero,
and immediate clearing without waiting for a TTL. Existing original-delivery
receipt tests remain in the passing race selection. These source tests do not
claim live AP1/RDP1 acceptance or live-job survival across a restart.

## Reproduce the non-live binaries

Use only the isolated authors, with no concurrent editor/build in either.
This creates a new private output directory, selects the exact source commits,
builds without launching either service, and restores the authors. A hash
mismatch is a stop/report condition. No passing repeat is required for handoff.

```sh
/home/spite/.local/bin/plans-heavy run -- uv run --no-project --offline python - <<'D171_REBUILD'
import hashlib, os, pathlib, subprocess
base = pathlib.Path('/home/spite/Projects')
out = base/'sp9-r894-mail-delegation-rebuild-20261117'
out.mkdir(mode=0o700)
cases = [
    ('codexpro-sp9-r894-pending-mail-20261117', 'go',
     '819dd7d9d649a47fba7a0d5c61c099118b56bad2',
     '/var/lib/ssd-dev/data/managed/tool-cache/go-mod/golang.org/toolchain@v0.0.1-go1.27.3.linux-amd64',
     'go1.27.3', './cmd/codexpro', 'codexpro-go-819dd7d9d6',
     '281bb8e671d66d97b0b94f5bb17db33f659c3865e8712e8bccecdc15a20271f9'),
    ('agent-mailbox-sp9-r894-pending-summary-20261117', '',
     '7a6f45ceb5a3c450c62bb0c5737c6db893ecd07c', '/usr/lib/go-1.26',
     'go1.26.8', './cmd/agentmail', 'agentmail-7a6f45ceb5',
     '6264b3d7640b20a024e9e5553eae36328405a79ee0c1281930e368c00419d105'),
]
def capture(args, **kw):
    return subprocess.check_output(args, text=True, timeout=30, **kw).strip()
for folder, subdir, revision, goroot, version, target, name, expected in cases:
    repo = base/folder
    assert not capture(['git','status','--porcelain=v1','--untracked-files=all'], cwd=repo)
    branch = subprocess.run(['git','symbolic-ref','--quiet','--short','HEAD'],
                            cwd=repo, text=True, capture_output=True)
    previous = branch.stdout.strip() if branch.returncode == 0 else capture(
        ['git','rev-parse','HEAD'], cwd=repo)
    env = dict(os.environ, GOTOOLCHAIN='local', GOROOT=goroot, GOMAXPROCS='2',
               CGO_ENABLED='1', GOOS='linux', GOARCH='amd64', GOAMD64='v1',
               GOFLAGS='', GOEXPERIMENT='', GOPROXY='off', GOSUMDB='off',
               CC='/usr/bin/gcc', CGO_CFLAGS='-O2 -g', CGO_CPPFLAGS='',
               CGO_CXXFLAGS='-O2 -g', CGO_FFLAGS='-O2 -g', CGO_LDFLAGS='-O2 -g')
    go = str(pathlib.Path(goroot)/'bin/go')
    assert capture([go,'version'], env=env) == f'go version {version} linux/amd64'
    try:
        subprocess.run(['git','switch','--detach',revision], cwd=repo, check=True)
        assert capture(['git','rev-parse','HEAD'], cwd=repo) == revision
        assert not capture(['git','status','--porcelain=v1','--untracked-files=all'], cwd=repo)
        binary = out/name
        subprocess.run(['nice','-n','10',go,'build','-p','2','-trimpath',
                        '-buildvcs=true','-ldflags=-buildid=','-o',str(binary),target],
                       cwd=repo/subdir, env=env, check=True, timeout=600)
        assert not capture(['git','status','--porcelain=v1','--untracked-files=all'], cwd=repo)
        digest = hashlib.sha256(binary.read_bytes()).hexdigest()
        assert digest == expected, (name, digest, expected)
        binary.chmod(0o555)
        print(name, revision, digest)
        print(capture([go,'version','-m',str(binary)], env=env))
    finally:
        subprocess.run(['git','switch'] + ([previous] if branch.returncode == 0
                       else ['--detach',previous]), cwd=repo, check=True)
D171_REBUILD
```

The recorded GCC is `gcc (Ubuntu 15.2.0-16ubuntu1) 15.2.0`. Existing installed
compilers are invoked directly with `GOTOOLCHAIN=local`; no download or
toolchain-module verification change is required.

## Configuration deltas and non-live preparation

The two checked-in JSON arrays are **additions/replacements for selected
fields**, not complete runtime configuration:

- [pending-summary-grants.d171.json](pending-summary-grants.d171.json):
  SHA-256 `6770eb02e707b755c6db17ea5fc95d6af9fcdb57e5ac0129d758554159ed0920`.
- [pending-mail-bindings.d171.json](pending-mail-bindings.d171.json):
  SHA-256 `b82014b9133cd356995052c10f2de45714a68b5da6d58f5348f03b228f5f331a`.

ROOT first confirms the actual controller's selected `--mail-grants-file`,
principals file, and CodexPro pending-mail EnvironmentFile. Do not infer these
from a predecessor selector. The already deployed D169 dedicated env file was
`/home/spite/.config/codexpro/pending-mail-r894.env`, subject to fresh selection
verification. If the controller has no companion flag, ROOT must choose and
configure its exact existing/new companion within its custody transition;
merely writing an unused file cannot activate a grant.

Set `D171_GRANTS_FILE`, `D171_ENV_FILE`, and `D171_PRINCIPALS_FILE` to those
reviewed absolute paths. The following command only validates/reads them and
writes review candidates under the build stage. It preserves existing exchanges,
all other summary grants and all other CodexPro bindings. It refuses unexpected
prior AP1/RDP1 configuration. It does not publish config or restart a service.

```sh
uv run --no-project --offline python - <<'D171_CONFIG_STAGE'
import hashlib, json, os, pathlib, shlex, subprocess
base = pathlib.Path('/home/spite/Projects')
source = base/'codexpro-sp9-r894-pending-mail-20261117/deploy'
build = base/'sp9-r894-mail-delegation-build-20261117'
review = build/'root-review'
review.mkdir(mode=0o700)
grants_file = pathlib.Path(os.environ['D171_GRANTS_FILE'])
env_file = pathlib.Path(os.environ['D171_ENV_FILE'])
principals = pathlib.Path(os.environ['D171_PRINCIPALS_FILE'])
for path in (grants_file, env_file, principals):
    assert path.is_absolute() and not path.is_symlink() and path.is_file()
binary = build/'agentmail-7a6f45ceb5'
assert hashlib.sha256(binary.read_bytes()).hexdigest() == '6264b3d7640b20a024e9e5553eae36328405a79ee0c1281930e368c00419d105'
def validate(path):
    subprocess.run([str(binary),'validate-mail-grants','--principals-file',str(principals),
                    '--mail-grants-file',str(path)], check=True, timeout=30)
def save(name, payload):
    with (review/name).open('xb') as stream:
        os.fchmod(stream.fileno(),0o600)
        stream.write(payload)
        stream.flush()
        os.fsync(stream.fileno())
validate(grants_file)
original_grants = grants_file.read_bytes()
grants = json.loads(original_grants)
original_exchanges = grants.get('exchanges', [])
assert isinstance(original_exchanges, list)
additions = json.loads((source/'pending-summary-grants.d171.json').read_text())
selected = list(grants.get('pending_summaries', []))
for addition in additions:
    key = tuple(addition[k] for k in ('principal','agent','label'))
    found = [g for g in selected if tuple(g[k] for k in ('principal','agent','label')) == key]
    assert not found or found == [addition]
    if not found:
        selected.append(addition)
grants['pending_summaries'] = selected
assert grants.get('exchanges', []) == original_exchanges
candidate_grants = (json.dumps(grants,indent=2)+'\n').encode()
original_env = env_file.read_bytes()
lines = original_env.decode().splitlines(keepends=True)
key = 'CODEXPRO_PENDING_MAIL_BINDINGS='
indexes = [i for i,line in enumerate(lines) if line.startswith(key)]
assert len(indexes) == 1
index = indexes[0]
assignment = shlex.split(lines[index], comments=False, posix=True)
assert len(assignment) == 1 and assignment[0].startswith(key)
bindings = json.loads(assignment[0][len(key):])
deltas = json.loads((source/'pending-mail-bindings.d171.json').read_text())
assert len({b['label'] for b in bindings}) == len(bindings)
for delta in deltas:
    old = [b for b in bindings if b['label'] == delta['label']]
    allowed = [delta]
    if delta['label'] == 'AP1':
        allowed.append(dict(delta, principal='plans-system', agent='android-ap1'))
    assert not old or len(old) == 1 and old[0] in allowed
    if old:
        bindings[bindings.index(old[0])] = delta
    else:
        bindings.append(delta)
lines[index] = key + json.dumps(json.dumps(bindings,separators=(',',':'))) + '\n'
candidate_env = ''.join(lines).encode()
save('mail-grants.before.json', original_grants)
save('mail-grants.candidate.json', candidate_grants)
save('pending-mail.before.env', original_env)
save('pending-mail.candidate.env', candidate_env)
validate(review/'mail-grants.candidate.json')
metadata = {'grants_file':str(grants_file),'env_file':str(env_file),
            'files':{name:hashlib.sha256((review/name).read_bytes()).hexdigest()
                     for name in ('mail-grants.before.json','mail-grants.candidate.json',
                                  'pending-mail.before.env','pending-mail.candidate.env')}}
save('configuration.json',(json.dumps(metadata,indent=2)+'\n').encode())
print('Prepared AP1/RDP1 configuration candidates; no runtime files changed')
D171_CONFIG_STAGE
```

The candidate validator is a local read-only subcommand, not a controller
launch. The source author has not executed this staging block against live
configuration. Keep the reviewed config snapshots private; no credential
value belongs in configuration deltas, source, mail or evidence.

## ROOT activation and recovery boundary

Use the already reviewed immutable-release and atomic same-filesystem
replacement procedure in D169 with this **current** selection table.
The pre-D169 selector hash and paths in its historical examples must not be
reused. Snapshot the fresh selected service drop-ins before changing them.

| Selection | Before expected executable digest | After expected executable digest |
| --- | --- | --- |
| Agentmail | `e59622ab50434ea87d5788e4898432d244e77cd926616e5fd7e3ed6dfd658439` | `6264b3d7640b20a024e9e5553eae36328405a79ee0c1281930e368c00419d105` |
| CodexPro | `03ac4b5ad13fd082027449e4e8a419f6999beaae0dd0fbbfbef8fd36b4f92b38` | `281bb8e671d66d97b0b94f5bb17db33f659c3865e8712e8bccecdc15a20271f9` |

1. ROOT rechecks controller cgroup/descendants, managed/native work and each
   affected worker's action boundary. Existing conversations, jobs, sockets,
   resource controls and delivery state remain in place.
2. Prepare a new immutable CodexPro release from the verified current D169
   payload/assets. Replace only its binary and source/manifest metadata.
   Use commit `819dd7d9d649a47fba7a0d5c61c099118b56bad2` and the corresponding
   `candidates[]` record in this build manifest. Preserve the existing helpers.
   Agentmail's target remains `/home/spite/Projects/agent-mailbox/bin/agentmail`;
   stage its new binary beside that target for an atomic replacement.
3. At ROOT's fresh controller custody boundary, stop only `agentmail.service`,
   atomically replace the verified prior executable, and start the controller.
   Verify `/proc/<MainPID>/exe` against the new Agentmail digest before continuing.
   This endpoint requires a controller restart; it has no binary hot reload.
4. Publish the reviewed companion atomically, only if the current bytes still
   equal `mail-grants.before.json`. The new schema must reach the matching new
   controller. Publish the pending env candidate with the same before-byte
   guard, preserving the existing socket and other service arguments.
5. Select the verified new immutable CodexPro release through its current
   service selector. At the agreed no-worker-mid-action boundary, reload the
   service manager and restart `codexpro.service`. Read back actual executing
   bytes, not merely a build file or process name.
6. ROOT coordinates the existing AP1/RDP1 owners and agreed SP for live
   next-call appearance, real owner pull, and next-call clearing. No new
   sessions, replay campaign or child-authored mail fixture is implied.

If acceptance fails, classify the actual result before further calls.
Withdrawing only these summary grants can leave the newer controller binary
running and avoid an unnecessary second controller transition. Missing notices
leave ordinary tool results and existing original delivery ownership intact.

If ROOT selects full D169 recovery, use the retained 6023fc3/25e1a886 artifacts
above and the captured fresh pre-D171 service selectors. **Before downgrading
Agentmail, remove the new `pending_summaries` field by restoring the guarded
pre-D171 companion**, or merge out only this change if another owner has since
edited it. The older strict parser rejects the new field and would otherwise
deny existing optional exchanges. Restore the pending env and CodexPro selector
at the coordinated action boundary, then restart only the selected units.
Every restore must compare current bytes with the exact D171 candidate first;
a concurrent change means stop/reconcile, never overwrite it blindly.

Keep the latest principals, credentials, database, journals, mailbox configuration,
delivery cursors and original receipt state. Retain both immutable releases and
all verified recovery artifacts. This handoff author performed no install,
restart, live fixture/probe, grant publication, credential change, or AP2 access.
