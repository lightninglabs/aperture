package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/lightninglabs/aperture/adminrpc"
	"github.com/spf13/cobra"
)

// NewStatsCmd creates the stats subcommand that displays revenue
// statistics.
func NewStatsCmd() *cobra.Command {
	var (
		from string
		to   string
	)

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Get revenue statistics",
		Long:  "Display total revenue, transaction count, and per-service breakdown.",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := getAdminClient()
			if err != nil {
				return err
			}
			defer cleanup()

			resp, err := client.GetStats(
				cmd.Context(),
				&adminrpc.GetStatsRequest{
					From: from,
					To:   to,
				},
			)
			if err != nil {
				return mapGRPCError(err)
			}

			if isJSONOutput(cmd) {
				return printProto(resp)
			}

			fmt.Printf(
				"Total Revenue: %d sats\n",
				resp.TotalRevenueSats,
			)
			fmt.Printf(
				"Transactions:  %d\n\n",
				resp.TransactionCount,
			)

			if len(resp.ServiceBreakdown) > 0 {
				w := tabwriter.NewWriter(
					os.Stdout, 0, 0, 2, ' ', 0,
				)
				fmt.Fprintln(
					w, "SERVICE\tREVENUE (sats)",
				)

				for _, s := range resp.ServiceBreakdown {
					fmt.Fprintf(
						w, "%s\t%d\n",
						s.ServiceName,
						s.TotalRevenueSats,
					)
				}

				return w.Flush()
			}

			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&from, "from", "", "Start date (RFC3339)")
	f.StringVar(&to, "to", "", "End date (RFC3339)")

	return cmd
}
