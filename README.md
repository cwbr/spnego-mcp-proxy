# SPNEGO MCP Proxy

This local HTTP proxy binds exclusively to `127.0.0.1` and forwards requests to
the configured MCP endpoint. It uses the current macOS user's Kerberos ticket
cache to authenticate MCP Streamable HTTP requests with SPNEGO. TLS validation
uses the macOS system trust store, including private certificate authorities.

The upstream is set at startup through `MCP_UPSTREAM_URL`. The proxy does not
store passwords or disable certificate validation. All components use Python,
and VS Code starts them automatically as a `stdio` MCP server.

## Prerequisites

- A valid Kerberos ticket: `klist -s`
- Python 3.11 or newer
- Xcode Command Line Tools and Kerberos development files if
  `requests-gssapi` must be compiled locally

## Installation

```sh
cd /Users/cwbr/Tools/git/spnego-mcp-proxy
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
```

If the required private CA is not installed in the macOS Keychain, ask your IT
team to install it. Certificate validation remains enabled.

## VS Code Configuration and Startup

VS Code starts the adapter automatically when the MCP server starts. The local
HTTP port is only used inside the adapter process. Set the upstream for each
server through `env` in the user `mcp.json` file:

```json
"spnegoMcpProxy": {
  "type": "stdio",
  "command": "/Users/cwbr/Tools/git/spnego-mcp-proxy/.venv/bin/python",
  "args": [
    "/Users/cwbr/Tools/git/spnego-mcp-proxy/stdio_adapter.py"
  ],
  "env": {
    "MCP_UPSTREAM_URL": "https://mcp.example.internal/mcp"
  }
}
```

The URL is not a secret, so a `.env` file is not needed. Additional MCP servers
can each use their own name and `MCP_UPSTREAM_URL` value. The dynamically chosen
local port allows them to run concurrently.

Then run `MCP: List Servers` and start or restart the server. Use
`MCP: List Servers` > `Show Output` to inspect connection issues.

The process starts the SPNEGO proxy automatically. It is stopped when the MCP
server is stopped or restarted.

## Manual HTTP Check

For a separate HTTP test, you can still start the proxy manually:

```sh
MCP_UPSTREAM_URL=https://mcp.example.internal/mcp \
  .venv/bin/waitress-serve --host=127.0.0.1 --port=8765 spnego_mcp_proxy:app
```

In a second terminal:

```sh
curl --fail-with-body --silent --show-error \
  --request POST http://127.0.0.1:8765/mcp \
  --header 'Content-Type: application/json' \
  --header 'Accept: application/json, text/event-stream' \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"manual-test","version":"1.0"}}}'
```

Verify that the MCP response is successful, then stop the proxy with `Ctrl+C`.