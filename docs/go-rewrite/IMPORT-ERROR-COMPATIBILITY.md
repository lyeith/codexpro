# Attachment error classes and native exception names

This final bundle is based on the last uploaded general-replay import file, not on either unexecuted supplemental staging version. The earlier V1/V2 copies remain intact. It corrects all nineteen remaining domain-error constructions and adds one narrowly typed native exception name. It also carries the 24-case owned-pair runner, including a complete malformed-URL contract check.

The source is exact `src/importOps.ts` at commit `44bb76bfa671e4aad540a8976f0fa6e6d5c2507a`, SHA-256 `293fdfd1ba6c9412ed1d0e33b22ed585102eeb02ecf6429f7f733d9ab957d0c1`. `import-error-classification.json` maps all twenty-three final Go domain constructions to their source lines. Nineteen change relative to the uploaded general-replay file: collision, hash, byte limits, URL policy, DNS/address failures, download timeout, HTTP status and redirect failures, reference variants, and empty destination. Those changes replace `fmt.Errorf(...)` with `wire.Errorf("", ...)`, preserving the original messages and arguments. No source code or recovery payload is invented.

## Native error names

`importOps.ts:344` constructs `new URL(reference.download_url)` before calling the domain validator. A malformed string therefore throws the native `TypeError` with message `Invalid URL`; `tools/shared.ts:14` preserves the native error name in the returned text. Go's earlier generic `Error:` prefix was not equivalent.

`wire.NativeErrorf` now represents an explicitly known native name and message, without domain metadata. `wire.ErrorResult` keeps its existing `CodexProError` branch first. Only when no domain error is present does it use an explicitly typed native name; wrapped errors retain their message context. Empty or absent names retain the existing `Error:` default. The import parser opts into `NativeErrorf("TypeError", "Invalid URL")` at its existing parse failure. No network, filesystem, context cancellation or unrelated native error is globally reclassified. The change does not alter URL admission, byte limits, redirects, finalization or success results.

## Regressions

The wire tests compare complete in-memory and serialized JSON envelopes for typed, wrapped, ordinary, wrapped-ordinary and unnamed native errors. They require no invented error code, metadata, null field or domain payload. A joined/wrapped error containing both classes proves that the established domain branch and detail flattening still take precedence.

The first import regression file performs real loopback downloads for collision, hash and size failures, checks exact error text/structure, verifies unchanged or absent destination bytes and temporary cleanup, and uses an owned disconnected socket to preserve native network errors. The second covers HTTP 404, a redirect lacking Location, a six-fetch redirect loop, reference variants, whitespace destination, credentials, blocked loopback, an explicitly allowlisted private address, scheme and helper-level missing-host failures. HTTP cases have five-second contexts; policy/reference cases fail before DNS or a socket. The malformed-URL regression requires exactly `TypeError: Invalid URL` in both text and structured content and verifies no destination was created. The sixty-second timeout is classified from source, not exercised by a slow shared-host test.

Both import test files reuse existing `replayErrorService` and `assertDomainError` helpers. Native Go tests are staged, not executed locally: this environment has no Go compiler. Lifecycle independently reviewed the complete domain mapping and native bridge and reported no concrete defect.

The paired supplement has eight direct/single/full workspace cases and sixteen import cases. The malformed-URL case preserves the entire source envelope, rejects invented domain metadata, and requires zero fixture requests and no file or temporary effects. All sixteen offline runner guard tests passed. The actual TypeScript/Go differential cases remain unexecuted at staging time.

## Apply and verify

The uploaded import base SHA-256 is `93c984d850722721ba6e6d3c591e19db6597615ae63428318b789b6486e2393d`; the manifest also pins the exact unchanged `wire/errors.go` base. V1/V2 production patches have not been applied. Verify each existing file's CAS hash before replacement; never overwrite a differing current file. New helper/test files must not overwrite an existing file. The supplied combined patch is against those original bases.

Format the changed Go files, then run the targeted gate under `nice -n 10`:

```sh
go test ./internal/wire -run 'TestNativeErrorResult' -count=1
go test ./internal/files -run 'TestDownloadedImport|TestImportHTTP|TestImportReference|TestImportURL|TestImportInvalid|TestReplayImport' -count=1
```

Run the normal changed-source build before the 24-case private differential. The supplemental invocation retains a 400-second external timeout with a 90-second kill grace, a 330-second internal budget, and the requirement that the complete 150-second activity-route allowance remain before that optional supplement starts. Require complete matching cases and clear cleanup receipts. Preparation, compilation and source review are not runtime parity passes.
