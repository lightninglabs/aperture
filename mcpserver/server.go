// Package mcpserver provides the Model Context Protocol server for
// aperturecli. It exposes the admin API operations as typed MCP tools
// over stdio JSON-RPC, enabling direct integration with agent
// frameworks.
package mcpserver

import (
	"context"

	"github.com/lightninglabs/aperture/adminrpc"
	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer creates a new MCP server with all aperturecli tools
// registered. The version parameter is injected from the CLI's build
// version to avoid import cycles.
func NewServer(client adminrpc.AdminClient,
	version string) *gomcp.Server {

	server := gomcp.NewServer(
		&gomcp.Implementation{
			Name:    "aperturecli",
			Version: version,
		},
		nil,
	)

	registerTools(server, client)

	return server
}

// Run starts the MCP server on the stdio transport and blocks until
// the context is cancelled or the transport closes.
func Run(ctx context.Context, client adminrpc.AdminClient,
	version string) error {

	server := NewServer(client, version)

	return server.Run(ctx, &gomcp.StdioTransport{})
}
