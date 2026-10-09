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

// ValidateRoutes runs the same checks as NewSNIRouter but does not require a default route.
func ValidateRoutes(routes []Route) error {
	_, err := build(routes)
	return err
}

func build(allRoutes []Route) (*SNIRouter, error) {
	s := &SNIRouter{
		Routes: make([]Route, 0, len(allRoutes)),
	}

	for _, route := range allRoutes {
		if route.Domain == "" {
			return nil, fmt.Errorf("empty domain")
		}
		if route.Port <= 0 {
			return nil, fmt.Errorf("invalid port for domain %q", route.Domain)
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
				return nil, fmt.Errorf("invalid regex for domain %q: %w", route.Domain, err)
			}
			route.compiled = re
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
			if route.ReverseMatch && !route.UseRegex {
				slog.Warn("reverseMatch without useRegex matches every other name", "domain", route.Domain)
			}
			s.Routes = append(s.Routes, route)
		}
	}

	return s, nil
}

func (s *SNIRouter) Route(sniValue string, isTls bool) (bool, string, time.Duration, error) {
	if !isTls {
		if s.nonTlsRoute == nil {
			return false, "", 0, fmt.Errorf("no non-tls route for non-tls connection")
		}
		return destination(s.nonTlsRoute)
	}
	for _, route := range s.Routes {
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
			return destination(&route)
		}
	}
	return destination(s.defaultRoute)
}

func destination(route *Route) (bool, string, time.Duration, error) {
	return route.UseProxy, net.JoinHostPort(route.Host, strconv.Itoa(route.Port)), time.Duration(route.DialTimeout) * time.Second, nil
}
