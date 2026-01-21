package routing

import (
	"log"
	"net"
	"strconv"
	"strings"
)

type SNIRouter struct {
	baseDomain  string
	basePort    int
	defaultPort int
}

func NewSNIRouter(baseDomain string, basePort int, defaultPort int) *SNIRouter {
	return &SNIRouter{
		baseDomain:  baseDomain,
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
	if strings.HasSuffix(sniValue, s.baseDomain) {
		number, err := strconv.Atoi(sniValue[:len(sniValue)-len(s.baseDomain)])
		if err != nil {
			log.Println("SNI parse failed:", err)
		}
		port = s.basePort + number
		useProxy = false
	}
	return useProxy, net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}
