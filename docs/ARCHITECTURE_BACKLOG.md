# CodexPro Go Architecture Improvement Backlog

**Created:** 2026-11-28  
**Baseline revision:** `756ff3f6fe25` (`concierge/stateless-go-only-20261128`)  
**Context:** Post-stateless MCP cutover architecture review and trial implementation project.

---

## PR 1 — Low-Risk Hot-Path & Ownership Cleanup (Complete — `9fa93b0`)

- [x] **1.1 O(1) project & workspace index (`go/internal/workspace/catalog.go`, `create.go`)**  
  Pre-indexed `byProjectID`, `byWorkspaceID`, and cached `workspaceIDs` in `workspace.Manager` via `rebuildIndexLocked()` instead of cloning `[]wire.Project` and hashing `IDForRoot(p.Root)` linearly on every `Project()`, `Resolve()`, and `WorkspaceList()` call.
- [x] **1.2 Accurate `ListChanged` capability & zero-alloc security headers (`go/internal/transport/http.go`)**  
  Advertised `Tools.ListChanged: !s.stateless` (no SSE push channel in stateless mode) and replaced the per-request `map[string]string` literal in `ServeHTTP` with direct `hdr.Set(...)` calls.
- [x] **1.3 Pass shared `*files.Service` into `batch.New` (`go/internal/batch/batch.go`, `go/cmd/codexpro/main.go`, `go/internal/engine/batch_children_test.go`, `go/internal/batch/README.md`)**  
  Eliminated the throwaway `files.New(cfg, resolver)` allocation in `batch.New` and removed `SetFiles`.
- [x] **1.4 O(1) LRU eviction + raised entry cap in `SnapshotStore` (`go/internal/files/snapshots.go`)**  
  Replaced the O(N) `order []string` linear scan and slice splice with `container/list` (`order *list.List`, `nodes map[string]*list.Element`) and raised `maxSnapshotEntries` from 256 to 2048 (alongside the 64 MB `maxSnapshotBytes` cap) to prevent `edit_tag_unknown` churn across concurrent fleet agents.
- [x] **1.5 Unify `batch/bounds.go` with `wire/limits.go` (`go/internal/wire/limits.go`, `go/internal/batch/bounds.go`)**  
  Replaced the double `utf16.Encode` allocation in `wire/limits.go` with a zero-alloc UTF-16 unit counter, normalized invalid UTF-8 in `wire.TruncateUTF8`, and delegated `batch/bounds.go` to `wire.BatchPriority`, `wire.TruncateUTF8`, and `wire.CompactStructured`.

---

## PR 2 — Transport Hot-Path & MCP `2026-07-28` Efficiency (Complete)

- [x] **2.1 Lightweight JSON-RPC envelope peek in `s.mcp()` (`go/internal/transport/http.go`, `access.go`)**  
  Replaced full `map[string]any` unmarshaling of up to 20 MB POST bodies with `mcpEnvelopePeek` (`ID`, `Method json.RawMessage`, `Params json.RawMessage`), avoiding nested map allocations for `params.arguments` on every POST and only parsing `params.name` when access logging is active or `2024-10-07` rewriting is triggered.
- [x] **2.2 Single-pass `rawResult.MarshalJSON` (`go/internal/transport/http.go`, `raw_result_test.go`)**  
  Eliminated the marshal → unmarshal → re-marshal round-trip for `wire.Result` and `map[string]any` payloads when injecting `_meta` and `resultType: "complete"` on `2026-07-28` responses.
- [x] **2.3 Cancel read-only legacy calls on HTTP disconnect — Closed (verified intentional contract)**  
  `logical_mail_http_test.go:483-517` explicitly requires accepted legacy-protocol calls to outlive HTTP disconnects; `2026-07-28` calls already cancel cleanly with the HTTP request.
- [ ] **2.4 Enable `2026-07-28` discovery caching hints (`go/internal/transport/http.go`)**  
  Evaluate `ServerOptions.SetCacheable` (`ttlMs` / `cacheScope`) for static `server/discover` and `tools/list` metadata per credential scope (deferred pending `go-sdk` v1.8.x API check).
- [x] **2.5 Remove redundant `Authorization` header rewrite (`go/internal/transport/http.go:621`) — Closed (verified required)**  
  `legacy_admission_test.go:51` and `logical_mail_request.go:66-67` rely on `req.Extra.Header.Get("Authorization")` when clients authenticate via query-parameter auth (since `sdkAuthenticatedHandler` restores `r.Header` to the original headers).

---

## PR 3 — Audit Throughput & Transport Modularity

- [ ] **3.1 Audit group-commit / hot-path review (`go/internal/audit/store.go`, `go/internal/engine/engine.go`)**  
  Inspect `Journal.run()` + `writeBatch()` to verify whether group-commit batching is already active or can be tightened.
- [ ] **3.2 Split `go/internal/transport/http.go` (~870 lines)**  
  Separate HTTP auth/routing (`auth_http.go`) and MCP envelope/response helpers (`envelope.go`) from `http.go`.
- [ ] **3.3 Retire stateful session lane (post-soak)**  
  After stateless mode soaks across Gemini, ChatGPT, and Claude clients, remove `sessions`, `session_admission.go`, `prune()`, and the `2024-10-07` shim.
