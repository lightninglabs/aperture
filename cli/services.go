package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/lightninglabs/aperture/adminrpc"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

// NewServicesCmd creates the services parent command with list, create,
// update, and delete subcommands.
func NewServicesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "services",
		Short: "Manage backend services",
		Long:  "Create, list, update, and delete backend services proxied by Aperture.",
	}

	cmd.AddCommand(
		newServicesListCmd(),
		newServicesCreateCmd(),
		newServicesUpdateCmd(),
		newServicesDeleteCmd(),
	)

	return cmd
}

func newServicesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all configured services",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := getAdminClient()
			if err != nil {
				return err
			}
			defer cleanup()

			resp, err := client.ListServices(
				context.Background(),
				&adminrpc.ListServicesRequest{},
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
			fmt.Fprintln(
				w, "NAME\tADDRESS\tPROTOCOL\tPRICE\tAUTH",
			)

			for _, s := range resp.Services {
				fmt.Fprintf(
					w, "%s\t%s\t%s\t%d\t%s\n",
					s.Name, s.Address,
					s.Protocol, s.Price, s.Auth,
				)
			}

			return w.Flush()
		},
	}
}

func newServicesCreateCmd() *cobra.Command {
	var (
		name       string
		address    string
		protocol   string
		hostRegexp string
		pathRegexp string
		price      int64
		auth       string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new backend service",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return ErrInvalidArgsf("--name is required")
			}
			if address == "" {
				return ErrInvalidArgsf(
					"--address is required",
				)
			}

			req := &adminrpc.CreateServiceRequest{
				Name:       name,
				Address:    address,
				Protocol:   protocol,
				HostRegexp: hostRegexp,
				PathRegexp: pathRegexp,
				Price:      price,
				Auth:       auth,
			}

			if flags.dryRun {
				if err := printDryRun(
					"CreateService", req,
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

			resp, err := client.CreateService(
				context.Background(), req,
			)
			if err != nil {
				return ErrConnectionWrap(err)
			}

			if isJSONOutput(cmd) {
				return printProto(resp)
			}

			fmt.Printf(
				"Service %q created successfully.\n",
				resp.Name,
			)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&name, "name", "", "Service name (required)")
	f.StringVar(
		&address, "address", "", "Backend address (required)",
	)
	f.StringVar(
		&protocol, "protocol", "http",
		"Protocol: http or https",
	)
	f.StringVar(
		&hostRegexp, "host-regexp", "",
		"Host header regexp pattern",
	)
	f.StringVar(
		&pathRegexp, "path-regexp", "",
		"URL path regexp pattern",
	)
	f.Int64Var(&price, "price", 0, "Price in satoshis")
	f.StringVar(
		&auth, "auth", "on",
		"Auth level: on, off, or freebie N",
	)

	return cmd
}

func newServicesUpdateCmd() *cobra.Command {
	var (
		name       string
		address    string
		protocol   string
		hostRegexp string
		pathRegexp string
		price      int64
		auth       string
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update an existing service",
		Long: `Update one or more fields of an existing service. Only flags
that are explicitly provided will be changed.

Examples:
  # Change price only:
  aperturecli services update --name myapi --price 500

  # Change address and protocol:
  aperturecli services update --name myapi --address 10.0.0.5:8080 --protocol https`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return ErrInvalidArgsf("--name is required")
			}

			req := &adminrpc.UpdateServiceRequest{
				Name: name,
			}

			// Only populate fields that were explicitly
			// set by the user.
			if cmd.Flags().Changed("address") {
				req.Address = address
			}
			if cmd.Flags().Changed("protocol") {
				req.Protocol = protocol
			}
			if cmd.Flags().Changed("host-regexp") {
				req.HostRegexp = hostRegexp
			}
			if cmd.Flags().Changed("path-regexp") {
				req.PathRegexp = pathRegexp
			}
			if cmd.Flags().Changed("price") {
				req.Price = proto.Int64(price)
			}
			if cmd.Flags().Changed("auth") {
				req.Auth = auth
			}

			if flags.dryRun {
				if err := printDryRun(
					"UpdateService", req,
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

			resp, err := client.UpdateService(
				context.Background(), req,
			)
			if err != nil {
				return ErrConnectionWrap(err)
			}

			if isJSONOutput(cmd) {
				return printProto(resp)
			}

			fmt.Printf(
				"Service %q updated successfully.\n",
				resp.Name,
			)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&name, "name", "", "Service name (required)")
	f.StringVar(&address, "address", "", "Backend address")
	f.StringVar(
		&protocol, "protocol", "",
		"Protocol: http or https",
	)
	f.StringVar(
		&hostRegexp, "host-regexp", "",
		"Host header regexp pattern",
	)
	f.StringVar(
		&pathRegexp, "path-regexp", "",
		"URL path regexp pattern",
	)
	f.Int64Var(&price, "price", 0, "Price in satoshis")
	f.StringVar(
		&auth, "auth", "",
		"Auth level: on, off, or freebie N",
	)

	return cmd
}

func newServicesDeleteCmd() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a backend service",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return ErrInvalidArgsf("--name is required")
			}

			req := &adminrpc.DeleteServiceRequest{
				Name: name,
			}

			if flags.dryRun {
				if err := printDryRun(
					"DeleteService", req,
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

			resp, err := client.DeleteService(
				context.Background(), req,
			)
			if err != nil {
				return ErrConnectionWrap(err)
			}

			if isJSONOutput(cmd) {
				return printProto(resp)
			}

			fmt.Printf(
				"Service %q deleted: %s\n",
				name, resp.Status,
			)
			return nil
		},
	}

	cmd.Flags().StringVar(
		&name, "name", "", "Service name (required)",
	)

	return cmd
}
