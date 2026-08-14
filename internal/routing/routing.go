package routing

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"time"
)

const DefaultDialTimeoutSeconds = 10

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
	s := &SNIRouter{
		Routes: make([]Route, 0, len(allRoutes)),
	}

	for _, route := range allRoutes {
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
