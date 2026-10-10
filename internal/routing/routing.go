package routing

import (
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strconv"
	"time"
)

const DefaultDialTimeoutSeconds = 2

type Route struct {
	Domain       string `yaml:"domain"`
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	UseRegex     bool   `yaml:"useRegex"`
	UseProxy     bool   `yaml:"useProxy"`
	ReverseMatch bool   `yaml:"reverseMatch"`
	DialTimeout  int    `yaml:"dialTimeout"`
	compiled     *regexp.Regexp
	prepared     bool
}

type SNIRouter struct {
	nonTlsRoute  *Route
	defaultRoute *Route
	Routes       []Route
}

func NewSNIRouter(allRoutes []Route) (*SNIRouter, error) {
	s, err := build(allRoutes)
	if err != nil {
		return nil, err
	}
	if s.defaultRoute == nil {
		return nil, fmt.Errorf("missing default route")
	}
	return s, nil
}

// Prepare checks routes, fills defaults, compiles regexes and rejects a duplicate default or
// non-tls, without requiring a default route. NewSNIRouter reuses prepared routes as they are,
// so routes shared by several routers are compiled and warned about once.
func Prepare(routes []Route) ([]Route, error) {
	out := make([]Route, 0, len(routes))
	for _, route := range routes {
		p, err := prepare(route)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if _, err := build(out); err != nil {
		return nil, err
	}
	return out, nil
}

func prepare(route Route) (Route, error) {
	if route.prepared {
		return route, nil
	}
	if route.Domain == "" {
		return Route{}, fmt.Errorf("empty domain")
	}
	if route.Port <= 0 {
		return Route{}, fmt.Errorf("invalid port for domain %q", route.Domain)
	}
	if route.Host == "" {
		route.Host = "127.0.0.1"
	}
	if route.DialTimeout <= 0 {
		route.DialTimeout = DefaultDialTimeoutSeconds
	}
	if route.UseRegex {
		re, err := regexp.Compile(route.Domain)
		if err != nil {
			return Route{}, fmt.Errorf("invalid regex for domain %q: %w", route.Domain, err)
		}
		route.compiled = re
	}
	if route.ReverseMatch && !route.UseRegex && route.Domain != "default" && route.Domain != "non-tls" {
		slog.Warn("reverseMatch without useRegex matches every other name", "domain", route.Domain)
	}
	route.prepared = true
	return route, nil
}

func build(allRoutes []Route) (*SNIRouter, error) {
	s := &SNIRouter{
		Routes: make([]Route, 0, len(allRoutes)),
	}

	for _, route := range allRoutes {
		route, err := prepare(route)
		if err != nil {
			return nil, err
		}
		switch route.Domain {
		case "non-tls":
			if s.nonTlsRoute != nil {
				return nil, fmt.Errorf("duplicate non-tls route")
			}
			s.nonTlsRoute = &route
		case "default":
			if s.defaultRoute != nil {
				return nil, fmt.Errorf("duplicate default route")
			}
			s.defaultRoute = &route
		default:
			s.Routes = append(s.Routes, route)
		}
	}

	return s, nil
}

// Match returns the route a connection takes: the first matching route, the non-tls route for
// non-tls traffic, otherwise the default route.
func (s *SNIRouter) Match(sniValue string, isTls bool) (*Route, error) {
	if !isTls {
		if s.nonTlsRoute == nil {
			return nil, fmt.Errorf("no non-tls route for non-tls connection")
		}
		return s.nonTlsRoute, nil
	}
	for i := range s.Routes {
		route := &s.Routes[i]
		var match bool
		if route.UseRegex {
			match = route.compiled != nil && route.compiled.MatchString(sniValue)
		} else {
			match = route.Domain == sniValue
		}
		if route.ReverseMatch {
			match = !match
		}
		if match {
			return route, nil
		}
	}
	return s.defaultRoute, nil
}

func (s *SNIRouter) Route(sniValue string, isTls bool) (bool, string, time.Duration, error) {
	route, err := s.Match(sniValue, isTls)
	if err != nil {
		return false, "", 0, err
	}
	useProxy, addr, timeout := route.Destination()
	return useProxy, addr, timeout, nil
}

// Destination returns whether to send a PROXY header, the backend address and the dial timeout.
func (r *Route) Destination() (bool, string, time.Duration) {
	return r.UseProxy, net.JoinHostPort(r.Host, strconv.Itoa(r.Port)), time.Duration(r.DialTimeout) * time.Second
}
