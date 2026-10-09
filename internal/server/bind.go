package server

import (
	"context"
	"net"
	"net/netip"
)

// listen binds addr. 0.0.0.0 only covers IPv4 and [::] is dual-stack, matching the
// wildcard rules in config. On Linux the socket uses FREEBIND, so the IP does not
// have to be on an interface yet (floating IPs, tentative IPv6 during DAD).
func listen(addr netip.AddrPort) (net.Listener, error) {
	network := "tcp4"
	if addr.Addr().Is6() {
		network = "tcp6"
		if addr.Addr().IsUnspecified() {
			network = "tcp"
		}
	}
	lc := net.ListenConfig{Control: freebind}
	return lc.Listen(context.Background(), network, addr.String())
}
