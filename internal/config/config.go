package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"sni-router/internal/routing"

	"gopkg.in/yaml.v3"
)

type file struct {
	Routes    []routing.Route `yaml:"routes"`
	Listeners []listenerFile  `yaml:"listeners"`
}

type listenerFile struct {
	Addr           string          `yaml:"addr"`
	MaxConnections int             `yaml:"maxConnections"`
	InheritRoutes  *bool           `yaml:"inheritRoutes"`
	Routes         []routing.Route `yaml:"routes"`
}

// Listener is one validated listen address with its merged routes.
type Listener struct {
	Addr           netip.AddrPort
	MaxConnections int
	Router         *routing.SNIRouter
}

// Name is the normalized address used as the listener's identity and metric label.
func (l Listener) Name() string {
	return l.Addr.String()
}

func Load(path string) ([]Listener, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading YAML file: %w", err)
	}
	return Parse(data)
}

func Parse(data []byte) ([]Listener, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("error unmarshalling YAML: %w", err)
	}
	if len(root.Content) > 0 && root.Content[0].Kind == yaml.SequenceNode {
		return nil, errors.New("routing config is a bare list of routes (v1 format); v2 expects a map with routes and listeners")
	}
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("error unmarshalling YAML: %w", err)
	}
	return f.validate()
}

func (f *file) validate() ([]Listener, error) {
	if len(f.Listeners) == 0 {
		return nil, errors.New("at least one listener is required")
	}
	shared, err := routing.Prepare(f.Routes)
	if err != nil {
		return nil, fmt.Errorf("shared routes: %w", err)
	}
	listeners := make([]Listener, 0, len(f.Listeners))
	seen := make(map[netip.AddrPort]bool, len(f.Listeners))
	for i, lf := range f.Listeners {
		addr, err := parseAddr(lf.Addr)
		if err != nil {
			return nil, fmt.Errorf("listener %d: %w", i, err)
		}
		if seen[addr] {
			return nil, fmt.Errorf("duplicate listener %s", addr)
		}
		seen[addr] = true
		if lf.MaxConnections < 0 {
			return nil, fmt.Errorf("listener %s: maxConnections must be >= 0", addr)
		}
		routes := lf.Routes
		if lf.InheritRoutes == nil || *lf.InheritRoutes {
			routes = mergeRoutes(addr.String(), lf.Routes, shared)
		}
		router, err := routing.NewSNIRouter(routes)
		if err != nil {
			return nil, fmt.Errorf("listener %s: %w", addr, err)
		}
		listeners = append(listeners, Listener{Addr: addr, MaxConnections: lf.MaxConnections, Router: router})
	}
	if err := checkWildcards(listeners); err != nil {
		return nil, err
	}
	return listeners, nil
}

func parseAddr(s string) (netip.AddrPort, error) {
	if s == "" {
		return netip.AddrPort{}, errors.New("addr is required")
	}
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("invalid addr %q (want ip:port or [ipv6]:port): %w", s, err)
	}
	if ap.Port() == 0 {
		return netip.AddrPort{}, fmt.Errorf("invalid addr %q: port must be > 0", s)
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), nil
}

// mergeRoutes puts the listener's routes first, then the shared ones.
// A listener route replaces any shared route with the same domain, including default and non-tls.
func mergeRoutes(listener string, own, shared []routing.Route) []routing.Route {
	merged := append([]routing.Route(nil), own...)
	ownDomains := make(map[string]bool, len(own))
	for _, r := range own {
		ownDomains[r.Domain] = true
	}
	for _, r := range shared {
		if ownDomains[r.Domain] {
			slog.Debug("listener route overrides shared route", "listener", listener, "domain", r.Domain)
			continue
		}
		merged = append(merged, r)
	}
	return merged
}

// checkWildcards rejects listeners that a wildcard on the same port already covers.
// 0.0.0.0 covers IPv4; [::] is dual-stack and covers both families.
func checkWildcards(listeners []Listener) error {
	for _, a := range listeners {
		if !a.Addr.Addr().IsUnspecified() {
			continue
		}
		for _, b := range listeners {
			if a.Addr == b.Addr || a.Addr.Port() != b.Addr.Port() {
				continue
			}
			if a.Addr.Addr().Is6() || b.Addr.Addr().Is4() {
				return fmt.Errorf("listener %s overlaps wildcard listener %s", b.Addr, a.Addr)
			}
		}
	}
	return nil
}
