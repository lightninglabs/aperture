package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/lightninglabs/aperture/adminrpc"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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

			rpcCtx, cancel := rpcTimeout(cmd.Context())
			defer cancel()

			resp, err := client.ListServices(
				rpcCtx,
				&adminrpc.ListServicesRequest{},
			)
			if err != nil {
				return mapGRPCError(err)
			}

			if isJSONOutput() {
				return printProto(resp)
			}

			w := tabwriter.NewWriter(
				os.Stdout, 0, 0, 2, ' ', 0,
			)
			fmt.Fprintln(
				w, "NAME\tADDRESS\tPROTOCOL\tPRICE\t"+
					"AUTH\tPAYMENT_LND",
			)

			for _, s := range resp.Services {
				lnd := "-"
				if s.Payment != nil && s.Payment.LndHost != "" {
					lnd = s.Payment.LndHost
				}
				fmt.Fprintf(
					w, "%s\t%s\t%s\t%d\t%s\t%s\n",
					s.Name, s.Address,
					s.Protocol, s.Price, s.Auth, lnd,
				)
			}

			return w.Flush()
		},
	}
}

func newServicesCreateCmd() *cobra.Command {
	var (
		name           string
		address        string
		protocol       string
		hostRegexp     string
		pathRegexp     string
		price          int64
		auth           string
		paymentLndhost string
		paymentTLSPath string
		paymentMacPath string
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

			payment, err := buildPaymentBackend(
				paymentLndhost, paymentTLSPath, paymentMacPath,
			)
			if err != nil {
				return err
			}

			req := &adminrpc.CreateServiceRequest{
				Name:       name,
				Address:    address,
				Protocol:   protocol,
				HostRegexp: hostRegexp,
				PathRegexp: pathRegexp,
				Price:      price,
				Auth:       auth,
				Payment:    payment,
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

			rpcCtx, cancel := rpcTimeout(cmd.Context())
			defer cancel()

			resp, err := client.CreateService(
				rpcCtx, req,
			)
			if err != nil {
				return mapGRPCError(err)
			}

			if isJSONOutput() {
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
	addPaymentFlags(
		f, &paymentLndhost, &paymentTLSPath, &paymentMacPath,
	)

	return cmd
}

func newServicesUpdateCmd() *cobra.Command {
	var (
		name           string
		address        string
		protocol       string
		hostRegexp     string
		pathRegexp     string
		price          int64
		auth           string
		paymentLndhost string
		paymentTLSPath string
		paymentMacPath string
		clearPayment   bool
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update an existing service",
		Long: `Update one or more fields of an existing service. Only flags
that are explicitly provided will be changed.

Examples:
  # Change price only:
  prismcli services update --name myapi --price 500

  # Change address and protocol:
  prismcli services update --name myapi --address 10.0.0.5:8080 --protocol https`,
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

			paymentSet := cmd.Flags().Changed("payment-lndhost") ||
				cmd.Flags().Changed("payment-tlspath") ||
				cmd.Flags().Changed("payment-macpath")
			if paymentSet && clearPayment {
				return ErrInvalidArgsf(
					"--payment-* and --clear-payment " +
						"are mutually exclusive",
				)
			}
			if paymentSet {
				payment, err := buildPaymentBackend(
					paymentLndhost, paymentTLSPath,
					paymentMacPath,
				)
				if err != nil {
					return err
				}
				req.Payment = payment
			}
			if clearPayment {
				req.ClearPayment = true
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

			rpcCtx, cancel := rpcTimeout(cmd.Context())
			defer cancel()

			resp, err := client.UpdateService(
				rpcCtx, req,
			)
			if err != nil {
				return mapGRPCError(err)
			}

			if isJSONOutput() {
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
	addPaymentFlags(
		f, &paymentLndhost, &paymentTLSPath, &paymentMacPath,
	)
	f.BoolVar(
		&clearPayment, "clear-payment", false,
		"Remove any existing per-service lnd override (returns "+
			"the service to the global default lnd). Mutually "+
			"exclusive with --payment-*.",
	)

	return cmd
}

//nolint:dupl
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

			rpcCtx, cancel := rpcTimeout(cmd.Context())
			defer cancel()

			resp, err := client.DeleteService(
				rpcCtx, req,
			)
			if err != nil {
				return mapGRPCError(err)
			}

			if isJSONOutput() {
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

// addPaymentFlags registers the --payment-lndhost, --payment-tlspath and
// --payment-macpath flags on f, binding them to the passed-in string
// pointers. Kept in one place so the create and update commands stay in
// sync with each other and with the server-side validation rules.
func addPaymentFlags(f *pflag.FlagSet, lndHost, tlsPath, macPath *string) {
	f.StringVar(
		lndHost, "payment-lndhost", "",
		"Merchant lnd gRPC host:port (enables per-service lnd "+
			"routing; requires --payment-tlspath and "+
			"--payment-macpath)",
	)
	f.StringVar(
		tlsPath, "payment-tlspath", "",
		"Absolute path to the merchant lnd's tls.cert on the "+
			"gateway host",
	)
	f.StringVar(
		macPath, "payment-macpath", "",
		"Absolute path to the merchant's minimum-privilege "+
			"macaroon file (invoices:read invoices:write "+
			"info:read)",
	)
}

// buildPaymentBackend validates that the three --payment-* values are
// either all empty (no per-service override, use the global lnd) or all
// set (per-service override). Returns a PaymentBackend pointer on the
// all-set path and nil on the all-empty path. Mirrors the server-side
// check so the user gets a clear error before the gRPC round-trip.
func buildPaymentBackend(lndHost, tlsPath, macPath string) (
	*adminrpc.PaymentBackend, error) {

	allEmpty := lndHost == "" && tlsPath == "" && macPath == ""
	allSet := lndHost != "" && tlsPath != "" && macPath != ""

	switch {
	case allEmpty:
		return nil, nil
	case allSet:
		return &adminrpc.PaymentBackend{
			LndHost: lndHost,
			TlsPath: tlsPath,
			MacPath: macPath,
		}, nil
	default:
		return nil, ErrInvalidArgsf(
			"--payment-lndhost, --payment-tlspath and " +
				"--payment-macpath must all be set together " +
				"or all be omitted",
		)
	}
}
