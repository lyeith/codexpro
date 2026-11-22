# CodexPro FAQ

## Where do I install CodexPro?

Build `codexpro-go` from this checkout's Go module and run it on the machine that holds the project roots. Configure one catalog for the projects agents can access. [Install from source](README.md#install-from-source)

## How do clients authenticate?

Use an HTTP MCP bearer credential. Configure the private Plans Access policy for named credentials and use the shared-token bridge during migration. Named personal agents receive read access for explicitly granted projects. [Named authentication](go/internal/config/README.md#named-credentials)

## Can I expose CodexPro at a stable hostname?

Point an HTTPS reverse proxy or managed tunnel at the CodexPro listener and configure the client's `/mcp` URL. Set up the proxy independently and forward the Authorization and MCP session headers. [Domain setup](DOMAIN_SETUP.md)

## Do I need to start one server per project?

A single catalog can serve multiple projects. Call `list_projects` for workspace handles and `open_workspace` for project instructions. Use the appropriate handle for each file and job call. [Project catalog](README.md#configure-projects)

## How do I check work across server restarts?

Jobs and the audit journal live in the state directory. Reinitialize MCP after the restart, then use `jobs` for an accepted shell job. The optional read-only work archive is available through `work_status`. [Jobs](docs/JOBS.md) · [Work status](docs/WORK_RUNS.md)
