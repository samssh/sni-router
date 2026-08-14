package config

import (
	"fmt"
	"os"
	"sni-router/internal/routing"

	"gopkg.in/yaml.v3"
)

func LoadRoutingConfig(configPath string) ([]routing.Route, error) {
	file, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("error reading YAML file: %w", err)
	}
	var routes []routing.Route
	err = yaml.Unmarshal(file, &routes)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling YAML: %w", err)
	}
	return routes, nil
}
