# One entrypoint for the default contract lanes

[contract_bundle.py](contract_bundle.py) runs the four existing default CLIs sequentially: files/search/Git workflows, process/jobs/batch, telemetry, then admin/catalog. It does not change their cases, comparisons or approval rules and does not start or stop either server. Managed workspace, lifecycle, profile restart, history/migration, startup and performance proofs remain separate; this entrypoint makes no stage-4 completion claim.

Supply two explicit launch manifests for the private TypeScript 7211 and Go 7210 processes, the exact HTTP-413 approval artifact, a new aggregate output directory and a separate new private control directory. The manifests must name the same fixture project ID and disjoint private roots/state. The wrapper reuses the paired suite's PID/start-time/listening-socket/path guard before creating either directory and before each lane. It derives endpoints, token-file paths and fixture roots from those bindings; there is no arbitrary-port or inferred-server fallback.

For the final private pair, from the repository root:

```bash
python3 docs/go-rewrite/contract_bundle.py \
  --reference-manifest /home/spite/.codexpro-go-proof/contract-final-ts/contract-manifest.json \
  --candidate-manifest /home/spite/.codexpro-go-proof/contract-final-go/launch.json \
  --approval-file docs/go-rewrite/evidence/http413-approval.json \
  --output-dir docs/go-rewrite/evidence/contract-bundle-1 \
  --private-control-dir /home/spite/.codexpro-go-proof/contract-bundle-control-1
```

Use new directory names for a later run and manifests belonging to that run's actual processes. The wrapper accepts no per-case selection flags. Each subprocess runs with nice value at least 10. Total per-lane budgets are 180 seconds for process/jobs/batch and 120 seconds for each other lane, reserving 30 seconds for interruption cleanup and one second for final reap. SIGINT permits the existing CLIs' `finally` cleanup to run. A further timeout signals only the wrapper-owned harness process group; it never signals a server PID. Timeout, missing/invalid reports or unverified cleanup prevent subsequent mutation lanes from starting.

The aggregate is `<output-dir>/summary.json`. It records each CLI's exit status, duration, script hash, unchanged full report path/hash/size and native count units. General workflow/step counts, process scenario counts, telemetry's unchanged/approved-delta categories and admin's pass/approved/fail/blocked/pending categories remain separate. `all_cli_gates_passed` follows the four CLIs' executed gates; admin's originally pending managed/profile cases remain visible in its count object and are not silently converted to passes.

Full case reports, raw transcripts and stdout/stderr remain under the private control directory. Files are created with mode 0600, directories with mode 0700. The aggregate includes no raw response body, credential, token path or log contents. It links the preserved reports; any later publication of fixture-only reports requires the evidence review described in [CONTRACT-COVERAGE.md](CONTRACT-COVERAGE.md). Normal contract mismatches with verified cleanup do not suppress later lanes. Any failed lane makes the final exit status nonzero.

Eight offline guard tests exercise refusal before writes, exact private bindings, overlap rejection, full-default CLI selection, preservation of approved/pending categories, credential/cleanup checks, owned-harness interruption cleanup and stopping subsequent mutation lanes after a timeout:

```bash
python3 -m unittest discover -s docs/go-rewrite -p test_contract_bundle.py -v
```

All eight guards passed locally in **0.429 seconds**, with no server traffic. They remain distinct from the actual runtime result below.

## Executed final private pair

The complete bundle passed in job `2b4ef08a`. Job wall time was **101.686 seconds**; the aggregate's own timestamp interval was **22:22:24.637–22:24:05.746 UTC on 31 October 2026**, or **101.109 seconds**. These are different measurement scopes. Every CLI exited zero, reported its gate passed and verified cleanup. No lane timed out, was interrupted, needed forced cleanup or was skipped.

| Lane | Duration | Preserved result units |
| --- | ---: | --- |
| General workflows | 7.784913 s | 20/20 workflows, 94/94 steps; zero failed/blocked; two approved retired-descriptor differences |
| Process/jobs/batch | 59.722850 s | 10/10 scenarios; 54 compared call positions in passing scenarios |
| Telemetry | 24.627739 s | 102 unchanged, two approved HTTP-413 differences, zero failed |
| Admin/catalog | 8.887954 s | 41 pass, eight approved retirement instances, zero failed/blocked, two original pending cases |

The two admin pending cases remain pending in this report; their separate managed/profile runtime receipts are linked in [RESULTS.md](RESULTS.md). Counts are not summed across unlike units. Compared call positions are pairs of corresponding calls, not a count of every HTTP request issued by setup, polling and cleanup.

The executed reference was TypeScript `44bb76bfa671e4aad540a8976f0fa6e6d5c2507a`. The owned candidate was `/home/spite/.codexpro-go/bin/codexpro-go`, SHA-256 **`c4402d15660cbad962b03a63a4ec8f1c4561f7a51264c2d2d1cdcaa4b7ebfda2`**, built from source commit **`80aee7b9da65a2f31682a86406dd5e4b5a4d4ece`** with Go tree **`2b4205a029bf7e7f02595ab57551a3dae8123711`**. The summary records reference/candidate launch-manifest hashes `084e1df81f59cd410dd38eafdaf9a9689cad0e2af4898a33e46c838e930f1e69` and `e53b73627d8d64092016d1063a3f6dd01261c486dc4c363c3eaa7f1838af562d`. The four recorded CLI hashes match the reviewed unchanged runners.

The [aggregate receipt](evidence/contract-bundle-1/summary.json) has SHA-256 **`f85afe41a5758fbfb3ccd015fa7db00b8c9cb7eaf660e9005b83add933a425ab`**. Complete lane reports and raw responses remain in the explicitly private control directory; this document publishes their result counts and identity bindings without copying raw payloads. Final performance, soak and handback state remain separate gates in [RESULTS.md](RESULTS.md).
