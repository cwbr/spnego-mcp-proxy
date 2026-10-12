// Command spnego-mcp-proxy is a stdio MCP server that forwards JSON-RPC
// messages to a remote MCP server (Streamable HTTP) and authenticates with
// SPNEGO using the single sign-on credentials of the logged-in user: SSPI on
// Windows, the Kerberos ticket cache on macOS.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log.SetFlags(0)
	log.SetPrefix("spnego-mcp-proxy: ")

	target, err := upstreamURL(os.Getenv("MCP_UPSTREAM_URL"))
	if err != nil {
		log.Fatal(err)
	}
	negotiator, err := newNegotiator()
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("%s forwarding stdio to %s", version, target)
	newBridge(newUpstream(target, newHTTPClient(), negotiator), os.Stdout).run(ctx, os.Stdin)
}
