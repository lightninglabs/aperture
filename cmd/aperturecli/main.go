// Package main is the entry point for the aperturecli CLI.
package main

import (
	"fmt"
	"os"

	"github.com/lightninglabs/aperture/cli"
	"golang.org/x/term"
)

func main() {
	rootCmd := cli.NewRootCmd()

	err := rootCmd.Execute()
	if err != nil {
		code := cli.ExitCode(err)

		// Emit structured JSON error on stderr when stdout is
		// not a TTY (agent/pipe mode), or when the error itself
		// is a CLIError (structured errors always get JSON).
		if !term.IsTerminal(int(os.Stdout.Fd())) {
			cli.WriteErrorJSON(os.Stderr, err)
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}

		os.Exit(code)
	}
}
