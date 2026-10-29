// Command forgec2-mcp exposes this console's read-only views to an MCP client over stdio.
//
// It is a separate binary on purpose: the server has no idea it exists, so nothing here can widen the API's authorization surface.
// The cookie flag is the operator's own session, which means the tool set can never see more than the person who started it.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/forgec2/forgec2/internal/mcp"
)

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:8000", "console base URL")
	cookie := flag.String("cookie", "", "session cookie header value, e.g. 'forgec2_session=...'")
	flag.Parse()

	if *cookie == "" {
		fmt.Fprintln(os.Stderr, "forgec2-mcp: -cookie is required; the tools act as that operator and never wider")
		os.Exit(2)
	}

	server := mcp.NewServer("forgec2", "1", os.Stdout)
	if err := mcp.RegisterReadOnlyTools(server, mcp.NewClient(*baseURL, *cookie)); err != nil {
		fmt.Fprintf(os.Stderr, "forgec2-mcp: %v\n", err)
		os.Exit(1)
	}
	if err := server.Serve(os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "forgec2-mcp: %v\n", err)
		os.Exit(1)
	}
}
