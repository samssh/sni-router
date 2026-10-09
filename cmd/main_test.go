package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sni-router/internal/monitoring"
	"sni-router/internal/server"

	"github.com/prometheus/client_golang/prometheus"
)

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitDial(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never accepted: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReload(t *testing.T) {
	addr := freePort(t)
	path := filepath.Join(t.TempDir(), "routing.yaml")
	if err := os.WriteFile(path, []byte(`
routes:
  - domain: default
    host: 127.0.0.1
    port: 443
listeners:
  - addr: `+addr+`
`), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := server.NewManager(monitoring.NewMetricsWithRegisterer(prometheus.NewRegistry()), 0)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := mgr.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	if err := reload(path, mgr); err != nil {
		t.Fatal(err)
	}
	waitDial(t, addr)

	if err := os.WriteFile(path, []byte("::: not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := reload(path, mgr)
	if err == nil || !strings.Contains(err.Error(), "keeping previous config") {
		t.Fatalf("err = %v, want keeping previous config", err)
	}
	waitDial(t, addr)
}

func TestLoadSettings(t *testing.T) {
	s, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.shutdownTimeout != 30*time.Second || s.metricsPort != 9113 || s.maxConns != 0 {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	for _, name := range []string{"SHUTDOWN_TIMEOUT_SECONDS", "METRICS_PORT", "MAX_CONNECTIONS"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "30s")
			if _, err := loadSettings(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("err = %v, want a parse error naming %s", err, name)
			}
		})
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
