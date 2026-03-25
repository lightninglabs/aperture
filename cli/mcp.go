package cli

import (
	"context"
	"os/signal"
	"syscall"

	aperturemcp "github.com/lightninglabs/aperture/mcpserver"
	"github.com/spf13/cobra"
)

// NewMCPCmd creates the mcp subcommand for Model Context Protocol
// server operations.
func NewMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Model Context Protocol server",
		Long:  "Expose aperturecli operations as MCP tools over stdio JSON-RPC.",
	}

	cmd.AddCommand(newMCPServeCmd())

	return cmd
}

// newMCPServeCmd creates the mcp serve subcommand that starts the
// stdio JSON-RPC MCP server.
func newMCPServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP server on stdio",
		Long: `Start an MCP (Model Context Protocol) server that exposes
Aperture admin operations as typed tools over stdio JSON-RPC.
This enables direct integration with agent frameworks like
Claude Code.

Available tools: get_info, get_health, list_services,
create_service, update_service, delete_service,
list_transactions, list_tokens, revoke_token, get_stats`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := getAdminClient()
			if err != nil {
				return err
			}
			defer cleanup()

			ctx, cancel := signal.NotifyContext(
				context.Background(),
				syscall.SIGINT, syscall.SIGTERM,
			)
			defer cancel()

			return aperturemcp.Run(ctx, client)
		},
	}
}
