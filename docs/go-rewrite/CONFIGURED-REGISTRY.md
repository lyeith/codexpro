# Startup capability filtering

`contracts.NewConfigured` combines the existing frozen inventory variants with
the capability switches accepted by startup configuration. `New` and
`NewVariant` retain their existing behavior and descriptors.

Replace the engine's `NewVariant` call, after workspace-manager construction,
with the equivalent of:

```go
registry, err := contracts.NewConfigured(contracts.ConfiguredOptions{
    WorktreeMode:    workspaces.Mode(),
    ToolMode:        cfg.String("toolMode", "standard"),
    ProjectCount:    len(workspaces.Projects()),
    AnalysisEnabled: cfg.Bool("analysisEnabled", true),
    AuditMode:       cfg.String("auditMode", "metadata"),
    ConnectionTest:  cfg.Bool("connectionTest", false),
})
```

`AnalysisEnabled` is deliberately explicit. An empty `AuditMode` selects
`metadata`. Invalid audit/worktree/tool selectors and negative project counts
return errors. Permission modes are not configurable through this API: Bash
remains full and writes remain workspace writes. The persistent project catalog
and historical `work_status` reader remain configured. All eleven approved
retired names stay absent.

The gates come from `src/tools/registry.ts` at reference commit `44bb76b`:

| Startup condition | Additional inventory effect |
| --- | --- |
| Analysis disabled | Hide `inspect_workspace`. |
| Audit off | Hide `activity_list`, `activity_get`, `activity_status`, `activity_export`. |
| Connection test | Hide the ten retained tools marked `connectionTestHidden`: `create_project`, `codexpro_self_test`, `write`, `edit`, `apply_patch`, `import_file`, `start_jobs`, `stop_jobs`, `bash`, `commit_changes`. |
| Connection test, direct workspace, at most one project | Also hide `list_projects`: project creation is disabled, and neither the worktree nor multi-project alternative requirement holds. |

The source leaves `jobs`, `batch`, and managed-workspace lifecycle descriptors
available during a connection test. This implementation keeps that distinction;
it does not infer hiding from the `mutating` annotation. Batch still needs its
ordinary per-child registration check before validation: an unavailable child
must return `batch_child_not_allowed` with `Tool NAME is not available in the
current CodexPro mode.`, as in the source handler. The engine's existing validator
adapter should preserve that behavior when its registry lookup is absent.

Filtering constructs a fresh `Registry`. Its listing, direct tool lookup and
inner validation therefore agree. Every retained raw JSON descriptor, input
schema, initialization field and relative registration order is preserved.
These are startup registration choices; saving a future profile does not mutate
the already-constructed registry.

## Independent source oracle

`capture_registry_capabilities.mjs` imports the verified reference TypeScript
registry directly using Node's native TypeScript support. It evaluates the
reference `toolNamesForMode` over 144 combinations: two workspace modes, three
inventory tiers, zero/one/two projects, both analysis settings, both audit modes,
and both connection-test settings. It imports no Go policy and contains no
expected tool-name list. The source SHA-256 is
`12bc54021a1ba8282e886b01e4d753bfac9c27adb2a53f3647151a485d4b795c`.

The generated oracle includes the complete reference descriptor-policy table,
input options, raw reference availability results, source hash, source commit
and Node version. The tests apply the retirement list from `APPROVAL.md`
independently. Source work is enabled for this evaluation so `work_status` stays
present as the approved historical reader; active work-management descriptors
are then removed by the approval list.

The policy table's iteration order differs from the actual captured TypeScript
registration order, notably for Bash and work tools. Availability is checked
against the evaluated reference function; order and descriptor bytes are checked
against the captured `tools/list` variants. The two full-profile membership sets
were cross-checked locally and agree with their raw captures (34 direct/full,
33 managed/standard source tools before approved retirements).

Regenerate an oracle into a fresh path with:

```bash
node docs/go-rewrite/capture_registry_capabilities.mjs \
  src/tools/registry.ts /tmp/sp6-registry-capabilities-ATTEMPT.json
```

The checked-in test fixture belongs at
`go/internal/contracts/testdata/registry-capability-oracle.json`. Run the Go
verification under the shared-host bounds:

```bash
nice -n 10 env GOMAXPROCS=2 go test -count=1 -timeout=30s \
  ./internal/contracts -run '^TestConfigured'
```

The oracle generation and source/capture membership check passed locally. The
new Go tests have not yet run; the remote connector was unavailable when this
change was staged.
