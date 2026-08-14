package routing

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
)

type Route struct {
	Domain       string `yaml:"domain"`
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	UseRegex     bool   `yaml:"useRegex"`
	UseProxy     bool   `yaml:"useProxy"`
	ReverseMatch bool   `yaml:"reverseMatch"`
	compiled     *regexp.Regexp
}

type SNIRouter struct {
	nonTlsRoute  *Route
	defaultRoute *Route
	Routes       []Route
}

func NewSNIRouter(allRoutes []Route) (*SNIRouter, error) {
	s := &SNIRouter{
		Routes: make([]Route, 0, len(allRoutes)),
	}

	for _, route := range allRoutes {
		if route.Host == "" {
			route.Host = "127.0.0.1"
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
			s.nonTlsRoute = &route
		case "default":
			s.defaultRoute = &route
		default:
			s.Routes = append(s.Routes, route)
		}
	}

	if s.defaultRoute == nil {
		return nil, fmt.Errorf("missing default route")
	}

	return s, nil
}

func (s *SNIRouter) Route(sniValue string, isTls bool) (bool, string, error) {
	if !isTls {
		if s.nonTlsRoute == nil {
			return false, "", fmt.Errorf("no non-tls route for non-tls connection")
		}
		return s.nonTlsRoute.UseProxy, net.JoinHostPort(s.nonTlsRoute.Host, strconv.Itoa(s.nonTlsRoute.Port)), nil
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
			return route.UseProxy, net.JoinHostPort(route.Host, strconv.Itoa(route.Port)), nil
		}
	}
	return s.defaultRoute.UseProxy, net.JoinHostPort(s.defaultRoute.Host, strconv.Itoa(s.defaultRoute.Port)), nil
}
