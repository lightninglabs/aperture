package aperture

import (
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// mailboxCount tracks the current number of active mailboxes.
	mailboxCount = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "hashmail",
		Name:      "mailbox_count",
	})

	// activeSessions tracks the active session count for mailbox.
	activeSessions = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "hashmail",
		Name:      "mailbox_active_sessions",
		Help:      "Number of active sessions",
	})

	// standbySessions tracks the standby session count for mailbox.
	standbySessions = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "hashmail",
		Name:      "mailbox_standby_sessions",
		Help:      "Number of standby sessions",
	})

	// inUseSessions tracks the in-use session count for mailbox.
	inUseSessions = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "hashmail",
		Name:      "mailbox_inuse_sessions",
		Help:      "Number of in-use sessions",
	})
)

// streamActivityTracker handles the calculation of session statistics.
var streamActivityTracker = newStreamActivity()

// PrometheusConfig is the set of configuration data that specifies if
// Prometheus metric exporting is activated, and if so the listening address of
// the Prometheus server.
type PrometheusConfig struct {
	// Enabled, if true, then Prometheus metrics will be exported.
	Enabled bool `long:"enabled" description:"if true prometheus metrics will be exported"`

	// ListenAddr is the listening address that we should use to allow the
	// main Prometheus server to scrape our metrics.
	ListenAddr string `long:"listenaddr" description:"the interface we should listen on for prometheus"`
}

// StartPrometheusExporter registers all relevant metrics with the Prometheus
// library, then launches the HTTP server that Prometheus will hit to scrape
// our metrics.
func StartPrometheusExporter(cfg *PrometheusConfig,
	shutdown <-chan struct{}) error {

	// If we're not active, then there's nothing more to do.
	if !cfg.Enabled {
		return nil
	}

	// Next, we'll register all our metrics.
	prometheus.MustRegister(mailboxCount)
	prometheus.MustRegister(activeSessions)
	prometheus.MustRegister(standbySessions)
	prometheus.MustRegister(inUseSessions)

	// Periodically update session classification metrics from internal tracker.
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				active, standby, inuse :=
					streamActivityTracker.ClassifyAndReset()
				activeSessions.Set(float64(active))
				standbySessions.Set(float64(standby))
				inUseSessions.Set(float64(inuse))
			case <-shutdown:
				log.Infof("Shutting down Prometheus session metrics updater")
				return
			}
		}
	}()

	// Finally, we'll launch the HTTP server that Prometheus will use to
	// scape our metrics.
	go func() {
		log.Infof("Prometheus metrics http endpoint being served on "+
			"%s", cfg.ListenAddr)

		http.Handle("/metrics", promhttp.Handler())
		fmt.Println(http.ListenAndServe(cfg.ListenAddr, nil))
	}()

	return nil
}
