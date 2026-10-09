package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sni-router/internal/monitoring"
	"sni-router/internal/server"

	"github.com/prometheus/client_golang/prometheus"
)

func TestReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.yaml")
	if err := os.WriteFile(path, []byte(`
routes:
  - domain: default
    host: 127.0.0.1
    port: 443
listeners:
  - addr: 127.0.0.1:18443
`), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := server.NewManager(monitoring.NewMetricsWithRegisterer(prometheus.NewRegistry()), 0)
	t.Cleanup(func() { _ = mgr.Shutdown(t.Context()) })
	if err := reload(path, mgr); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("::: not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := reload(path, mgr); err == nil {
		t.Fatal("expected error for invalid yaml")
	}
}

func TestCheckRemovedEnv(t *testing.T) {
	if err := checkRemovedEnv(); err != nil {
		t.Fatalf("unexpected error with no removed env set: %v", err)
	}
	for _, name := range []string{"LISTEN_ADDR", "LISTEN_PORT", "DROP_UID", "DROP_GID"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "1")
			err := checkRemovedEnv()
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("err = %v, want it to name %s", err, name)
			}
		})
	}
}

func TestSetupLogging(t *testing.T) {
	for _, level := range []string{"debug", "info", "WARN", "error"} {
		if err := setupLogging(level); err != nil {
			t.Fatalf("level %q: %v", level, err)
		}
	}
	if err := setupLogging("verbose"); err == nil {
		t.Fatal("expected error for unknown level")
	}
}
