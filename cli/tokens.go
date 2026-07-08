package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/lightninglabs/aperture/adminrpc"
	"github.com/spf13/cobra"
)

// NewTokensCmd creates the tokens parent command with list and revoke
// subcommands.
func NewTokensCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "Manage L402 tokens",
	}

	cmd.AddCommand(
		newTokensListCmd(),
		newTokensRevokeCmd(),
	)

	return cmd
}

func newTokensListCmd() *cobra.Command {
	var (
		limit  int32
		offset int32
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List issued tokens",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := getAdminClient()
			if err != nil {
				return err
			}
			defer cleanup()

			rpcCtx, cancel := rpcTimeout(cmd.Context())
			defer cancel()

			resp, err := client.ListTokens(
				rpcCtx,
				&adminrpc.ListTokensRequest{
					Limit:  limit,
					Offset: offset,
				},
			)
			if err != nil {
				return mapGRPCError(err)
			}

			if isJSONOutput() {
				return printProto(resp)
			}

			fmt.Printf(
				"Total: %d tokens\n\n", resp.TotalCount,
			)

			w := tabwriter.NewWriter(
				os.Stdout, 0, 0, 2, ' ', 0,
			)
			fmt.Fprintln(
				w,
				"ID\tTOKEN_ID\tSERVICE\t"+
					"PRICE_SATS\tSTATE\tCREATED_AT",
			)

			for _, t := range resp.Tokens {
				fmt.Fprintf(
					w, "%d\t%s\t%s\t%d\t%s\t%s\n",
					t.Id, t.TokenId,
					t.ServiceName, t.PriceSats,
					t.State, t.CreatedAt,
				)
			}

			return w.Flush()
		},
	}

	f := cmd.Flags()
	f.Int32Var(&limit, "limit", 0, "Max results to return")
	f.Int32Var(&offset, "offset", 0, "Pagination offset")

	return cmd
}

//nolint:dupl
func newTokensRevokeCmd() *cobra.Command {
	var tokenID string

	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke a token",
		RunE: func(cmd *cobra.Command, args []string) error {
			if tokenID == "" {
				return ErrInvalidArgsf(
					"--token-id is required",
				)
			}

			req := &adminrpc.RevokeTokenRequest{
				TokenId: tokenID,
			}

			if flags.dryRun {
				if err := printDryRun(
					"RevokeToken", req,
				); err != nil {
					return err
				}
				return ErrDryRunPassedNew()
			}

			client, cleanup, err := getAdminClient()
			if err != nil {
				return err
			}
			defer cleanup()

			rpcCtx, cancel := rpcTimeout(cmd.Context())
			defer cancel()

			resp, err := client.RevokeToken(
				rpcCtx, req,
			)
			if err != nil {
				return mapGRPCError(err)
			}

			if isJSONOutput() {
				return printProto(resp)
			}

			fmt.Printf(
				"Token %q revoked: %s\n",
				tokenID, resp.Status,
			)
			return nil
		},
	}

	cmd.Flags().StringVar(
		&tokenID, "token-id", "", "Token ID to revoke (required)",
	)

	return cmd
}
