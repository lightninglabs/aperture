package kirin

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"

	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/kirin/proxy"
	"github.com/lightningnetwork/lnd/build"
	"gopkg.in/yaml.v2"
)

// Main is the true entrypoint of Kirin.
func Main() {
	// TODO: Prevent from running twice.
	err := start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// start sets up the proxy server and runs it. This function blocks until a
// shutdown signal is received.
func start() error {
	// First, parse configuration file and set up logging.
	configFile := filepath.Join(kirinDataDir, defaultConfigFilename)
	cfg, err := getConfig(configFile)
	if err != nil {
		return fmt.Errorf("unable to parse config file: %v", err)
	}
	err = setupLogging(cfg)
	if err != nil {
		return fmt.Errorf("unable to set up logging: %v", err)
	}

	// Create the auxiliary services the proxy needs to work.
	authenticator, err := auth.NewLndAuthenticator(cfg.Authenticator)
	if err != nil {
		return err
	}
	servicesProxy, err := proxy.New(authenticator, cfg.Services)
	if err != nil {
		return err
	}

	// Finally start the reverse proxy.
	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: http.HandlerFunc(servicesProxy.ServeHTTP),
	}
	tlsKeyFile := filepath.Join(kirinDataDir, defaultTLSKeyFilename)
	tlsCertFile := filepath.Join(kirinDataDir, defaultTLSCertFilename)

	// The ListenAndServeTLS below will block until shut down or an error
	// occurs. So we can just defer a cleanup function here that will close
	// everything on shutdown.
	defer cleanup(server)
	return server.ListenAndServeTLS(tlsCertFile, tlsKeyFile)
}

// getConfig loads and parses the configuration file then checks it for valid
// content.
func getConfig(configFile string) (*config, error) {
	cfg := &config{}
	b, err := ioutil.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	err = yaml.Unmarshal(b, cfg)
	if err != nil {
		return nil, err
	}

	// Then check the configuration that we got from the config file, all
	// required values need to be set at this point.
	if cfg.ListenAddr == "" {
		return nil, fmt.Errorf("missing listen address for server")
	}
	return cfg, nil
}

// setupLogging parses the debug level and initializes the log file rotator.
func setupLogging(cfg *config) error {
	if cfg.DebugLevel == "" {
		cfg.DebugLevel = defaultLogLevel
	}

	// Now initialize the logger and set the log level.
	logFile := filepath.Join(kirinDataDir, defaultLogFilename)
	err := logWriter.InitLogRotator(
		logFile, defaultMaxLogFileSize, defaultMaxLogFiles,
	)
	if err != nil {
		return err
	}
	return build.ParseAndSetDebugLevels(cfg.DebugLevel, logWriter)
}

// cleanup closes the given server and shuts down the log rotator.
func cleanup(server *http.Server) {
	err := server.Close()
	if err != nil {
		log.Errorf("Error closing server: %v", err)
	}
	log.Info("Shutdown complete")
	err = logWriter.Close()
	if err != nil {
		log.Errorf("Could not close log rotator: %v", err)
	}
}
