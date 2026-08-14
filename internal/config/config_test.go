package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRoutingConfig(t *testing.T) {
	t.Run("valid yaml", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "routing.yaml")
		content := `
- domain: prom.example.com
  host: 10.0.0.1
  port: 8443
  useProxy: true
- domain: default
  host: 127.0.0.1
  port: 443
`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		routes, err := LoadRoutingConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(routes) != 2 {
			t.Fatalf("got %d routes, want 2", len(routes))
		}
		if routes[0].Domain != "prom.example.com" || routes[0].Host != "10.0.0.1" || routes[0].Port != 8443 || !routes[0].UseProxy {
			t.Fatalf("unexpected first route: %+v", routes[0])
		}
		if routes[1].Domain != "default" {
			t.Fatalf("unexpected second route: %+v", routes[1])
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := LoadRoutingConfig(filepath.Join(t.TempDir(), "missing.yaml"))
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("invalid yaml", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "routing.yaml")
		if err := os.WriteFile(path, []byte("::: not yaml"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadRoutingConfig(path)
		if err == nil {
			t.Fatal("expected error for invalid yaml")
		}
	})
}
