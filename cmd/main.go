package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sni-router/internal/config"
	"sni-router/internal/monitoring"
	"sni-router/internal/routing"
	"sni-router/internal/server"
	"strconv"
	"syscall"
	"time"
)

func getIntEnv(env string, defaultValue int) int {
	stringValue, exists := os.LookupEnv(env)
	if !exists {
		return defaultValue
	}
	value, err := strconv.Atoi(stringValue)
	if err != nil {
		log.Fatalf("could not pars %s: %s", env, err.Error())
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

func main() {
	listenPort := getIntEnv("LISTEN_PORT", 443)
	metricsPort := getIntEnv("METRICS_PORT", 9113)
	routes, err := config.LoadRoutingConfig(getStringEnv("ROUTING_CONFIG_PATH", "/etc/sni-router/routing.yaml"))
	if err != nil {
		log.Fatal(err)
	}
	router, err := routing.NewSNIRouter(routes)
	if err != nil {
		log.Fatal(err)
	}
	metrics := monitoring.NewMetrics()
	go metrics.Start(metricsPort)
	listener := server.NewListener(router, metrics, listenPort).WithMaxConns(getIntEnv("MAX_CONNECTIONS", 0))
	go listener.Listen()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	timeout := time.Duration(getIntEnv("SHUTDOWN_TIMEOUT_SECONDS", 30)) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := listener.Shutdown(ctx); err != nil {
		log.Println("shutdown:", err)
	}
}
