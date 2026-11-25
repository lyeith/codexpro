# D169 paired pending-mail release

> Historical release and recovery record. For the current registration-based implementation, use [D176 activation](pending-mail-d176.md); its CodexPro process has no binding list.

This runbook prepares the CodexPro notice and Agentmail metadata endpoint as a
paired Go release. Source/build work is complete independently of runtime
activation. ROOT owns the fresh custody check, endpoint-first installation,
service restart, and live acceptance. Commands under **Future ROOT installation**
and **Recovery** have not been executed by the source author.

## D171 continuation supersedes the AP1 binding proposal below

The owner-only D169 pair was activated by ROOT. The live AP1 appearance check
failed: AP1 actually used the same `sp-fleet/sp-fleet` CodexPro transport as
RDP1, despite its separate Agentmail registration. Retain that failed result;
its later owner pull does not prove that a notice appeared or cleared.
David's Option B and ROOT's D171 instruction authorize only exact summary
metadata delegation for AP1 and RDP1. See
[pending-mail-delegation.md](pending-mail-delegation.md) for the continuation,
current source/binary selection, configuration deltas and recovery boundary.
The older AP1 owner binding and pre-D169 service-selector hashes below are
historical records, not current installation inputs.

For current Go installation and recovery, follow [Go deployment](README.md)
and Agentmail's `docs/delivery-mode-cutover.md`. Preserve the latest policy,
credentials, registrations, sockets, databases, jobs, queue/receipt state, and
conversations. Do not invoke the historical TypeScript cutover/rollback scripts,
restore an older database, run a second Agentmail controller on its live state,
or stop a managed native session to refresh its client.

## Pinned source and artifacts

The binaries are built from these source revisions; a later documentation-only
commit does not change their source identity.

| Artifact | Source revision | SHA-256 |
| --- | --- | --- |
| CodexPro candidate | `6023fc3999bf472ee408e03998fde16e07162725` | `03ac4b5ad13fd082027449e4e8a419f6999beaae0dd0fbbfbef8fd36b4f92b38` |
| Agentmail combined candidate | `25e1a886e6f1135554004f4c5b1623544da5cdf4` | `e59622ab50434ea87d5788e4898432d244e77cd926616e5fd7e3ed6dfd658439` |
| Agentmail recovery candidate retaining Claude fixes | `9c4af3ddc2d84ba9d3e775f3f288f17708a05107` | `ac66de3f9f65e2b8e1564e4f88cdcb3afffbdf4a16793a1e20ae8e9635e41134` |
| Retained running CodexPro release | `14030689304c262e8aba9dea0a207cc9d81f4cf0` | `b3061618495dd9dd3701787c653501e590768e015b606134cdc147de18cb83e2` |
| Observed running Agentmail before this release | `09189be99ae2da16cae0041641deaeb4bc8fa404` | `dc0d2e21ccb78125931d97b2c3fbe5ed82edf4351b65b584b5e4b139c644cac5` |

Staged binaries and `manifest.json` are under
`/home/spite/Projects/sp9-r894-mail-release-build-20261117`:

- `codexpro-go-6023fc3999`
- `agentmail-25e1a886e6`
- `agentmail-rollback-9c4af3ddc2`

The combined Agentmail commit merges feature `be5ceec46609fea782f7b7ee932a71c37e6a390d`
with main `9c4af3ddc2d84ba9d3e775f3f288f17708a05107`. Both are ancestors.
It retains the two later Claude fixes: peer-transport input correlation
(`b9a050d`) and bounded legacy-cursor rechecking (`9c4af3d`). The recovery
candidate retains both fixes too. The observed `09189be9` binary is retained
for custody, not selected here as a downgrade target.

CodexPro embeds the exact source revision and `vcs.modified=false`.
Both Agentmail binaries omit embedded VCS metadata with this installed Go 1.26.8:
`/usr/lib/go-1.26/src/cmd/go/internal/vcs/vcs.go` declares Git's `.git`
root with `isDir: true`, while the isolated author has a normal Git worktree
pointer file. `isVCSRoot` requires that file-type match. Provenance therefore
uses the verified clean source/ancestry, exact author/CWD, compiler/flags, retained
build output, and binary digest recorded in the manifest. No source
instrumentation or candidate rebuild was introduced to manufacture a stamp.

The unrelated dirty TypeScript checkout `/home/spite/Projects/codexpro` is not
the deployed Go source. Its `src/work/runtime.ts` and
`test/work-http.test.mjs` changes remain untouched. Agentmail canonical main
also remains untouched.

## Reproduce the non-live builds

Use only the two isolated authors below, with no concurrent editor/build in
either author. This command refuses dirty authors, temporarily selects exact
commits, restores each prior branch/HEAD in `finally`, creates a fresh private
output directory, and never launches either service. It uses already-installed
compilers directly: selecting a toolchain module while setting
`GOSUMDB=off` fails verification, so `GOTOOLCHAIN=local` is deliberate.
Expected digest mismatch is a stop/report condition, not permission to update
the accepted digest.

The resource owner is the existing `plans-heavy` command. The fixed compiler,
GCC identity, environment, flags, source trees, and full `go version -m`
readback are also retained in the original manifest. The compiler used
`gcc (Ubuntu 15.2.0-16ubuntu1) 15.2.0` at `/usr/bin/gcc`.

```sh
/home/spite/.local/bin/plans-heavy run -- uv run --no-project --offline python - <<'D169_BUILD'
import hashlib, os, pathlib, subprocess
base = pathlib.Path('/home/spite/Projects')
out = base / 'sp9-r894-mail-rebuild-20261117'
out.mkdir(mode=0o700)  # refuse an existing output directory
cp = base / 'codexpro-sp9-r894-pending-mail-20261117'
am = base / 'agent-mailbox-sp9-r894-pending-summary-20261117'
cases = [
    (cp, 'go', '6023fc3999bf472ee408e03998fde16e07162725',
     '/var/lib/ssd-dev/data/managed/tool-cache/go-mod/golang.org/toolchain@v0.0.1-go1.27.3.linux-amd64',
     'go1.27.3', './cmd/codexpro', 'codexpro-go-6023fc3999',
     '03ac4b5ad13fd082027449e4e8a419f6999beaae0dd0fbbfbef8fd36b4f92b38'),
    (am, '', '25e1a886e6f1135554004f4c5b1623544da5cdf4',
     '/usr/lib/go-1.26', 'go1.26.8', './cmd/agentmail',
     'agentmail-25e1a886e6', 'e59622ab50434ea87d5788e4898432d244e77cd926616e5fd7e3ed6dfd658439'),
    (am, '', '9c4af3ddc2d84ba9d3e775f3f288f17708a05107',
     '/usr/lib/go-1.26', 'go1.26.8', './cmd/agentmail',
     'agentmail-rollback-9c4af3ddc2', 'ac66de3f9f65e2b8e1564e4f88cdcb3afffbdf4a16793a1e20ae8e9635e41134'),
]
def capture(args, **kw):
    return subprocess.check_output(args, text=True, timeout=30, **kw).strip()
for repo, subdir, rev, goroot, version, target, name, expected in cases:
    assert not capture(['git', 'status', '--porcelain=v1'], cwd=repo)
    branch = subprocess.run(['git', 'symbolic-ref', '--quiet', '--short', 'HEAD'],
                            cwd=repo, text=True, capture_output=True)
    previous = branch.stdout.strip() if branch.returncode == 0 else capture(
        ['git', 'rev-parse', 'HEAD'], cwd=repo)
    env = dict(os.environ, GOTOOLCHAIN='local', GOROOT=goroot, GOMAXPROCS='2',
               CGO_ENABLED='1', GOOS='linux', GOARCH='amd64', GOAMD64='v1',
               GOFLAGS='', GOEXPERIMENT='', GOPROXY='off', GOSUMDB='off',
               CC='/usr/bin/gcc', CGO_CFLAGS='-O2 -g', CGO_CPPFLAGS='',
               CGO_CXXFLAGS='-O2 -g', CGO_FFLAGS='-O2 -g', CGO_LDFLAGS='-O2 -g')
    go = str(pathlib.Path(goroot) / 'bin/go')
    assert capture([go, 'version'], env=env) == f'go version {version} linux/amd64'
    try:
        subprocess.run(['git', 'switch', '--detach', rev], cwd=repo, check=True)
        assert capture(['git', 'rev-parse', 'HEAD'], cwd=repo) == rev
        assert not capture(['git', 'status', '--porcelain=v1'], cwd=repo)
        binary = out / name
        subprocess.run(['nice', '-n', '10', go, 'build', '-p', '2', '-trimpath',
                        '-buildvcs=true', '-ldflags=-buildid=', '-o', str(binary),
                        target], cwd=repo/subdir, env=env, check=True, timeout=600)
        assert not capture(['git', 'status', '--porcelain=v1'], cwd=repo)
        digest = hashlib.sha256(binary.read_bytes()).hexdigest()
        assert digest == expected, (name, digest, expected)
        binary.chmod(0o555)
        print(name, rev, digest)
        print(capture([go, 'version', '-m', str(binary)], env=env))
    finally:
        subprocess.run(['git', 'switch'] + ([previous] if branch.returncode == 0
                       else ['--detach', previous]), cwd=repo, check=True)
D169_BUILD
```

Focused source checks (already collected; these commands describe the checks,
not a request to repeat passing tests):

```sh
# CodexPro source author/go, Go 1.27.3:
go test -p 2 ./internal/config ./internal/transport \
  -run 'Test(PendingMail|LogicalMail)' -count=1
go test -race -p 2 ./internal/transport \
  -run 'Test(PendingMail|LogicalMail)' -count=1

# Combined Agentmail source author, Go 1.26.8:
go test -p 2 ./internal/store ./internal/api ./internal/adapters/claude \
  -run 'Test(PendingSummary|Logical|RoleScopedHTTP|MailAlias|InputReceipt|ClaudeInputReceipt|ClaudeReceipt)' \
  -count=1
```

## Binding and authorization selection

The proposal below is explicit configuration, not prefix inference. Source types
were non-secret Plans Access policy `agents[]` registration metadata in
`/home/spite/.config/plans-access/principals.json` and selected Agentmail
`mailboxes` configuration rows from the database actually opened by the
controller (read-only/query-only access). No message or session data was used.

| Labels | Principal | Registered agent | Canonical mailbox | Logical selection |
| --- | --- | --- | --- | --- |
| AP1 | plans-system | android-ap1 | android/ap1 | AP1 |
| SP1, SP2, SP3, SP4, SP5, SP6, SP7, SP8, SP9, SP10, SP11, SP12, SP13 | sp-fleet | sp-fleet | plans/sp-fleet | Exact listed SP label |

Those registrations have active `both` audience eligibility, and the exact
logical recipients are configured on their pull mailboxes. AP1's selection is
explicitly `AP1`, not an empty ordinary-mail selection. AP2 is excluded.

The new endpoint is
`GET /v1/mailboxes/pending-summary?address=...&logical_recipient=...`.
Existing named policy/credential-pin authentication applies. The requester must
be the canonical mailbox owner; aliases, host/legacy scope, manager visibility,
other owners, and unconfigured logical selections are refused. The selected
pull mailbox and logical configuration are validated in the same read
transaction as the exact pending count and latest canonical sender. Empty
logical selection means only ordinary mail. There is no body scan/projection,
receipt/ACK, cursor update, delivery retry, or service wake.

CodexPro forwards the current call's bearer and credential pin only when the
authenticated principal and agent match the explicit startup binding. It caches
routing parsing briefly, but revalidates metadata on every named call; zero
counts cannot hide new mail until a TTL expires. The notice comes before
unchanged original delivery preparation so the original envelope remains last
and its existing receipt observer keeps ownership. No credential is copied into
this binding file.

### RDP1 remains an explicit completion gap

ROOT confirms RDP1 calls CodexPro through the shared `sp-fleet/sp-fleet`
transport. Its own Agentmail registration is
`plans-system/rdp-rdp1`, mailbox `rdp/rdp1`, logical recipient `RDP1`,
with Agentmail-only audience. The shared bearer cannot use this owner-only
endpoint for that mailbox. A named label is not authority, and an RDP1 binding
must not be enabled by relabelling the SP owner.

Two possible authority decisions are concrete, neither is implemented:

1. Authorize a dedicated RDP named CodexPro connection bound to the existing
   `plans-system/rdp-rdp1` owner, using a credential accepted by both services.
   Confirm that RDP's actual connector/connection can use it while preserving
   the conversation. A CodexPro-only audience is insufficient because the same
   bearer is forwarded to Agentmail. This leaves endpoint ownership unchanged.
2. Authorize a narrow summary-only delegation from actual caller
   `sp-fleet/sp-fleet` to exactly `rdp/rdp1` + `RDP1`. This needs policy and
   source changes plus AUTH/privacy review and refusal tests. It must not alter
   mailbox ownership, general read/receipt/send/task/lifecycle grants or use
   RDP's credential. Any holder of the shared SP registration could request the
   allowed metadata; the label cannot distinguish one conversation. ROOT must
   explicitly accept that scope.

No existing owner-only route bridges that mismatch. SP/AP source acceptance
does not establish complete D169 coverage, and separate RDP Agentmail pulls do
not satisfy an every-CodexPro-result notice requirement.

## Future ROOT installation

These are reviewable commands for the current observed SSD topology. ROOT must
recheck source selection, process/cgroup custody, nonterminal managed native
work, and worker action boundaries immediately before transition. Earlier
observations are not permission to skip the fresh check. Keep existing socket,
tunnel, service arguments, resource controls, policy and credentials in place.

The live CodexPro argv0 and WorkingDirectory point directly at
`/home/spite/apps/codexpro-go/releases/1403068930`; there is no `current`
symlink. The selector is the existing
`~/.config/systemd/user/codexpro.service.d/90-codexpro-go-release.conf`, SHA-256
`3a1a620381922f63dcec0a8ac2bb09dd28869f224efd9f407ec9f5c1e0db2be7`.
It contains exactly two references to that release path. A stale guard below
means stop/reconcile, not overwrite a newer deployment.

### 1. Prepare immutable CodexPro payload and Agentmail replacement

This preparatory block makes no service call. It verifies the accepted hashes,
copies only the verified prior release payload, replaces the binary and source
manifest, and stages the Agentmail executable beside its actual target for an
atomic same-filesystem replacement. It retains the original Agentmail bytes
for custody. Existing helper/assets are unchanged by the CodexPro feature.

```sh
uv run --no-project --offline python - <<'D169_STAGE'
import hashlib, json, os, pathlib, shutil, subprocess
stage = pathlib.Path('/home/spite/Projects/sp9-r894-mail-release-build-20261117')
old = pathlib.Path('/home/spite/apps/codexpro-go/releases/1403068930')
new = old.parent / '6023fc3999-r894'
tmp = old.parent / '.staging-6023fc3999-r894'
cp_sha = '6023fc3999bf472ee408e03998fde16e07162725'
digests = {
    'codexpro-go-6023fc3999': '03ac4b5ad13fd082027449e4e8a419f6999beaae0dd0fbbfbef8fd36b4f92b38',
    'agentmail-25e1a886e6': 'e59622ab50434ea87d5788e4898432d244e77cd926616e5fd7e3ed6dfd658439',
    'agentmail-rollback-9c4af3ddc2': 'ac66de3f9f65e2b8e1564e4f88cdcb3afffbdf4a16793a1e20ae8e9635e41134',
}
def digest(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()
def exclusive(p, data, mode):
    with p.open('xb') as f:
        os.fchmod(f.fileno(), mode)
        f.write(data)
        f.flush()
        os.fsync(f.fileno())
def replace(p, data, mode):
    candidate = p.with_name(p.name + '.r894-new')
    exclusive(candidate, data, mode)
    os.replace(candidate, p)
for name, wanted in digests.items():
    assert digest(stage/name) == wanted, name
assert not new.exists() and not tmp.exists()
old_manifest = json.loads((old/'manifest.json').read_text())
assert old_manifest['source_commit'] == '14030689304c262e8aba9dea0a207cc9d81f4cf0'
assert not any(p.is_symlink() for p in old.rglob('*'))
actual = {str(p.relative_to(old)) for p in old.rglob('*') if p.is_file()}
assert actual == set(old_manifest['files']) | {'manifest.json'}
for name, meta in old_manifest['files'].items():
    relative = pathlib.PurePosixPath(name)
    assert not relative.is_absolute() and '..' not in relative.parts
    assert digest(old/name) == meta['sha256'], name
assert digest(old/'codexpro-go') == 'b3061618495dd9dd3701787c653501e590768e015b606134cdc147de18cb83e2'
shutil.copytree(old, tmp)
tmp.chmod(0o700)
replace(tmp/'codexpro-go', (stage/'codexpro-go-6023fc3999').read_bytes(), 0o500)
replace(tmp/'SOURCE_REVISION', (cp_sha+'\n').encode(), 0o444)
build = next(x for x in json.loads((stage/'manifest.json').read_text())['builds']
             if x['name'] == 'codexpro-go-6023fc3999')
manifest = {
    'schema_version': 1, 'source_commit': cp_sha, 'go_tree': build['source_tree'],
    'build_command': 'nice -n 10 go build -p 2 -trimpath -buildvcs=true -ldflags=-buildid= -o OUTPUT ./cmd/codexpro (in go/)',
    'go_version_m': build['build_info'],
    'build_environment': build['environment'],
    'preserved_assets_from': old_manifest['source_commit'],
    'ast_grep': old_manifest['ast_grep'],
    'host_resource_helper': old_manifest['host_resource_helper'],
    'files': {str(p.relative_to(tmp)): {'sha256': digest(p), 'bytes': p.stat().st_size}
              for p in sorted(tmp.rglob('*')) if p.is_file() and p.name != 'manifest.json'},
}
replace(tmp/'manifest.json', (json.dumps(manifest, indent=2)+'\n').encode(), 0o444)
tmp.chmod(0o555)
os.rename(tmp, new)
target = pathlib.Path('/home/spite/Projects/agent-mailbox/bin/agentmail')
assert digest(target) == 'dc0d2e21ccb78125931d97b2c3fbe5ed82edf4351b65b584b5e4b139c644cac5'
exclusive(stage/'agentmail-retained-09189be99a', target.read_bytes(), 0o500)
exclusive(target.parent/'.agentmail-r894-new',
          (stage/'agentmail-25e1a886e6').read_bytes(), 0o700)
print('Prepared immutable release', new)
print('Prepared Agentmail sibling replacement; no services changed')
D169_STAGE
```

### 2. Install Agentmail first at ROOT's freshly checked boundary

The endpoint is part of the controller and requires an actual controller
restart; there is no endpoint hot reload. ROOT must confirm that stopping this
unit will not terminate live managed/native work. Stop only the controller
through the existing service manager; never issue a native-session stop.
After the swap, verify actual executing bytes before beginning CodexPro
activation. If any guard fails, retain the stopped/held state and classify it.

```sh
set -euo pipefail
systemctl --user stop agentmail.service
test "$(systemctl --user show agentmail.service -p MainPID --value)" = 0
uv run --no-project --offline python - <<'D169_AGENTMAIL_SWAP'
import hashlib, os, pathlib
target = pathlib.Path('/home/spite/Projects/agent-mailbox/bin/agentmail')
candidate = target.parent/'.agentmail-r894-new'
assert hashlib.sha256(target.read_bytes()).hexdigest() == 'dc0d2e21ccb78125931d97b2c3fbe5ed82edf4351b65b584b5e4b139c644cac5'
assert hashlib.sha256(candidate.read_bytes()).hexdigest() == 'e59622ab50434ea87d5788e4898432d244e77cd926616e5fd7e3ed6dfd658439'
os.replace(candidate, target)
directory = os.open(target.parent, os.O_RDONLY | os.O_DIRECTORY)
try:
    os.fsync(directory)
finally:
    os.close(directory)
D169_AGENTMAIL_SWAP
systemctl --user start agentmail.service
systemctl --user is-active agentmail.service
uv run --no-project --offline python - <<'D169_AGENTMAIL_READBACK'
import hashlib, pathlib, subprocess
pid = subprocess.check_output(['systemctl', '--user', 'show', 'agentmail.service',
                               '-p', 'MainPID', '--value'], text=True).strip()
assert int(pid) > 0
actual = hashlib.sha256((pathlib.Path('/proc')/pid/'exe').read_bytes()).hexdigest()
assert actual == 'e59622ab50434ea87d5788e4898432d244e77cd926616e5fd7e3ed6dfd658439', actual
print('Agentmail executing accepted candidate', pid, actual)
D169_AGENTMAIL_READBACK
```

### 3. Select CodexPro and startup-only bindings

Execute only after the endpoint owner is installed and ROOT has established the
CodexPro worker action boundary. This preserves every existing ExecStart
argument and resource drop-in. It substitutes only the two pinned release paths
in the existing selector, and adds an independent owner-only EnvironmentFile
containing non-secret routing metadata. Existing original-mail settings remain
unchanged. The current inspection found no pending-mail startup settings.

```sh
set -euo pipefail
uv run --no-project --offline python - <<'D169_CODEXPRO_SELECT'
import hashlib, json, os, pathlib
stage = pathlib.Path('/home/spite/Projects/sp9-r894-mail-release-build-20261117')
dropins = pathlib.Path('/home/spite/.config/systemd/user/codexpro.service.d')
selector = dropins/'90-codexpro-go-release.conf'
notice = dropins/'91-codexpro-pending-mail.conf'
envfile = pathlib.Path('/home/spite/.config/codexpro/pending-mail-r894.env')
old = '/home/spite/apps/codexpro-go/releases/1403068930'
new = '/home/spite/apps/codexpro-go/releases/6023fc3999-r894'
original = selector.read_bytes()
assert hashlib.sha256(original).hexdigest() == '3a1a620381922f63dcec0a8ac2bb09dd28869f224efd9f407ec9f5c1e0db2be7'
assert original.decode().count(old) == 2
assert not notice.exists() and not envfile.exists()
assert hashlib.sha256((pathlib.Path(new)/'codexpro-go').read_bytes()).hexdigest() == '03ac4b5ad13fd082027449e4e8a419f6999beaae0dd0fbbfbef8fd36b4f92b38'
labels = ['SP1', 'SP2', 'SP3', 'SP4', 'SP5', 'SP6', 'SP7', 'SP8',
          'SP9', 'SP10', 'SP11', 'SP12', 'SP13']
bindings = [{'label': label, 'principal': 'sp-fleet', 'agent': 'sp-fleet',
             'mailbox': 'plans/sp-fleet', 'logical_recipient': label}
            for label in labels]
bindings.append({'label': 'AP1', 'principal': 'plans-system',
                 'agent': 'android-ap1', 'mailbox': 'android/ap1',
                 'logical_recipient': 'AP1'})
def exclusive(p, data):
    with p.open('xb') as f:
        os.fchmod(f.fileno(), 0o600)
        f.write(data)
        f.flush()
        os.fsync(f.fileno())
envfile.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
payload = ("CODEXPRO_PENDING_MAIL_SOCKET=/home/spite/.local/state/agentmail/agentmail.sock\n"
           "CODEXPRO_PENDING_MAIL_BINDINGS='" +
           json.dumps(bindings, separators=(',', ':')) + "'\n")
exclusive(stage/'90-codexpro-go-release.before-r894.conf', original)
exclusive(envfile, payload.encode())
exclusive(notice, ('[Service]\nEnvironmentFile='+str(envfile)+'\n').encode())
candidate = selector.with_name(selector.name+'.r894-new')
exclusive(candidate, original.decode().replace(old, new).encode())
os.replace(candidate, selector)
directory = os.open(dropins, os.O_RDONLY | os.O_DIRECTORY)
try:
    os.fsync(directory)
finally:
    os.close(directory)
print('Selected exact candidate and SP/AP bindings; no RDP1 or AP2 binding')
D169_CODEXPRO_SELECT
systemctl --user daemon-reload
systemctl --user restart codexpro.service
systemctl --user is-active codexpro.service
```

### 4. Read back the running executables and complete live acceptance

```sh
uv run --no-project --offline python - <<'D169_PROCESS_READBACK'
import hashlib, os, pathlib, subprocess
for unit, expected in [
    ('agentmail.service', 'e59622ab50434ea87d5788e4898432d244e77cd926616e5fd7e3ed6dfd658439'),
    ('codexpro.service', '03ac4b5ad13fd082027449e4e8a419f6999beaae0dd0fbbfbef8fd36b4f92b38'),
]:
    pid = subprocess.check_output(['systemctl', '--user', 'show', unit,
                                   '-p', 'MainPID', '--value'], text=True).strip()
    assert int(pid) > 0
    exe = pathlib.Path('/proc')/pid/'exe'
    actual = hashlib.sha256(exe.read_bytes()).hexdigest()
    assert actual == expected, (unit, actual)
    print(unit, pid, os.readlink(exe), actual)
D169_PROCESS_READBACK
```

That readback proves which executable runs, not mail acceptance. ROOT separately
owns the agreed active AP1 and SP acceptance: new addressed mail appears within
the next named tool result, counts/latest canonical sender match the selected
mailbox, original envelopes and existing receipts remain intact, and the notice
clears on the next call after the ordinary owner pull. Do not send fixture mail
to live sessions without the agreed recipient/authority. Do not probe AP2.
The RDP1 authority gap above remains open even when SP/AP acceptance passes.

Normal MCP HTTP sessions may reinitialize after restart. Preserve the existing
conversation and recover its owned job/result IDs; reacquire stale edit tags.
Do not create replacement conversations or rerun accepted work to manufacture
a successful restart result.

## Recovery

ROOT classifies a failure before selecting recovery. This change adds no
Agentmail schema migration or new durable write, but the source tests/builds
are not an OLD/NEW/OLD live recovery proof. ROOT retains the policy compatibility
decision and latest state. Do not use an older policy/DB/job snapshot.

The narrow recovery is to remove the notice startup selection and restore the
retained CodexPro release; the new Agentmail endpoint can remain installed and
unused. Keep the newest Agentmail binary if doing so avoids an unnecessary
second controller transition. These guards deliberately stop if another
deployment has changed the selector.

At the same freshly checked CodexPro action boundary:

```sh
set -euo pipefail
uv run --no-project --offline python - <<'D169_CODEXPRO_RECOVER'
import hashlib, os, pathlib
stage = pathlib.Path('/home/spite/Projects/sp9-r894-mail-release-build-20261117')
dropins = pathlib.Path('/home/spite/.config/systemd/user/codexpro.service.d')
selector = dropins/'90-codexpro-go-release.conf'
notice = dropins/'91-codexpro-pending-mail.conf'
old = '/home/spite/apps/codexpro-go/releases/1403068930'
new = '/home/spite/apps/codexpro-go/releases/6023fc3999-r894'
original = (stage/'90-codexpro-go-release.before-r894.conf').read_bytes()
assert hashlib.sha256(original).hexdigest() == '3a1a620381922f63dcec0a8ac2bb09dd28869f224efd9f407ec9f5c1e0db2be7'
assert selector.read_bytes() == original.decode().replace(old, new).encode()
assert notice.read_text() == '[Service]\nEnvironmentFile=/home/spite/.config/codexpro/pending-mail-r894.env\n'
assert hashlib.sha256((pathlib.Path(old)/'codexpro-go').read_bytes()).hexdigest() == 'b3061618495dd9dd3701787c653501e590768e015b606134cdc147de18cb83e2'
held = notice.with_name(notice.name+'.held-r894')
assert not held.exists()
candidate = selector.with_name(selector.name+'.r894-recover')
with candidate.open('xb') as f:
    os.fchmod(f.fileno(), 0o600)
    f.write(original)
    f.flush()
    os.fsync(f.fileno())
os.replace(candidate, selector)
notice.rename(held)  # retained; no longer has a .conf suffix
directory = os.open(dropins, os.O_RDONLY | os.O_DIRECTORY)
try:
    os.fsync(directory)
finally:
    os.close(directory)
D169_CODEXPRO_RECOVER
systemctl --user daemon-reload
systemctl --user restart codexpro.service
```

If ROOT specifically selects Agentmail binary recovery, use the built
`9c4af3ddc2` candidate, which retains both Claude fixes. Do not substitute the
older observed `09189be9` binary. Apply the same freshly checked controller
custody boundary; keep the latest database, journal, policy and registrations.

```sh
set -euo pipefail
uv run --no-project --offline python - <<'D169_AGENTMAIL_RECOVERY_STAGE'
import hashlib, os, pathlib
stage = pathlib.Path('/home/spite/Projects/sp9-r894-mail-release-build-20261117')
binary = stage/'agentmail-rollback-9c4af3ddc2'
target = pathlib.Path('/home/spite/Projects/agent-mailbox/bin/agentmail')
assert hashlib.sha256(binary.read_bytes()).hexdigest() == 'ac66de3f9f65e2b8e1564e4f88cdcb3afffbdf4a16793a1e20ae8e9635e41134'
assert hashlib.sha256(target.read_bytes()).hexdigest() == 'e59622ab50434ea87d5788e4898432d244e77cd926616e5fd7e3ed6dfd658439'
candidate = target.parent/'.agentmail-r894-recover'
with candidate.open('xb') as f:
    os.fchmod(f.fileno(), 0o700)
    f.write(binary.read_bytes())
    f.flush()
    os.fsync(f.fileno())
D169_AGENTMAIL_RECOVERY_STAGE
systemctl --user stop agentmail.service
test "$(systemctl --user show agentmail.service -p MainPID --value)" = 0
uv run --no-project --offline python - <<'D169_AGENTMAIL_RECOVERY_SWAP'
import hashlib, os, pathlib
target = pathlib.Path('/home/spite/Projects/agent-mailbox/bin/agentmail')
candidate = target.parent/'.agentmail-r894-recover'
assert hashlib.sha256(candidate.read_bytes()).hexdigest() == 'ac66de3f9f65e2b8e1564e4f88cdcb3afffbdf4a16793a1e20ae8e9635e41134'
assert hashlib.sha256(target.read_bytes()).hexdigest() == 'e59622ab50434ea87d5788e4898432d244e77cd926616e5fd7e3ed6dfd658439'
os.replace(candidate, target)
directory = os.open(target.parent, os.O_RDONLY | os.O_DIRECTORY)
try:
    os.fsync(directory)
finally:
    os.close(directory)
D169_AGENTMAIL_RECOVERY_SWAP
systemctl --user start agentmail.service
```

Use the process readback block with expected CodexPro digest
`b3061618495dd9dd3701787c653501e590768e015b606134cdc147de18cb83e2`
and, only if Agentmail was recovered, expected Agentmail digest
`ac66de3f9f65e2b8e1564e4f88cdcb3afffbdf4a16793a1e20ae8e9635e41134`. Re-establish the actual named-policy and receipt
contract through the existing owners before releasing the hold. Do not remove
prior release directories, the staged candidates, retained original bytes, or
the recovery selector while accepted processes may still refer to them.
