package secrets

import (
	"net/url"
	"os"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
)

// EtcdSetup is a helper that instantiates a new etcd cluster along with a
// client connection to it. A cleanup closure is also returned to free any
// allocated resources required by etcd.
func EtcdSetup(t *testing.T) (*clientv3.Client, func()) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "etcd")
	if err != nil {
		t.Fatalf("unable to create temp dir: %v", err)
	}

	cfg := embed.NewConfig()
	cfg.Dir = tempDir
	cfg.Logger = "zap"
	cfg.LCUrls = []url.URL{{Host: "127.0.0.1:9125"}}
	cfg.LPUrls = []url.URL{{Host: "127.0.0.1:9126"}}

	etcd, err := embed.StartEtcd(cfg)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("unable to start etcd: %v", err)
	}

	select {
	case <-etcd.Server.ReadyNotify():
	case <-time.After(5 * time.Second):
		os.RemoveAll(tempDir)
		etcd.Server.Stop() // trigger a shutdown
		t.Fatal("server took too long to start")
	}

	client, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{cfg.LCUrls[0].Host},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("unable to connect to etcd: %v", err)
	}

	return client, func() {
		etcd.Close()
		os.RemoveAll(tempDir)
	}
}
