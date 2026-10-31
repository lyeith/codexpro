# First process contract replay: four failures

The unmodified first-run evidence remains in `process-contracts-1-failures.json`.
The lane reported six of ten scenarios passing and no cleanup errors. The four
failures have two distinct causes; no field comparison is relaxed by the fix.

## Genuine implementation difference: batch child wrapper

All three batch failures contained extra `codexpro_tool` and `codexpro_title`
fields in each child's structured result. The missing-file child additionally
used `[project:contract]` where the source used the enclosing workspace label.

In the exact source, `src/tools/context.ts` stores `validatedHandler` in the
registered handler map (lines 462–475), while its MCP audit/tagging/redaction
wrapper is registered separately. `src/tools/batch.ts` invokes that stored raw
handler (lines 421–430) and only then applies the child output budgets.

The candidate called the fully wrapped `Engine.Call` for every child. Besides
the observed public differences, that also created independent child audit
actions, attached/acknowledged background status and retook the enclosing lease.
The new `Engine.CallBatchChild` validates and calls the domain dispatcher under
the already-held outer lease. It preserves the enclosing audit action and the
distinct per-child request identity supplied by batch. It performs no additional
audit action, status attachment, path redaction or workspace execution wrapper.

The exact current `wire.Success` implementation (SHA-256
`ea2702cede0f41d79b15d183ab351a4bea0f208e91cae980885f17ab8962b961`)
adds the two presentation fields to structured data and passes the supplied text
through unchanged; it has no JSON fallback. The raw-child adapter removes only
those two manufactured top-level fields, before child structured budgeting. It
preserves every other result field and text byte. It does not toggle shared
`Files.TransformText`, so concurrent normal reads keep their existing behavior.

`batch/child.go` chooses that raw interface when the caller is an Engine. Apply
the single-call change in `go/internal/batch/execute.go`:

```go
raw, err := s.callChild(ctx, childScope, op.Tool, clone(op.Validated))
```

This replaces only `s.caller.Call` at that location. Keep the surrounding failure
handling, output budgets and settlement wait unchanged. New integration tests
assert exactly one audit action for an executed parallel batch, only outer
presentation tags, unchanged semantic child fields, raw inner validation, and
one final workspace-relative redaction for a failing child.

## Proven harness defect: old job identities in recovery details

The output-pinning scenario itself read its retained source correctly. Its final
unknown-input error returned the last ten workspace job IDs, exactly as the
source contract requires. Both sides listed the two current jobs followed by
the eight created in preceding scenarios. `Side.begin` had erased their earlier
symbolic bindings, leaving those eight legitimately different random IDs raw.

The corrected harness retains only identities already observed in a previous
scenario, qualified by that scenario. It never invents a mapping from
`known_job_ids` alone. All recovery-list counts, ordering, duplicates, unknown
identities and accompanying error text remain compared. No cleanup ownership is
inferred from a symbolic binding, and raw evidence is preserved unchanged.
Thirteen local harness guard tests pass, including two focused regressions for
cross-scenario binding and strict recovery-list comparison.

## Validation status

The Python harness tests have executed locally. Native Go tests and a fresh
paired replay must execute through the parent before these candidate fixes are
reported as passing. The previous raw result remains a failed run; it is not
rewritten or relabelled.
