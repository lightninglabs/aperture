package cli

import (
	"encoding/json"
	"io"
	"os"

	"golang.org/x/term"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	// outputJSON is the JSON output format identifier.
	outputJSON = "json"

	// outputHuman is the human-readable output format identifier.
	outputHuman = "human"
)

// resolveOutputFormat determines the output format by checking (in
// priority order): the --json flag, the --human flag, and then TTY
// detection. When stdout is not a TTY, JSON is the default so agents
// piping output always get machine-readable results.
func resolveOutputFormat() string {
	if flags.jsonOutput {
		return outputJSON
	}

	if flags.humanOutput {
		return outputHuman
	}

	// Fall back to TTY detection: non-TTY defaults to JSON.
	if !isTTY() {
		return outputJSON
	}

	return outputHuman
}

// isJSONOutput is a convenience wrapper that returns true when the
// resolved format is JSON.
func isJSONOutput() bool {
	return resolveOutputFormat() == outputJSON
}

// isTTY reports whether stdout is connected to an interactive terminal.
func isTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// writeJSON encodes data as indented JSON to the given writer.
func writeJSON(w io.Writer, data any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")

	return encoder.Encode(data)
}

// printProto marshals a protobuf message as indented JSON to stdout
// using protojson for correct field naming.
func printProto(msg proto.Message) error {
	marshaler := protojson.MarshalOptions{
		Indent:          "  ",
		EmitUnpopulated: true,
	}

	data, err := marshaler.Marshal(msg)
	if err != nil {
		return err
	}

	_, err = os.Stdout.Write(append(data, '\n'))
	return err
}

// printDryRun outputs a dry-run representation of the request that
// would be sent, without actually executing the RPC.
func printDryRun(rpcName string, req proto.Message) error {
	marshaler := protojson.MarshalOptions{
		Indent:          "  ",
		EmitUnpopulated: true,
	}

	reqData, err := marshaler.Marshal(req)
	if err != nil {
		return err
	}

	// Use json.RawMessage to embed the protojson output directly
	// without a double-serialize round-trip.
	envelope := map[string]any{
		"dry_run": true,
		"rpc":     rpcName,
		"request": json.RawMessage(reqData),
	}

	return writeJSON(os.Stdout, envelope)
}
