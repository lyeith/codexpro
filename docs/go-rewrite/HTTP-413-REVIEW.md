# HTTP 413 review request and wiring recipe

Prepared from the paired private telemetry run (attempt 2: 104 cases, 96 passed), captured release `44bb76b` HTTP source, and the current staged Go handler sources. This is a proposed compatibility delta; no approval is inferred and no mail has been sent.

## Coordinator request

Please approve one explicit response-body delta for oversized JSON requests on the usage and inbox HTTP endpoints. The private TypeScript reference returns status 413 and an Express HTML error page whose `<pre>` starts with `PayloadTooLargeError: request entity too large`, followed by ten Node/body-parser/router stack frames containing release paths and line numbers. The Go candidate currently returns a JSON domain-error envelope; we will correct that to the same HTML document, error class and message, content type (`text/html; charset=utf-8`), CSP (`default-src 'none'`), and `X-Content-Type-Options: nosniff`. Existing request limits and authentication/origin-check ordering remain unchanged.

The requested delta is solely omission of the Node development stack after the first error line. Go will not fabricate a Node stack or expose a replacement Go stack. The response length will describe the resulting HTML body. MCP JSON-RPC errors, profile JSON errors, and usage/inbox validation errors retain their own contracts. The failed raw reference responses remain in the evidence, and the rerun will validate all stable fields with a narrowly documented allowance for this approved stack omission.

## Proposed response

Status: `413 Request Entity Too Large`.

```html
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Error</title>
</head>
<body>
<pre>PayloadTooLargeError: request entity too large</pre>
</body>
</html>
```

The body has a final newline. The helper explicitly sets the three headers named above and the body byte length. It does not replace outer authentication or cache headers. The compact failure report truncates the cache-control value, so this note does not claim an independently verified full cache-control string; retain and compare the existing outer response.

## Narrow wiring recipe

1. After the ruling, add `httpcompat_payload_too_large.go` and its test to `go/internal/httpcompat/payload_too_large.go` and `payload_too_large_test.go`. The helper is `httpcompat.PayloadTooLarge(w, r)`.
2. In `go/internal/usage/http.go`, replace only the `responseError(w, &Error{413, "request entity too large"})` call inside the `errors.As(err, &tooBig)` branch following `io.ReadAll(http.MaxBytesReader(w, r.Body, 128*1024))` with `httpcompat.PayloadTooLarge(w, r)`. Keep its immediate return, the 128 KiB bound, same-origin ordering, and other read/validation failures.
3. Make the same single-call change in `go/internal/inbox/http.go` inside the existing `32*1024` MaxBytesReader failure branch. Keep the 32 KiB bound and all domain-error paths.
4. Both remote handlers already import `internal/httpcompat` for the previously verified generic 404 response. The final complete 413 variants were regenerated from the freshly captured `usage-http-current.go` and `inbox-http-current.go`, whose hashes were verified against the parent-provided exact current hashes. Do not use the older local `usage-http.go`, `inbox-http.go`, or reconstructed `*-with404.go` variants.
5. Do not route every oversized request through this HTML helper in main/transport. The captured final error middleware explicitly gives `/mcp` a JSON-RPC 413 with `error.code: -32000`, `error.message: "Payload too large."`, `id: null`; `/admin/profile` gets its existing JSON error contract. A declared profile content length over 32,768 is rejected earlier with `payload_too_large` / `Profile request body is too large.`; parser-detected overflow is `payload_too_large` / `Request body is too large.`.
6. Parent has verified that main has no shared body limiter and transport's 20 MiB guard applies only to MCP processing with the exact JSON-RPC 413 contract. No main or transport patch is needed. Usage/inbox's existing smaller limits reach their own handlers.

## Validation and evidence

The staged helper test checks the exact body, final newline, byte length, status, content type, CSP and nosniff, including body suppression for a direct HEAD invocation and no query reflection. Parent must run the native Go tests before committing.

The paired rerun should exercise both known oversized requests plus below/at/above-limit valid JSON and origin rejection before parsing. Preserve the prior failed evidence. Once the coordinator permits stack omission, validate the TypeScript HTML shell, exact first error line, and stack-frame-only suffix; validate the candidate's exact stack-free shell. Restrict any comparison normalization to this named approved delta and these response cases, and record it in the report. Do not drop arbitrary error bodies, content types, headers, or audit fields.

Source evidence: `sp6-reference/http-tail-source.ts` contains the captured `/mcp` parser and final middleware; `http-source.ts` contains `adminBodyLimit`, same-origin checks and the profile parser. `telemetry2-failed-compact.json` preserves the two real response diffs. No changes to generic parser media-type/compression behavior are part of this proposal; those need separate source or paired evidence.

## Inactive comparison policy

`telemetry_approved_deltas.py` provides `payload_too_large_projection(side, response, *, approval)`. It is not imported by the telemetry runner or cases. After a ruling, the parent may supply the actual recorded ruling metadata and pass this callback only to the two named body-budget cases. The approval record requires the exact delta ID, source commit, authority, registry path/row, observed ruling time, and ruling-text SHA-256; synthetic unit-test metadata does not constitute approval.

The reference check requires the exact captured ten-frame Express stack, including function names, module paths, line/column numbers, order and shared release-directory prefix. The candidate must match the exact stack-free HTML. Status and the required headers must match; all remaining compared headers remain in the comparison. Both raw bodies remain unchanged, and the normalization ledger records their hashes, the case and the approval. The six local guard tests pass, including changed headers, messages, frames, HTML, wrong cases, missing approval and evidence immutability. Existing telemetry runner/cases files remain unchanged.

## Staged file hashes

- `httpcompat_payload_too_large.go`: `a3ab8555d0bad41c926e18ff678b4007aa16c8021b6203c6ff8e2ed90b2c90a7`
- `httpcompat_payload_too_large_test.go`: `7b899fe9ce62a8dde6f5af8996ad24d558c85723ff572c7d45c652d4e7ee6fac`
- `usage-http-current.go` (exact pre-413): `ad6a1a78c44a971faf2b12de99c60c41d4f8134fef8ff12573c63dffd8bb006c`
- `usage-http-413.go` (desired): `1bc7a397a8c683866887a7144050c37aa5403787737c878ec3772823ab46d4d3`
- `inbox-http-current.go` (exact pre-413): `d3d4ce69a89be58d25cad6c0f792f89ee0a9b1ca00eda412994e08faba1ba0c3`
- `inbox-http-413.go` (desired): `65f96400059f235dcdc9c796c872a6f6f2b6bf739f188e496fcaa83223fd0318`
- `telemetry_approved_deltas.py`: `54da223044a1f001201f0a812b4355abe0db6fe7c7a60170bdd41bd9c8849e76`
- `test_telemetry_approved_deltas.py`: `c6ee4553d8cfdcef31f56a48c37ddcb3c5979fde79bf2774d6202626dd35ecf1`

`http413-handler-hashes.json` contains the exact freshly captured pre-413 handler hashes. Each complete staged handler differs from that baseline by one call only. The parent must compare-and-swap against the current file hash; a mismatch requires inspection, not overwriting with a stale cached handler. The complete variants, patches, hash map and review note are now frozen for the parent upload.

The audit fix files remain frozen for the parent's upload consolidation and are independent of this review request.
