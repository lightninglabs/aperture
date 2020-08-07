package aperture

import (
	"crypto/tls"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/cert"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/tor"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"gopkg.in/yaml.v2"
)

const (
	// topLevelKey is the top level key for an etcd cluster where we'll
	// store all LSAT proxy related data.
	topLevelKey = "lsat/proxy"

	// etcdKeyDelimeter is the delimeter we'll use for all etcd keys to
	// represent a path-like structure.
	etcdKeyDelimeter = "/"

	// selfSignedCertValidity is the certificate validity duration we are
	// using for aperture certificates. This is higher than lnd's default
	// 14 months and is set to a maximum just below what some operating
	// systems set as a sane maximum certificate duration. See
	// https://support.apple.com/en-us/HT210176 for more information.
	selfSignedCertValidity = time.Hour * 24 * 820

	// selfSignedCertExpiryMargin is how much time before the certificate's
	// expiry date we already refresh it with a new one. We set this to half
	// the certificate validity length to make the chances bigger for it to
	// be refreshed on a routine server restart.
	selfSignedCertExpiryMargin = selfSignedCertValidity / 2
)

var (
	// http2TLSCipherSuites is the list of cipher suites we allow the server
	// to use. This list removes a CBC cipher from the list used in lnd's
	// cert package because the underlying HTTP/2 library treats it as a bad
	// cipher, according to https://tools.ietf.org/html/rfc7540#appendix-A
	// (also see golang.org/x/net/http2/ciphers.go).
	http2TLSCipherSuites = []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
	}
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
	configFile := filepath.Join(apertureDataDir, defaultConfigFilename)
	cfg, err := getConfig(configFile)
	if err != nil {
		return fmt.Errorf("unable to parse config file: %v", err)
	}
	err = setupLogging(cfg)
	if err != nil {
		return fmt.Errorf("unable to set up logging: %v", err)
	}

	// Initialize our etcd client.
	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{cfg.Etcd.Host},
		DialTimeout: 5 * time.Second,
		Username:    cfg.Etcd.User,
		Password:    cfg.Etcd.Password,
	})
	if err != nil {
		return fmt.Errorf("unable to connect to etcd: %v", err)
	}

	// Create the proxy and connect it to lnd.
	genInvoiceReq := func(price int64) (*lnrpc.Invoice, error) {
		return &lnrpc.Invoice{
			Memo:  "LSAT",
			Value: price,
		}, nil
	}
	servicesProxy, err := createProxy(cfg, genInvoiceReq, etcdClient)
	if err != nil {
		return err
	}
	handler := http.HandlerFunc(servicesProxy.ServeHTTP)
	httpsServer := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: handler,
	}

	// Create TLS configuration by either creating new self-signed certs or
	// trying to obtain one through Let's Encrypt.
	var serveFn func() error
	if cfg.Insecure {
		// Normally, HTTP/2 only works with TLS. But there is a special
		// version called HTTP/2 Cleartext (h2c) that some clients
		// support and that gRPC uses when the grpc.WithInsecure()
		// option is used. The default HTTP handler doesn't support it
		// though so we need to add a special h2c handler here.
		serveFn = httpsServer.ListenAndServe
		httpsServer.Handler = h2c.NewHandler(handler, &http2.Server{})
	} else {
		httpsServer.TLSConfig, err = getTLSConfig(
			cfg.ServerName, cfg.AutoCert,
		)
		if err != nil {
			return err
		}
		serveFn = func() error {
			// The httpsServer.TLSConfig contains certificates at
			// this point so we don't need to pass in certificate
			// and key file names.
			return httpsServer.ListenAndServeTLS("", "")
		}
	}

	// The ListenAndServeTLS below will block until shut down or an error
	// occurs. So we can just defer a cleanup function here that will close
	// everything on shutdown.
	defer cleanup(etcdClient, httpsServer)

	// Finally start the server.
	log.Infof("Starting the server, listening on %s.", cfg.ListenAddr)

	errChan := make(chan error)
	go func() {
		errChan <- serveFn()
	}()

	// If we need to listen over Tor as well, we'll set up the onion
	// services now. We're not able to use TLS for onion services since they
	// can't be verified, so we'll spin up an additional HTTP/2 server
	// _without_ TLS that is not exposed to the outside world. This server
	// will only be reached through the onion services, which already
	// provide encryption, so running this additional HTTP server should be
	// relatively safe.
	if cfg.Tor != nil && (cfg.Tor.V2 || cfg.Tor.V3) {
		torController, err := initTorListener(cfg, etcdClient)
		if err != nil {
			return err
		}
		defer func() {
			_ = torController.Stop()
		}()

		httpServer := &http.Server{
			Addr:    fmt.Sprintf("localhost:%d", cfg.Tor.ListenPort),
			Handler: h2c.NewHandler(handler, &http2.Server{}),
		}
		go func() {
			errChan <- httpServer.ListenAndServe()
		}()
		defer httpServer.Close()
	}

	return <-errChan
}

// fileExists reports whether the named file or directory exists.
// This function is taken from https://github.com/btcsuite/btcd
func fileExists(name string) bool {
	if _, err := os.Stat(name); err != nil {
		if os.IsNotExist(err) {
			return false
		}
	}
	return true
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
	logFile := filepath.Join(apertureDataDir, defaultLogFilename)
	err := logWriter.InitLogRotator(
		logFile, defaultMaxLogFileSize, defaultMaxLogFiles,
	)
	if err != nil {
		return err
	}
	return build.ParseAndSetDebugLevels(cfg.DebugLevel, logWriter)
}

// getTLSConfig returns a TLS configuration for either a self-signed certificate
// or one obtained through Let's Encrypt.
func getTLSConfig(serverName string, autoCert bool) (*tls.Config, error) {
	// If requested, use the autocert library that will create a new
	// certificate through Let's Encrypt as soon as the first client HTTP
	// request on the server using the TLS config comes in. Unfortunately
	// you cannot tell the library to create a certificate on startup for a
	// specific host.
	if autoCert {
		serverName := serverName
		if serverName == "" {
			return nil, fmt.Errorf("servername option is " +
				"required for secure operation")
		}

		certDir := filepath.Join(apertureDataDir, "autocert")
		log.Infof("Configuring autocert for server %v with cache dir "+
			"%v", serverName, certDir)

		manager := autocert.Manager{
			Cache:      autocert.DirCache(certDir),
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(serverName),
		}

		go func() {
			err := http.ListenAndServe(
				":http", manager.HTTPHandler(nil),
			)
			if err != nil {
				log.Errorf("autocert http: %v", err)
			}
		}()
		return &tls.Config{
			GetCertificate: manager.GetCertificate,
			CipherSuites:   http2TLSCipherSuites,
			MinVersion:     tls.VersionTLS12,
		}, nil
	}

	// If we're not using autocert, we want to create self-signed TLS certs
	// and save them at the specified location (if they don't already
	// exist).
	tlsKeyFile := filepath.Join(apertureDataDir, defaultTLSKeyFilename)
	tlsCertFile := filepath.Join(apertureDataDir, defaultTLSCertFilename)
	if !fileExists(tlsCertFile) && !fileExists(tlsKeyFile) {
		log.Infof("Generating TLS certificates...")
		err := cert.GenCertPair(
			"aperture autogenerated cert", tlsCertFile, tlsKeyFile,
			nil, nil, selfSignedCertValidity,
		)
		if err != nil {
			return nil, err
		}
		log.Infof("Done generating TLS certificates")
	}

	// Load the certs now so we can inspect it and return a complete TLS
	// config later.
	certData, parsedCert, err := cert.LoadCert(tlsCertFile, tlsKeyFile)
	if err != nil {
		return nil, err
	}

	// The margin is negative, so adding it to the expiry date should give
	// us a date in about the middle of it's validity period.
	expiryWithMargin := parsedCert.NotAfter.Add(
		-1 * selfSignedCertExpiryMargin,
	)

	// If the certificate expired or it was outdated, delete it and the TLS
	// key and generate a new pair.
	if time.Now().After(expiryWithMargin) {
		log.Info("TLS certificate will expire soon, generating a " +
			"new one")

		err := os.Remove(tlsCertFile)
		if err != nil {
			return nil, err
		}

		err = os.Remove(tlsKeyFile)
		if err != nil {
			return nil, err
		}

		log.Infof("Renewing TLS certificates...")
		err = cert.GenCertPair(
			"aperture autogenerated cert", tlsCertFile, tlsKeyFile,
			nil, nil, selfSignedCertValidity,
		)
		if err != nil {
			return nil, err
		}
		log.Infof("Done renewing TLS certificates")

		// Reload the certificate data.
		certData, _, err = cert.LoadCert(tlsCertFile, tlsKeyFile)
		if err != nil {
			return nil, err
		}
	}
	return &tls.Config{
		Certificates: []tls.Certificate{certData},
		CipherSuites: http2TLSCipherSuites,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// initTorListener initiates a Tor controller instance with the Tor server
// specified in the config. Onion services will be created over which the proxy
// can be reached at.
func initTorListener(cfg *config, etcd *clientv3.Client) (*tor.Controller, error) {
	// Establish a controller connection with the backing Tor server and
	// proceed to create the requested onion services.
	onionCfg := tor.AddOnionConfig{
		VirtualPort: int(cfg.Tor.VirtualPort),
		TargetPorts: []int{int(cfg.Tor.ListenPort)},
		Store:       newOnionStore(etcd),
	}
	torController := tor.NewController(cfg.Tor.Control, "", "")
	if err := torController.Start(); err != nil {
		return nil, err
	}

	if cfg.Tor.V2 {
		onionCfg.Type = tor.V2
		addr, err := torController.AddOnion(onionCfg)
		if err != nil {
			return nil, err
		}

		log.Infof("Listening over Tor on %v", addr)
	}

	if cfg.Tor.V3 {
		onionCfg.Type = tor.V3
		addr, err := torController.AddOnion(onionCfg)
		if err != nil {
			return nil, err
		}

		log.Infof("Listening over Tor on %v", addr)
	}

	return torController, nil
}

// createProxy creates the proxy with all the services it needs.
func createProxy(cfg *config, genInvoiceReq InvoiceRequestGenerator,
	etcdClient *clientv3.Client) (*proxy.Proxy, error) {

	challenger, err := NewLndChallenger(cfg.Authenticator, genInvoiceReq)
	if err != nil {
		return nil, err
	}
	minter := mint.New(&mint.Config{
		Challenger:     challenger,
		Secrets:        newSecretStore(etcdClient),
		ServiceLimiter: newStaticServiceLimiter(cfg.Services),
	})
	authenticator := auth.NewLsatAuthenticator(minter, challenger)
	return proxy.New(
		authenticator, cfg.Services, cfg.ServeStatic, cfg.StaticRoot,
	)
}

// cleanup closes the given server and shuts down the log rotator.
func cleanup(etcdClient io.Closer, server io.Closer) {
	if err := etcdClient.Close(); err != nil {
		log.Errorf("Error terminating etcd client: %v", err)
	}
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
