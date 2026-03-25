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
// registered. The server uses the provided gRPC admin client for all
// operations.
func NewServer(client adminrpc.AdminClient) *gomcp.Server {
	server := gomcp.NewServer(
		&gomcp.Implementation{
			Name:    "aperturecli",
			Version: "0.1.0",
		},
		nil,
	)

	registerTools(server, client)

	return server
}

// Run starts the MCP server on the stdio transport and blocks until
// the context is cancelled or the transport closes.
func Run(ctx context.Context, client adminrpc.AdminClient) error {
	server := NewServer(client)

	return server.Run(ctx, &gomcp.StdioTransport{})
}
