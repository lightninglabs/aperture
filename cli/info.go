package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/lightninglabs/aperture/adminrpc"
	"github.com/spf13/cobra"
)

// NewInfoCmd creates the info subcommand that displays Aperture server
// information.
func NewInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Get Aperture server information",
		Long:  "Display server info including network, listen address, and TLS status.",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := getAdminClient()
			if err != nil {
				return err
			}
			defer cleanup()

			resp, err := client.GetInfo(
				context.Background(),
				&adminrpc.GetInfoRequest{},
			)
			if err != nil {
				return ErrConnectionWrap(err)
			}

			if isJSONOutput(cmd) {
				return printProto(resp)
			}

			w := tabwriter.NewWriter(
				os.Stdout, 0, 0, 2, ' ', 0,
			)
			fmt.Fprintf(w, "Network:\t%s\n", resp.Network)
			fmt.Fprintf(
				w, "Listen Address:\t%s\n", resp.ListenAddr,
			)
			fmt.Fprintf(w, "Insecure:\t%v\n", resp.Insecure)

			return w.Flush()
		},
	}
}
