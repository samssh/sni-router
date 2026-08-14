package main

import (
	"os"
	"path/filepath"
	"testing"

	"sni-router/internal/monitoring"
	"sni-router/internal/routing"
	"sni-router/internal/server"

	"github.com/prometheus/client_golang/prometheus"
)

func TestReloadRouter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.yaml")
	if err := os.WriteFile(path, []byte(`
- domain: default
  host: 127.0.0.1
  port: 443
`), 0o644); err != nil {
		t.Fatal(err)
	}
	router, err := routing.NewSNIRouter([]routing.Route{
		{Domain: "default", Host: "127.0.0.1", Port: 9},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener := server.NewListener(router, monitoring.NewMetricsWithRegisterer(prometheus.NewRegistry()), 0)
	if err := reloadRouter(path, listener); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("::: not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := reloadRouter(path, listener); err == nil {
		t.Fatal("expected error for invalid yaml")
	}
}
