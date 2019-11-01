package kirin

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	
	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/kirin/proxy"
	"gopkg.in/yaml.v2"
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

	err = yaml.Unmarshal(b, &cfg)
	if err != nil {
		return err
	}
	if cfg.ListenAddr == "" {
		return fmt.Errorf("missing listen address for server")
	}

	authenticator, err := auth.NewLndAuthenticator(cfg.Authenticator)
	if err != nil {
		return err
	}
	servicesProxy, err := proxy.New(authenticator, cfg.Services)
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
