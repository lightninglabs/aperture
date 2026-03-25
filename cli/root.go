// Package cli provides the command-line interface for aperturecli.
package cli

import (
	"github.com/spf13/cobra"
)

const (
	defaultHost     = "localhost:8081"
	defaultMacaroon = "~/.aperture/admin.macaroon"
)

// flags holds the CLI flags.
var flags struct {
	// host is the Aperture admin gRPC host:port.
	host string

	// macaroon is the path to the admin macaroon file.
	macaroon string

	// tlsCert is the path to the TLS certificate for server
	// verification.
	tlsCert string

	// insecure skips TLS verification.
	insecure bool

	// jsonOutput forces JSON output.
	jsonOutput bool

	// humanOutput forces human-readable output.
	humanOutput bool

	// dryRun shows what would be done without executing.
	dryRun bool
}

// NewRootCmd creates the root aperturecli command with all subcommands
// registered.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "aperturecli",
		Short: "CLI for the Aperture admin API",
		Long: `aperturecli is a command-line interface and MCP server for
managing an Aperture L402 reverse proxy. It connects to the
admin gRPC API to manage services, view transactions, and
control the system.

Agent-friendly: defaults to JSON output when stdout is not a
TTY. Use --json or --human to override.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	pf := rootCmd.PersistentFlags()
	pf.StringVar(
		&flags.host, "host", defaultHost,
		"Aperture admin gRPC host:port",
	)
	pf.StringVar(
		&flags.macaroon, "macaroon", defaultMacaroon,
		"Path to admin macaroon file",
	)
	pf.StringVar(
		&flags.tlsCert, "tls-cert", "",
		"Path to TLS certificate for server verification",
	)
	pf.BoolVar(
		&flags.insecure, "insecure", false,
		"Skip TLS verification (plaintext gRPC)",
	)
	pf.BoolVar(
		&flags.jsonOutput, "json", false,
		"Force JSON output",
	)
	pf.BoolVar(
		&flags.humanOutput, "human", false,
		"Force human-readable output",
	)
	pf.BoolVar(
		&flags.dryRun, "dry-run", false,
		"Show what would be done without executing "+
			"(mutating commands only)",
	)

	rootCmd.AddCommand(
		NewInfoCmd(),
		NewHealthCmd(),
		NewServicesCmd(),
		NewTransactionsCmd(),
		NewTokensCmd(),
		NewStatsCmd(),
		NewSchemaCmd(),
		NewMCPCmd(),
	)

	return rootCmd
}
