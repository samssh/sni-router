package routing

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
)

type Route struct {
	Domain   string `yaml:"domain"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	UseRegex bool   `yaml:"useRegex"`
	UseProxy bool   `yaml:"useProxy"`
}

type SNIRouter struct {
	nonTlsRoute  *Route
	defaultRoute *Route
	Routes       []Route
}

func NewSNIRouter(allRoutes []Route) *SNIRouter {
	s := &SNIRouter{
		Routes: make([]Route, 0, len(allRoutes)),
	}

	for _, route := range allRoutes {
		if route.Host == "" {
			route.Host = "127.0.0.1"
		}
		switch route.Domain {
		case "non-tls":
			s.nonTlsRoute = &route // or route := route; s.nonTlsRoute = &route
		case "default":
			s.defaultRoute = &route
		default:
			s.Routes = append(s.Routes, route)
		}
	}

	return s
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
			match, _ = regexp.MatchString(route.Domain, sniValue)
		} else {
			match = route.Domain == sniValue
		}
		if match {
			return true, net.JoinHostPort(route.Host, strconv.Itoa(route.Port)), nil
		}
	}
	return s.defaultRoute.UseProxy, net.JoinHostPort(s.defaultRoute.Host, strconv.Itoa(s.defaultRoute.Port)), nil
}
