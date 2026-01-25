package main

import (
	"log"
	"os"
	"sni-router/internal/monitoring"
	"sni-router/internal/routing"
	"sni-router/internal/server"
	"strconv"
	"strings"
)

func parsIntEnv(env string, defaultValue int) int {
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

func main() {
	listenPort := parsIntEnv("LISTEN_PORT", 443)
	routingBaseDomains := strings.Split(os.Getenv("ROUTING_BASE_DOMAINS"), ",")
	routingBasePort := parsIntEnv("ROUTING_BASE_PORT", -1)
	routingDefaultPort := parsIntEnv("ROUTING_DEFAULT_PORT", 444)
	metricsPort := parsIntEnv("METRICS_PORT", 9113)
	router := routing.NewSNIRouter(routingBaseDomains, routingBasePort, routingDefaultPort)
	metrics := monitoring.NewMetrics()
	go metrics.Start(metricsPort)
	listener := server.NewListener(router, metrics, listenPort)
	listener.Listen()
}
