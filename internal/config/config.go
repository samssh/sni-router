package config

import (
	"log"
	"os"
	"sni-router/internal/routing"

	"gopkg.in/yaml.v3"
)

func LoadRoutingConfig(configPath string) []routing.Route {
	file, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalf("Error reading YAML file: %v", err)
	}
	var routes []routing.Route
	err = yaml.Unmarshal(file, &routes)
	if err != nil {
		log.Fatalf("Error unmarshalling YAML: %v", err)
	}
	return routes
}
