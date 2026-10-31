# Batch compatibility

`Service.Call` separates preflight from effects. It resolves and validates every
operation, including operations skipped by a stored-file resume, before it
touches the batch directory, Git exclude file, or any child tool. Preflight
errors are returned as ordinary error results so the caller can durably record
that the failed attempt had no child effects. Errors after persistence begins
propagate to the engine's uncertainty handling.

The registered catalog validates child arguments. The batch service additionally
enforces the legacy cross-operation rules: credentials only on the outer call,
distinct canonical targets for write/edit, exclusive apply_patch mutations,
serial mutations and verification, verification after all file mutations, and
parallel execution only for the six read-only tools. Parallel fan-out is bounded
by the existing twelve-operation input limit. Results preserve input ordering.

Stored JSON remains directly compatible with `.codexpro-batches/` files. Only
validated, explicitly supplied arguments are saved; injected workspace and
session values never enter the definition. Verification forces `on_timeout` to
`kill`. The shared file service performs writes and shares canonical mutation
locks with tagged edits. Automatic retention preserves the resumed file and the
newest twenty automatic definitions; custom names and symbolic links are not
retention candidates. Retention takes candidate locks and recomputes the newest
set after acquisition, preserving a file edited while it waited.

The original JSON fields, text headings, failure precedence, skip markers,
resume indices, efficiency hints and truncation markers are retained. Child
structured and text payloads have separate byte budgets, and aggregate text is
UTF-8 safe. Executed Bash scripts are attached only to private
`wire.Result.DashboardMetadata` for audit, never serialized as public metadata.

## Integration

Construct with configuration, workspace resolver, generic child caller, and the
catalog's child-validation callback. Call `SetFiles` with the engine's shared
file service and `SetJobLookup` with a read-only lookup returning whether a job
exists and whether its exact owned scope is quiescent.

The Go runner persists a terminal command outcome separately from exact scope
settlement. For an otherwise successful Bash result, batch waits at most thirty
seconds for the known job's quiescence proof. A canceled or expired wait produces
the existing unfinished-verification error. It never treats an exit marker as
proof that escaped descendants have stopped. Unknown jobs retain the legacy
behavior of checking the tool result itself.

Work checkpoint transitions were retired in the coordinator-approved design.
Supplying `checkpoint` receives the existing managed-operation precondition
error; no replacement state machine or success-shaped checkpoint is invented.
