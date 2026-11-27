# Deprecated: this TypeScript checkout is not production

CodexPro runs the Go service. Its source is `~/Projects/codexpro-go` on SSD
(the `go/` module in the same repository, branch
`concierge/stateless-go-only-20261128` and later releases). The systemd unit
`codexpro.service` runs `~/apps/codexpro-go/current/codexpro-go`.

This branch (`david/catchup-0.30.0`) holds the retired TypeScript service
(0.30/0.31). Production moved to Go on 1 November 2026 and the Go line removed
the TypeScript sources on 22 November 2026 (commit 1f91753). Do not build,
deploy or fix CodexPro from this checkout; make changes in `codexpro-go`.

The `codexpro` project in `~/.config/codexpro/projects.json` points at
`~/Projects/codexpro-go`.

The checkout is kept, with its uncommitted work-runtime edits and the
stateless-transport spec, for reference only.
