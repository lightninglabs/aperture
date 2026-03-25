package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// CommandSchema is the machine-readable schema for a CLI command,
// designed for agent introspection via aperturecli schema.
type CommandSchema struct {
	// Name is the command name (e.g. "services").
	Name string `json:"name"`

	// FullPath is the fully qualified command path
	// (e.g. "aperturecli services list").
	FullPath string `json:"full_path"`

	// Description is the short description of the command.
	Description string `json:"description"`

	// LongDescription is the detailed description, if available.
	LongDescription string `json:"long_description,omitempty"`

	// Args describes the positional arguments.
	Args []ArgSchema `json:"args,omitempty"`

	// Flags lists all flags accepted by this command.
	Flags []FlagSchema `json:"flags,omitempty"`

	// Subcommands lists child commands, if any.
	Subcommands []CommandSchema `json:"subcommands,omitempty"`

	// ExitCodes documents the semantic exit codes.
	ExitCodes map[int]string `json:"exit_codes,omitempty"`
}

// ArgSchema describes a positional argument.
type ArgSchema struct {
	// Name is the argument name.
	Name string `json:"name"`

	// Required indicates whether the argument is mandatory.
	Required bool `json:"required"`

	// Description explains the argument.
	Description string `json:"description"`
}

// FlagSchema describes a CLI flag.
type FlagSchema struct {
	// Name is the long flag name (without --).
	Name string `json:"name"`

	// Short is the single-character shorthand, if any.
	Short string `json:"short,omitempty"`

	// Type is the value type (string, bool, int, etc.).
	Type string `json:"type"`

	// Default is the default value as a string.
	Default string `json:"default,omitempty"`

	// Description explains the flag.
	Description string `json:"description"`
}

// exitCodeDocs is the canonical exit code documentation shared by all
// commands.
var exitCodeDocs = map[int]string{
	ExitSuccess:         "success",
	ExitGeneralError:    "general error",
	ExitInvalidArgs:     "invalid arguments",
	ExitConnectionError: "gRPC connection failure",
	ExitAuthFailure:     "macaroon authentication failure",
	ExitNotFound:        "resource not found",
	ExitDryRunPassed:    "dry-run completed (no action taken)",
}

// NewSchemaCmd creates the schema introspection subcommand. It allows
// agents to discover the CLI's commands, flags, and types at runtime
// without parsing --help text.
func NewSchemaCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "schema [command]",
		Short: "Show machine-readable CLI schema",
		Long: `Dump the CLI schema as JSON for agent introspection.

  aperturecli schema              List all commands
  aperturecli schema services     Show schema for the services command
  aperturecli schema --all        Full schema tree`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()

			if all {
				schema := buildSchema(root)
				return writeJSON(os.Stdout, schema)
			}

			if len(args) == 0 {
				var commands []CommandSchema
				for _, sub := range root.Commands() {
					if sub.Hidden ||
						sub.Name() == "help" ||
						sub.Name() == "completion" {

						continue
					}

					commands = append(
						commands, CommandSchema{
							Name:        sub.Name(),
							FullPath:    sub.CommandPath(),
							Description: sub.Short,
						},
					)
				}

				return writeJSON(os.Stdout, commands)
			}

			target, _, err := root.Find(
				splitCommandPath(args[0]),
			)
			if err != nil || target == nil {
				return ErrInvalidArgsf(
					"unknown command: %s", args[0],
				)
			}

			schema := buildSchema(target)
			return writeJSON(os.Stdout, schema)
		},
	}

	cmd.Flags().BoolVar(
		&all, "all", false, "Dump the full CLI schema tree",
	)

	return cmd
}

// buildSchema recursively builds a CommandSchema from a cobra command.
func buildSchema(cmd *cobra.Command) CommandSchema {
	schema := CommandSchema{
		Name:            cmd.Name(),
		FullPath:        cmd.CommandPath(),
		Description:     cmd.Short,
		LongDescription: cmd.Long,
		ExitCodes:       exitCodeDocs,
	}

	// Extract positional arg info from the Use string.
	schema.Args = extractArgs(cmd)

	// Extract flags.
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		fs := FlagSchema{
			Name:        f.Name,
			Type:        f.Value.Type(),
			Default:     f.DefValue,
			Description: f.Usage,
		}

		if f.Shorthand != "" {
			fs.Short = f.Shorthand
		}

		schema.Flags = append(schema.Flags, fs)
	})

	// Recurse into subcommands.
	for _, sub := range cmd.Commands() {
		if sub.Hidden || sub.Name() == "help" ||
			sub.Name() == "completion" {

			continue
		}

		schema.Subcommands = append(
			schema.Subcommands, buildSchema(sub),
		)
	}

	return schema
}

// extractArgs parses the command's Use string to extract positional
// argument descriptions.
func extractArgs(cmd *cobra.Command) []ArgSchema {
	use := cmd.Use
	parts := strings.Fields(use)

	var args []ArgSchema

	for _, p := range parts[1:] {
		if p == "[flags]" {
			continue
		}

		if strings.HasPrefix(p, "<") &&
			strings.HasSuffix(p, ">") {

			name := strings.Trim(p, "<>")
			args = append(args, ArgSchema{
				Name:     name,
				Required: true,
				Description: "Required: " +
					name,
			})
		} else if strings.HasPrefix(p, "[") &&
			strings.HasSuffix(p, "]") {

			name := strings.Trim(p, "[]")
			args = append(args, ArgSchema{
				Name:     name,
				Required: false,
				Description: "Optional: " +
					name,
			})
		}
	}

	return args
}

// splitCommandPath splits a dot-or-space-separated command path into
// individual command names for cmd.Find().
func splitCommandPath(path string) []string {
	if strings.Contains(path, " ") {
		return strings.Fields(path)
	}

	return strings.Split(path, ".")
}
