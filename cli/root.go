// Package cli provides the command-line interface for prismcli.
package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

const (
	defaultHost     = "localhost:8081"
	defaultMacaroon = "~/.prism/admin.macaroon"
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

	// timeout is the RPC call timeout.
	timeout time.Duration
}

// NewRootCmd creates the root prismcli command with all subcommands
// registered.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "prismcli",
		Short: "CLI for the Loka Prism L402 admin API",
		Long: `prismcli is a command-line interface and MCP server for
managing a Loka Prism L402 reverse proxy. It connects to the
admin gRPC API to manage services, view transactions, and
control the system.

Agent-friendly: defaults to JSON output when stdout is not a
TTY. Use --json or --human to override.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command,
			args []string) error {

			if flags.jsonOutput && flags.humanOutput {
				return ErrInvalidArgsf(
					"--json and --human are " +
						"mutually exclusive",
				)
			}

			return nil
		},
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
	pf.DurationVar(
		&flags.timeout, "timeout", 30*time.Second,
		"RPC call timeout",
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
		newVersionCmd(),
	)

	return rootCmd
}

// Version is set at build time via -ldflags.
var Version = "dev"

// newVersionCmd creates the version subcommand.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print prismcli version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("prismcli version %s\n", Version)
		},
	}
}
