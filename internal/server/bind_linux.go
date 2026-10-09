package server

import (
	"log/slog"
	"syscall"

	"golang.org/x/sys/unix"
)

func freebind(network, address string, c syscall.RawConn) error {
	return c.Control(func(fd uintptr) {
		var err error
		if network == "tcp4" {
			err = unix.SetsockoptInt(int(fd), unix.SOL_IP, unix.IP_FREEBIND, 1)
		} else {
			err = unix.SetsockoptInt(int(fd), unix.SOL_IPV6, unix.IPV6_FREEBIND, 1)
			if err != nil {
				// Kernels before 4.15 have no IPV6_FREEBIND; IP_FREEBIND covers IPv6 sockets too.
				err = unix.SetsockoptInt(int(fd), unix.SOL_IP, unix.IP_FREEBIND, 1)
			}
		}
		if err != nil {
			slog.Warn("set FREEBIND failed; bind needs the address on an interface", "addr", address, "error", err)
		}
	})
}
