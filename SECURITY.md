# Security policy

Report vulnerabilities privately through the repository's GitHub security reporting or contact the maintainer. Describe the affected commit and behavior. Omit credentials, private project data and environment files.

## Access to CodexPro

Configure a private Plans Access `principals.json` policy to issue and revoke named bearer credentials. Named personal agents receive read-only tools for their granted projects. Trusted operator credentials have access to shell execution, workspace writes, retained job output and administrative views. Give each workflow the narrowest access it needs. [Authentication configuration](go/internal/config/README.md#named-credentials)

MCP clients should send credentials in the Authorization header. Keep the shared token file and policy files private. Use the shared bearer bridge during a client migration, then disable it after the named credentials have been verified. For remote access, use an independently configured HTTPS endpoint and bind the direct listener to the intended network interface. [Deployment](deploy/README.md)

## Workspace and process boundaries

Register only the project roots clients need. File tools check paths, blocked globs and symlinks. Operator shell commands run with the server account's operating-system privileges. Host-resource scopes manage shell processes and resource limits. Configure the account and host isolation for the workflows that use full Bash. [Host job resources](docs/HOST_JOB_RESOURCES.md)

Keep state, policy, private curl configuration and tunnel credentials outside the served projects. The server retains bounded job output, Git change receipts and audit metadata. Activity views can contain command text and output. Share them only with trusted operators, and check the retention cursor before relying on a continuous audit history. [Activity](docs/ACTIVITY_JSON.md) · [Jobs](docs/JOBS.md)
