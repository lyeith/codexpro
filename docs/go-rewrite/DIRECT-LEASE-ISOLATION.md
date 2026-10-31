# Direct-project isolation from historical lease inspection

`TestHistoricalLeaseProbeDoesNotBlockUnrelatedDirectProject` in `go/internal/workspace/direct_isolation_test.go` exercises the specific isolation condition in DESIGN §7. It is a native integration test over real private Git repositories, the production workspace/file services and the durable lease store. It is not a paired TypeScript/Go test or an HTTP latency benchmark.

The fixture creates a real managed worktree with `Manager.CreateWorkspace`, closes the manager, and reopens the same private catalog/state in supported direct mode. The catalog also contains an unrelated Git repository. The restored managed lease remains historical state; calls to the unrelated project resolve explicitly to `Mode: "direct"`. No live configuration, process, repository or state is involved.

The test holds the production `Manager.Reconcile` probe for the historical managed checkout. While that probe remains blocked, it executes eight rounds of concurrent `open_workspace(project_id="healthy")` and `read` calls through `workspace.Service.Call` and `files.Service.Call`. Each pair must complete within a one-second context, with the correct unrelated direct root, workspace identity and content hash. It alternates two direct sessions. After every round it checks that the historical lease's complete persisted JSON, revision column, in-memory copy and ownership remain unchanged.

The held probe then returns an injected `context.DeadlineExceeded` while the overall reconciliation context is still valid. The resulting observation must keep the lease ready and owned, change only health-observation metadata, and increment its revision exactly once. Direct open/read calls must continue to work in that unavailable-health state without altering the historical lease. A subsequent `Reconcile` call with the real Git inspector must heal the same lease, without manual JSON changes or ownership replacement. The healthy observation increments the revision once more. A second manager restart verifies the healed persisted state and repeats direct open/read access.

The verbose receipt records all open/read durations, the block interval, observation revisions, zero observed orphan transitions and successful persistence after reopen. This one-second native bound detects coupling to the deliberately held probe; it does not replace the separate ordinary/build percentile targets. The injected per-probe timeout is explicit. Actual helper/scope behavior, HTTP serving, audit wrappers and host-load conditions have their separate runtime receipts in [RESULTS.md](RESULTS.md).

From the repository's `go/` directory, the bounded gate is:

```bash
nice -n 10 go test -race ./internal/workspace -run '^TestHistoricalLeaseProbeDoesNotBlockUnrelatedDirectProject$' -count=1 -timeout=45s -v
```

The test's overall context is 20 seconds and its owned goroutine cleanup wait is bounded to two seconds. The final result and tested source identity belong in [RESULTS.md](RESULTS.md); this document does not promote a prepared test to an executed pass.

## Executed native gate

The 31 October 22:11 UTC race gate passed in job `05fdf639`; see `evidence/direct-lease-isolation-native-1.json`. It exercised sixteen concurrent calls over eight rounds while the probe was held for 98.109 ms. The maximum read was 8.377405 ms and maximum open 13.036556 ms, below the one-second per-call assertion. Persisted lease bytes/revision and owner remained unchanged during the block; orphan observations were zero. The unavailable and healed observations advanced revision 2 to 3 to 4, and real Git healing survived a reopen. This is native production-component concurrency evidence with an injected probe timeout, not HTTP or incident-load reproduction.
