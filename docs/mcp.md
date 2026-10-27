# MCP server

`bastionhub mcp` serves read-only information from the endpoint registry and tunnel status. It uses
`github.com/modelcontextprotocol/go-sdk` v1.8.0.

## Tools

| Tool | Result | Source |
|---|---|---|
| `list_endpoints` | Endpoint name, port, user and description | Local `endpoints.yaml` |
| `get_tunnel_status` | Endpoint name, port, status, uptime and description | SSH query to `admin_alias`, or the local host when `BASTIONHUB_LOCAL=1` |

Both tools are read-only and return structured content with a text copy. The server exposes no tools
for access issuance, invites, certificates or configuration changes.

## Transports

| Transport | Command | Address |
|---|---|---|
| stdio | `bastionhub mcp` or `bastionhub mcp --transport stdio` | Standard input and output |
| Streamable HTTP | `bastionhub mcp --transport http` | `http://127.0.0.1:8421/mcp` |

HTTP binds only to `127.0.0.1`. The SDK's localhost protection rejects requests with a non-localhost
Host header, and Go's cross-origin protection answers 403 to disallowed `Origin` headers.

## HTTP authentication

At start, the HTTP transport writes a random token (64 hex characters) to the token file, mode
`0600`, and deletes the file when it stops on SIGINT or SIGTERM. An existing file at that path is
replaced. Every request carries `Authorization: Bearer <token>`; a request without it, or with another
token, answers 401. The token changes on every start, so a client reads the file after the server
starts.

| Variable | Default | Sets |
|---|---|---|
| `BASTIONHUB_MCP_TOKEN_FILE` | `~/.config/bastionhub/mcp-token` | the token file |

A server killed with SIGKILL leaves the file; the next start replaces it. On Windows the file's
mode is not enforced. stdio has no token: the client is the user who launched the process.

## Configuration and requirements

The server reads `~/.config/bastionhub/endpoints.yaml`, or `$BASTIONHUB_CONFIG` when set. Tunnel
status requires `ssh` on `PATH` and access to `admin_alias`, unless `BASTIONHUB_LOCAL=1`; local status
requires the `ss` command. The transport flag accepts `stdio` or `http`; its default is `stdio`, and
`BASTIONHUB_MCP_TRANSPORT` sets the same value.
