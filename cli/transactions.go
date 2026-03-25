package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/lightninglabs/aperture/adminrpc"
	"github.com/spf13/cobra"
)

// NewTransactionsCmd creates the transactions parent command.
func NewTransactionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "transactions",
		Short: "Query L402 transactions",
	}

	cmd.AddCommand(newTransactionsListCmd())

	return cmd
}

func newTransactionsListCmd() *cobra.Command {
	var (
		service   string
		state     string
		startDate string
		endDate   string
		limit     int32
		offset    int32
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List transactions with optional filters",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := getAdminClient()
			if err != nil {
				return err
			}
			defer cleanup()

			rpcCtx, cancel := rpcTimeout(cmd.Context())
			defer cancel()

			resp, err := client.ListTransactions(
				rpcCtx,
				&adminrpc.ListTransactionsRequest{
					Service:   service,
					State:     state,
					StartDate: startDate,
					EndDate:   endDate,
					Limit:     limit,
					Offset:    offset,
				},
			)
			if err != nil {
				return mapGRPCError(err)
			}

			if isJSONOutput() {
				return printProto(resp)
			}

			fmt.Printf(
				"Total: %d transactions\n\n",
				resp.TotalCount,
			)

			w := tabwriter.NewWriter(
				os.Stdout, 0, 0, 2, ' ', 0,
			)
			fmt.Fprintln(
				w,
				"ID\tTOKEN_ID\tSERVICE\t"+
					"PRICE_SATS\tSTATE\tCREATED_AT",
			)

			for _, tx := range resp.Transactions {
				fmt.Fprintf(
					w, "%d\t%s\t%s\t%d\t%s\t%s\n",
					tx.Id, tx.TokenId,
					tx.ServiceName, tx.PriceSats,
					tx.State, tx.CreatedAt,
				)
			}

			return w.Flush()
		},
	}

	f := cmd.Flags()
	f.StringVar(
		&service, "service", "",
		"Filter by service name",
	)
	f.StringVar(
		&state, "state", "",
		"Filter by state (pending, settled)",
	)
	f.StringVar(
		&startDate, "from", "", "Start date (RFC3339)",
	)
	f.StringVar(&endDate, "to", "", "End date (RFC3339)")
	f.Int32Var(&limit, "limit", 0, "Max results to return")
	f.Int32Var(&offset, "offset", 0, "Pagination offset")

	return cmd
}
