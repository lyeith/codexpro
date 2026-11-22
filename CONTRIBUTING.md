# Contributing to CodexPro

Build the connector from the `go/` module. Use Linux, Go 1.27.3, a C compiler for SQLite, Git and ripgrep. [Source installation](README.md#install-from-source)

```sh
cd go
go build ./cmd/codexpro
go test ./...
go test -race ./...
go vet ./...
```

Use a disposable project catalog, listener and state directory for HTTP checks. Use fixture bearer credentials and a fixture named policy. Service activation and host-resource tests require a separate user systemd environment. [Deployment](deploy/README.md)

Explain the behavior, preserved MCP contracts, and verification in pull requests. Keep credentials and private project data out of source. Update both READMEs and the affected reference documentation when setup or supported tools change.
