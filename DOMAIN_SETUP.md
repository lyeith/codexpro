# CodexPro domain setup

Run `codexpro-go` on the machine holding the projects. Configure its listener or systemd socket on the address reachable by your HTTPS reverse proxy or managed tunnel. For a proxy on the same machine, bind the origin to loopback. [Service setup](deploy/README.md)

Configure the hostname in your proxy or tunnel, with the CodexPro origin as the upstream. For example, expose `https://mcp.example.com/mcp` at the proxy and forward it to `http://127.0.0.1:7200/mcp`. Forward the Authorization and MCP headers, and preserve streaming HTTP responses. The proxy manages certificates and DNS.

Configure the MCP client with the HTTPS URL and an approved bearer credential. Test an authenticated `/healthz` request and an MCP initialize call through the proxy. Keep the origin credentials and tunnel credentials private. [Authentication](go/internal/config/README.md#named-credentials) · [ChatGPT connection options](https://developers.openai.com/plugins/deploy/connect-chatgpt)
