package cli

import (
	"fmt"

	"github.com/lightninglabs/aperture/adminrpc"
	"github.com/spf13/cobra"
)

// NewHealthCmd creates the health subcommand that checks server
// health.
func NewHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check Aperture server health",
		Long:  "Query the health endpoint to verify the server is responsive.",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := getAdminClient()
			if err != nil {
				return err
			}
			defer cleanup()

			resp, err := client.GetHealth(
				cmd.Context(),
				&adminrpc.GetHealthRequest{},
			)
			if err != nil {
				return mapGRPCError(err)
			}

			if isJSONOutput(cmd) {
				return printProto(resp)
			}

			fmt.Printf("Status: %s\n", resp.Status)
			return nil
		},
	}
}
