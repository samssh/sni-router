package server

import (
	"net/netip"
	"strings"
	"testing"
)

func TestListenFreebindUnassignedAddress(t *testing.T) {
	for _, addr := range []string{"192.0.2.1:0", "[2001:db8::1]:0"} {
		t.Run(addr, func(t *testing.T) {
			ln, err := listen(netip.MustParseAddrPort(addr))
			if err != nil && strings.Contains(err.Error(), "address family not supported") {
				t.Skip("IPv6 disabled")
			}
			if err != nil {
				t.Fatalf("bind to unassigned address with FREEBIND: %v", err)
			}
			_ = ln.Close()
		})
	}
}
