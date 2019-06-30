package kirin

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"

	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/kirin/proxy"
)

/**
 */

// Main is the true entrypoint of Kirin.
func Main() {
	// TODO: Prevent from running twice.
	err := start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func start() error {
	configFile := filepath.Join(kirinDataDir, defaultConfigFilename)
	var cfg config
	b, err := ioutil.ReadFile(configFile)
	if err != nil {
		return err
	}

	yaml.Unmarshal(b, &cfg)
	if cfg.ListenAddr == "" {
		return fmt.Errorf("missing listen address for server")
	}

	authenticator := auth.NewMockAuthenticator()
	servicesProxy, err := proxy.New(*authenticator, cfg.Services)
	if err != nil {
		return err
	}

	// Start the reverse proxy.
	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: http.HandlerFunc(servicesProxy.ServeHTTP),
	}

	tlsKeyFile := filepath.Join(kirinDataDir, defaultTLSKeyFilename)
	tlsCertFile := filepath.Join(kirinDataDir, defaultTLSCertFilename)
	return server.ListenAndServeTLS(tlsCertFile, tlsKeyFile)
}
