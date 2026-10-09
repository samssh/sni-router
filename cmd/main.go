package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sni-router/internal/config"
	"sni-router/internal/monitoring"
	"sni-router/internal/server"
	"strconv"
	"syscall"
	"time"
)

const addrCheckInterval = 30 * time.Second

// removedEnv lists v1 settings that must not be silently ignored.
var removedEnv = []struct{ name, hint string }{
	{"LISTEN_ADDR", "configure listeners in the routing config"},
	{"LISTEN_PORT", "configure listeners in the routing config"},
	{"DROP_UID", "the image runs as a non-root user with CAP_NET_BIND_SERVICE"},
	{"DROP_GID", "the image runs as a non-root user with CAP_NET_BIND_SERVICE"},
}

func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

func getIntEnv(env string, defaultValue int) int {
	stringValue, exists := os.LookupEnv(env)
	if !exists {
		return defaultValue
	}
	value, err := strconv.Atoi(stringValue)
	if err != nil {
		fatal("could not parse env", "name", env, "error", err)
	}
	return value
}

func getStringEnv(env string, defaultValue string) string {
	stringValue, exists := os.LookupEnv(env)
	if !exists {
		return defaultValue
	}
	return stringValue
}

func setupLogging(levelName string) error {
	var level slog.Level
	if err := level.UnmarshalText([]byte(levelName)); err != nil {
		return fmt.Errorf("invalid LOG_LEVEL %q: %w", levelName, err)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	return nil
}

func checkRemovedEnv() error {
	for _, env := range removedEnv {
		if _, ok := os.LookupEnv(env.name); ok {
			return fmt.Errorf("%s was removed in v2: %s", env.name, env.hint)
		}
	}
	return nil
}

func reload(path string, mgr *server.Manager) error {
	listeners, err := config.Load(path)
	if err != nil {
		return err
	}
	mgr.Apply(listeners)
	return nil
}

func main() {
	if err := setupLogging(getStringEnv("LOG_LEVEL", "info")); err != nil {
		fatal("invalid config", "error", err)
	}
	if err := checkRemovedEnv(); err != nil {
		fatal("invalid config", "error", err)
	}
	metricsPort := getIntEnv("METRICS_PORT", 9113)
	configPath := getStringEnv("ROUTING_CONFIG_PATH", "/etc/sni-router/routing.yaml")
	listeners, err := config.Load(configPath)
	if err != nil {
		fatal("invalid routing config", "path", configPath, "error", err)
	}
	metrics := monitoring.NewMetrics()
	go metrics.Start(metricsPort)
	mgr := server.NewManager(metrics, getIntEnv("MAX_CONNECTIONS", 0))
	mgr.Apply(listeners)

	ctx, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()
	go mgr.WatchAddresses(ctx, addrCheckInterval)

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := reload(configPath, mgr); err != nil {
				slog.Error("reload failed; keeping previous config", "error", err)
				continue
			}
			slog.Info("reloaded routing config")
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	timeout := time.Duration(getIntEnv("SHUTDOWN_TIMEOUT_SECONDS", 30)) * time.Second
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := mgr.Shutdown(shutdownCtx); err != nil {
		slog.Warn("shutdown", "error", err)
	}
}
