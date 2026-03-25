package mcpserver

import (
	"context"

	"github.com/lightninglabs/aperture/adminrpc"
	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// protoJSON is the shared marshaler for all tool responses.
var protoJSON = protojson.MarshalOptions{
	Indent:          "  ",
	EmitUnpopulated: true,
}

// protoResult wraps a JSON-serialized proto message for the MCP typed
// response pattern.
type protoResult struct {
	Data string `json:"data"`
}

// toResult marshals a proto message into a protoResult.
func toResult(msg proto.Message) (*protoResult, error) {
	data, err := protoJSON.Marshal(msg)
	if err != nil {
		return nil, err
	}

	return &protoResult{Data: string(data)}, nil
}

// registerTools adds all aperture admin tools to the MCP server.
func registerTools(
	server *gomcp.Server, client adminrpc.AdminClient) {

	// get_info — no parameters.
	gomcp.AddTool(
		server,
		&gomcp.Tool{
			Name:        "get_info",
			Description: "Get Aperture server information (network, listen address, TLS status)",
		},
		func(ctx context.Context,
			req *gomcp.CallToolRequest,
			args emptyArgs) (
			*gomcp.CallToolResult, *protoResult, error) {

			resp, err := client.GetInfo(
				ctx, &adminrpc.GetInfoRequest{},
			)
			if err != nil {
				return nil, nil, err
			}

			r, err := toResult(resp)
			return nil, r, err
		},
	)

	// get_health — no parameters.
	gomcp.AddTool(
		server,
		&gomcp.Tool{
			Name:        "get_health",
			Description: "Check Aperture server health",
		},
		func(ctx context.Context,
			req *gomcp.CallToolRequest,
			args emptyArgs) (
			*gomcp.CallToolResult, *protoResult, error) {

			resp, err := client.GetHealth(
				ctx, &adminrpc.GetHealthRequest{},
			)
			if err != nil {
				return nil, nil, err
			}

			r, err := toResult(resp)
			return nil, r, err
		},
	)

	// list_services — no parameters.
	gomcp.AddTool(
		server,
		&gomcp.Tool{
			Name:        "list_services",
			Description: "List all configured backend services",
		},
		func(ctx context.Context,
			req *gomcp.CallToolRequest,
			args emptyArgs) (
			*gomcp.CallToolResult, *protoResult, error) {

			resp, err := client.ListServices(
				ctx, &adminrpc.ListServicesRequest{},
			)
			if err != nil {
				return nil, nil, err
			}

			r, err := toResult(resp)
			return nil, r, err
		},
	)

	// create_service — all service fields.
	gomcp.AddTool(
		server,
		&gomcp.Tool{
			Name:        "create_service",
			Description: "Create a new backend service with pricing and auth configuration",
		},
		func(ctx context.Context,
			req *gomcp.CallToolRequest,
			args createServiceArgs) (
			*gomcp.CallToolResult, *protoResult, error) {

			resp, err := client.CreateService(
				ctx,
				&adminrpc.CreateServiceRequest{
					Name:       args.Name,
					Address:    args.Address,
					Protocol:   args.Protocol,
					HostRegexp: args.HostRegexp,
					PathRegexp: args.PathRegexp,
					Price:      args.Price,
					Auth:       args.Auth,
				},
			)
			if err != nil {
				return nil, nil, err
			}

			r, err := toResult(resp)
			return nil, r, err
		},
	)

	// update_service — name required, others optional.
	gomcp.AddTool(
		server,
		&gomcp.Tool{
			Name:        "update_service",
			Description: "Update an existing service (e.g. change price, address, auth)",
		},
		func(ctx context.Context,
			req *gomcp.CallToolRequest,
			args updateServiceArgs) (
			*gomcp.CallToolResult, *protoResult, error) {

			updateReq := &adminrpc.UpdateServiceRequest{
				Name:       args.Name,
				Address:    args.Address,
				Protocol:   args.Protocol,
				HostRegexp: args.HostRegexp,
				PathRegexp: args.PathRegexp,
				Auth:       args.Auth,
			}

			if args.Price != nil {
				updateReq.Price = proto.Int64(*args.Price)
			}

			resp, err := client.UpdateService(
				ctx, updateReq,
			)
			if err != nil {
				return nil, nil, err
			}

			r, err := toResult(resp)
			return nil, r, err
		},
	)

	// delete_service — name required.
	gomcp.AddTool(
		server,
		&gomcp.Tool{
			Name:        "delete_service",
			Description: "Delete a backend service by name",
		},
		func(ctx context.Context,
			req *gomcp.CallToolRequest,
			args deleteServiceArgs) (
			*gomcp.CallToolResult, *protoResult, error) {

			resp, err := client.DeleteService(
				ctx,
				&adminrpc.DeleteServiceRequest{
					Name: args.Name,
				},
			)
			if err != nil {
				return nil, nil, err
			}

			r, err := toResult(resp)
			return nil, r, err
		},
	)

	// list_transactions — optional filters.
	gomcp.AddTool(
		server,
		&gomcp.Tool{
			Name:        "list_transactions",
			Description: "List L402 transactions with optional filters",
		},
		func(ctx context.Context,
			req *gomcp.CallToolRequest,
			args listTransactionsArgs) (
			*gomcp.CallToolResult, *protoResult, error) {

			resp, err := client.ListTransactions(
				ctx,
				&adminrpc.ListTransactionsRequest{
					Service:   args.Service,
					State:     args.State,
					StartDate: args.From,
					EndDate:   args.To,
					Limit:     args.Limit,
					Offset:    args.Offset,
				},
			)
			if err != nil {
				return nil, nil, err
			}

			r, err := toResult(resp)
			return nil, r, err
		},
	)

	// list_tokens — optional pagination.
	gomcp.AddTool(
		server,
		&gomcp.Tool{
			Name:        "list_tokens",
			Description: "List all issued L402 tokens",
		},
		func(ctx context.Context,
			req *gomcp.CallToolRequest,
			args listTokensArgs) (
			*gomcp.CallToolResult, *protoResult, error) {

			resp, err := client.ListTokens(
				ctx,
				&adminrpc.ListTokensRequest{
					Limit:  args.Limit,
					Offset: args.Offset,
				},
			)
			if err != nil {
				return nil, nil, err
			}

			r, err := toResult(resp)
			return nil, r, err
		},
	)

	// revoke_token — token_id required.
	gomcp.AddTool(
		server,
		&gomcp.Tool{
			Name:        "revoke_token",
			Description: "Revoke an L402 token by ID",
		},
		func(ctx context.Context,
			req *gomcp.CallToolRequest,
			args revokeTokenArgs) (
			*gomcp.CallToolResult, *protoResult, error) {

			resp, err := client.RevokeToken(
				ctx,
				&adminrpc.RevokeTokenRequest{
					TokenId: args.TokenID,
				},
			)
			if err != nil {
				return nil, nil, err
			}

			r, err := toResult(resp)
			return nil, r, err
		},
	)

	// get_stats — optional date range.
	gomcp.AddTool(
		server,
		&gomcp.Tool{
			Name:        "get_stats",
			Description: "Get revenue statistics with optional date range and per-service breakdown",
		},
		func(ctx context.Context,
			req *gomcp.CallToolRequest,
			args getStatsArgs) (
			*gomcp.CallToolResult, *protoResult, error) {

			resp, err := client.GetStats(
				ctx,
				&adminrpc.GetStatsRequest{
					From: args.From,
					To:   args.To,
				},
			)
			if err != nil {
				return nil, nil, err
			}

			r, err := toResult(resp)
			return nil, r, err
		},
	)
}

// Tool input types.

// emptyArgs is used for tools with no parameters.
type emptyArgs struct{}

// createServiceArgs are the input parameters for the create_service
// tool.
type createServiceArgs struct {
	Name       string `json:"name" jsonschema:"required,description=Service name"`
	Address    string `json:"address" jsonschema:"required,description=Backend address (host:port)"`
	Protocol   string `json:"protocol,omitempty" jsonschema:"description=Protocol: http or https"`
	HostRegexp string `json:"host_regexp,omitempty" jsonschema:"description=Host header regexp pattern"`
	PathRegexp string `json:"path_regexp,omitempty" jsonschema:"description=URL path regexp pattern"`
	Price      int64  `json:"price" jsonschema:"required,description=Price in satoshis"`
	Auth       string `json:"auth,omitempty" jsonschema:"description=Auth level: on or off or freebie N"`
}

// updateServiceArgs are the input parameters for the update_service
// tool.
type updateServiceArgs struct {
	Name       string `json:"name" jsonschema:"required,description=Service name to update"`
	Address    string `json:"address,omitempty" jsonschema:"description=New backend address"`
	Protocol   string `json:"protocol,omitempty" jsonschema:"description=New protocol: http or https"`
	HostRegexp string `json:"host_regexp,omitempty" jsonschema:"description=New host regexp pattern"`
	PathRegexp string `json:"path_regexp,omitempty" jsonschema:"description=New path regexp pattern"`
	Price      *int64 `json:"price,omitempty" jsonschema:"description=New price in satoshis"`
	Auth       string `json:"auth,omitempty" jsonschema:"description=New auth level"`
}

// deleteServiceArgs are the input parameters for the delete_service
// tool.
type deleteServiceArgs struct {
	Name string `json:"name" jsonschema:"required,description=Service name to delete"`
}

// listTransactionsArgs are the input parameters for the
// list_transactions tool.
type listTransactionsArgs struct {
	Service string `json:"service,omitempty" jsonschema:"description=Filter by service name"`
	State   string `json:"state,omitempty" jsonschema:"description=Filter by state (pending or settled)"`
	From    string `json:"from,omitempty" jsonschema:"description=Start date (RFC3339)"`
	To      string `json:"to,omitempty" jsonschema:"description=End date (RFC3339)"`
	Limit   int32  `json:"limit,omitempty" jsonschema:"description=Max results to return"`
	Offset  int32  `json:"offset,omitempty" jsonschema:"description=Pagination offset"`
}

// listTokensArgs are the input parameters for the list_tokens tool.
type listTokensArgs struct {
	Limit  int32 `json:"limit,omitempty" jsonschema:"description=Max results to return"`
	Offset int32 `json:"offset,omitempty" jsonschema:"description=Pagination offset"`
}

// revokeTokenArgs are the input parameters for the revoke_token tool.
type revokeTokenArgs struct {
	TokenID string `json:"token_id" jsonschema:"required,description=Token ID to revoke"`
}

// getStatsArgs are the input parameters for the get_stats tool.
type getStatsArgs struct {
	From string `json:"from,omitempty" jsonschema:"description=Start date (RFC3339)"`
	To   string `json:"to,omitempty" jsonschema:"description=End date (RFC3339)"`
}
