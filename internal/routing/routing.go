package routing

import (
	"log"
	"net"
	"strconv"
	"strings"
)

type SNIRouter struct {
	baseDomains []string
	basePort    int
	defaultPort int
}

func NewSNIRouter(baseDomains []string, basePort int, defaultPort int) *SNIRouter {
	return &SNIRouter{
		baseDomains: baseDomains,
		basePort:    basePort,
		defaultPort: defaultPort,
	}
}

func (s *SNIRouter) Route(sniValue string) (bool, string) {
	var port = s.defaultPort
	var useProxy = true
	if sniValue == "shadowsocks" {
		useProxy = false
		port = 8661
	}
	for _, baseDomain := range s.baseDomains {
		if strings.HasSuffix(sniValue, baseDomain) {
			number, err := strconv.Atoi(sniValue[:len(sniValue)-len(baseDomain)])
			if err != nil {
				log.Println("SNI parse failed:", err)
			}
			port = s.basePort + number
			useProxy = false
			break
		}
	}
	if !strings.HasSuffix(sniValue, "samssh.ir") {
		port = s.basePort + 7
		useProxy = false
	}
	return useProxy, net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}
