// Package meterd implements a reference token-metering price server for
// aperture's dynamicprice.metered mode. It sells prepaid bundles of LLM
// tokens over L402: a client pays one Lightning invoice for a bundle of
// tokens, draws it down across requests, and receives a fresh 402 challenge
// once the bundle is spent.
package meterd

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	flags "github.com/jessevdk/go-flags"
)

// Main is the true entry point of the meterd daemon. It is factored out so
// that a thin main package can call it.
func Main() {
	cfg, err := LoadConfig(os.Args[1:])
	if err != nil {
		// The flags package already prints help output itself, so a
		// help request is not an error.
		var flagErr *flags.Error
		if errors.As(err, &flagErr) &&
			flagErr.Type == flags.ErrHelp {

			os.Exit(0)
		}

		_, _ = fmt.Fprintf(os.Stderr, "Error loading config: %v\n",
			err)
		os.Exit(1)
	}

	server, err := NewServer(cfg)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error creating server: %v\n",
			err)
		os.Exit(1)
	}

	if err := server.Start(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error starting server: %v\n",
			err)
		os.Exit(1)
	}

	// Run until interrupted.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Infof("Received shutdown signal, stopping server")
	server.Stop()
}
