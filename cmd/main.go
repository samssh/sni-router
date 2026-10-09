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

func getIntEnv(env string, defaultValue int) (int, error) {
	stringValue, exists := os.LookupEnv(env)
	if !exists {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(stringValue)
	if err != nil {
		return 0, fmt.Errorf("could not parse %s: %w", env, err)
	}
	return value, nil
}

func getStringEnv(env string, defaultValue string) string {
	stringValue, exists := os.LookupEnv(env)
	if !exists {
		return defaultValue
	}
	return stringValue
}

type settings struct {
	configPath      string
	metricsPort     int
	maxConns        int
	shutdownTimeout time.Duration
}

// loadSettings reads every env var up front, so a bad value fails at startup instead of on SIGTERM.
func loadSettings() (settings, error) {
	s := settings{configPath: getStringEnv("ROUTING_CONFIG_PATH", "/etc/sni-router/routing.yaml")}
	var err error
	if s.metricsPort, err = getIntEnv("METRICS_PORT", 9113); err != nil {
		return settings{}, err
	}
	if s.maxConns, err = getIntEnv("MAX_CONNECTIONS", 0); err != nil {
		return settings{}, err
	}
	seconds, err := getIntEnv("SHUTDOWN_TIMEOUT_SECONDS", 30)
	if err != nil {
		return settings{}, err
	}
	s.shutdownTimeout = time.Duration(seconds) * time.Second
	return s, nil
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
		return fmt.Errorf("keeping previous config: %w", err)
	}
	if err := mgr.Apply(listeners); err != nil {
		return fmt.Errorf("config applied, but %w", err)
	}
	return nil
}

func main() {
	if err := setupLogging(getStringEnv("LOG_LEVEL", "info")); err != nil {
		fatal("invalid config", "error", err)
	}
	if err := checkRemovedEnv(); err != nil {
		fatal("invalid config", "error", err)
	}
	cfg, err := loadSettings()
	if err != nil {
		fatal("invalid config", "error", err)
	}
	listeners, err := config.Load(cfg.configPath)
	if err != nil {
		fatal("invalid routing config", "path", cfg.configPath, "error", err)
	}
	metrics := monitoring.NewMetrics()
	go metrics.Start(cfg.metricsPort)
	mgr := server.NewManager(metrics, cfg.maxConns)
	if err := mgr.Apply(listeners); err != nil {
		fatal("startup failed", "error", err)
	}

	ctx, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()
	go mgr.WatchAddresses(ctx, addrCheckInterval)

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := reload(cfg.configPath, mgr); err != nil {
				slog.Error("reload failed", "error", err)
				continue
			}
			slog.Info("reloaded routing config")
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.shutdownTimeout)
	defer cancel()
	if err := mgr.Shutdown(shutdownCtx); err != nil {
		slog.Warn("shutdown", "error", err)
	}
}
