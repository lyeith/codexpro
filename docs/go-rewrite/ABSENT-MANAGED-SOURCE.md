# Catalog capture when the managed source root is absent

The current live runtime was observed in `worktreeMode=off`, with no custom managed-root setting and no managed worktree directory. This is evidence for an absent managed store, not a source of historical lease fixtures. The actual catalog can still be preserved and verified independently.

The capture helper previously required an existing managed root and `leases/` directory. It now accepts a missing final root component only when explicitly passed `--allow-absent-managed-root`. Its parent must exist. The helper records that the root is absent, copies and re-verifies the real catalog, then checks continued absence and the same parent filesystem identity. A root that appears, or a parent that changes even if the root disappears again, causes capture to fail. Existing incomplete stores and broken symlinks remain errors; the flag cannot convert them into empty history.

Only the new private snapshot receives an empty `worktrees/leases` directory so the normal archive reader can process a zero-lease catalog snapshot. The capture manifest explicitly records `source_managed_root_absent:true`, zero source lease documents, the before/after absence observations, an empty snapshot layout, and `source_directory_created:false`. It creates no connector identity or synthetic lease. The native proof carries the absence evidence into its own receipt and requires zero imported lease documents and no imported connector identity. It still compares the actual catalog bytes, imports twice idempotently and uses the normal read-only Go archive reader.

After substituting the exact catalog path observed in the live runtime and fresh private attempt paths, run from the rewrite worktree:

```sh
nice -n 10 timeout -k 5s 30s python3 docs/go-rewrite/capture_legacy_workspaces.py \
  --catalog-file EXACT_OBSERVED_SOURCE_CATALOG \
  --managed-root /home/spite/.codexpro/worktrees \
  --allow-absent-managed-root \
  --snapshot-root /home/spite/.codexpro-go-proof/catalog-ATTEMPT \
  --archive-dir /home/spite/.codexpro-go-proof/catalog-archive-ATTEMPT
```

Then run the opt-in native archive gate from the Go module:

```sh
nice -n 10 timeout -k 10s 150s env GOMAXPROCS=2 \
  CODEXPRO_LEGACY_WORKSPACE_PLAN=/home/spite/.codexpro-go-proof/catalog-ATTEMPT/probe-plan.json \
  go test ./internal/legacyworkspace -run '^TestCapturedLegacyWorkspaceSnapshot$' -count=1 -timeout=130s -v
```

Report a passing result as actual catalog preservation with verified absence of managed source state and zero lease adoptions. It is not evidence of historical or active lease migration. The separate source-schema fixtures and managed lifecycle runtime lanes retain their distinct purposes.
