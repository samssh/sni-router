package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"
	"github.com/prometheus/client_golang/prometheus"
	"sni-router/internal/config"
	"sni-router/internal/routing"
)

func waitUp(t *testing.T, reg *prometheus.Registry, addr netip.AddrPort) {
	t.Helper()
	eventually(t, "listener_up for "+addr.String(), func() bool {
		v, ok := listenerGauge(t, reg, "sni_router_listener_up", addr.String())
		return ok && v == 1
	})
}

func shutdownOnCleanup(t *testing.T, m *Manager) {
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
}

func getBody(t *testing.T, addr netip.AddrPort, serverName string) string {
	t.Helper()
	resp, err := routerHTTPClient(addr.String(), serverName).Get("https://" + serverName + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// holdBackend accepts TCP connections and hands them to the test without answering.
func holdBackend(t *testing.T) (routing.Route, <-chan net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan net.Conn, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
			accepted <- c
		}
	}()
	host, port := backendHostPort(t, ln.Addr())
	return routing.Route{Domain: "default", Host: host, Port: port}, accepted
}

// openHeld dials the router, sends a ClientHello, and waits until the backend accepted it.
func openHeld(t *testing.T, addr netip.AddrPort, accepted <-chan net.Conn) (client, backend net.Conn) {
	t.Helper()
	client, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Write(clientHelloWithSNI(t, "prom.example.com")); err != nil {
		t.Fatal(err)
	}
	select {
	case backend = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("backend did not accept connection")
	}
	return client, backend
}

func expectRejected(t *testing.T, addr netip.AddrPort) {
	t.Helper()
	c, err := net.Dial("tcp", addr.String())
	if err != nil {
		return
	}
	defer c.Close()
	if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected connection to be rejected")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("connection was accepted, expected rejection")
	}
}

func TestManagerServesEachListenerWithItsRoutes(t *testing.T) {
	a := startTLSBackend(t, "a")
	b := startTLSBackend(t, "b")
	aHost, aPort := backendRoute(t, a)
	bHost, bPort := backendRoute(t, b)

	m, reg := newTestManager(0)
	shutdownOnCleanup(t, m)
	addrA := freeAddr(t, "127.0.0.1")
	addrB := freeAddr(t, "127.0.0.1")
	m.Apply([]config.Listener{
		testSpec(t, addrA, 0, []routing.Route{{Domain: "default", Host: aHost, Port: aPort}}),
		testSpec(t, addrB, 0, []routing.Route{{Domain: "default", Host: bHost, Port: bPort}}),
	})
	waitUp(t, reg, addrA)
	waitUp(t, reg, addrB)

	if got := getBody(t, addrA, "prom.example.com"); got != "a" {
		t.Fatalf("listener A body = %q, want a", got)
	}
	if got := getBody(t, addrB, "prom.example.com"); got != "b" {
		t.Fatalf("listener B body = %q, want b", got)
	}
}

func TestManagerIPv6Listener(t *testing.T) {
	backend := startTLSBackend(t, "v6")
	host, port := backendRoute(t, backend)
	m, reg := newTestManager(0)
	shutdownOnCleanup(t, m)
	addr := freeAddr(t, "::1")
	m.Apply([]config.Listener{testSpec(t, addr, 0, []routing.Route{{Domain: "default", Host: host, Port: port}})})
	waitUp(t, reg, addr)
	if got := getBody(t, addr, "prom.example.com"); got != "v6" {
		t.Fatalf("body = %q, want v6", got)
	}
}

func TestManagerBindFailureIsIsolatedAndRetried(t *testing.T) {
	backend := startTLSBackend(t, "ok")
	host, port := backendRoute(t, backend)
	routes := []routing.Route{{Domain: "default", Host: host, Port: port}}

	busy := freeAddr(t, "127.0.0.1")
	occupier, err := net.Listen("tcp", busy.String())
	if err != nil {
		t.Fatal(err)
	}
	healthy := freeAddr(t, "127.0.0.1")

	m, reg := newTestManager(0)
	shutdownOnCleanup(t, m)
	m.Apply([]config.Listener{testSpec(t, busy, 0, routes), testSpec(t, healthy, 0, routes)})
	waitUp(t, reg, healthy)
	if got := getBody(t, healthy, "prom.example.com"); got != "ok" {
		t.Fatalf("healthy listener body = %q", got)
	}
	if v, ok := listenerGauge(t, reg, "sni_router_listener_up", busy.String()); !ok || v != 0 {
		t.Fatalf("busy listener_up = %v (present %v), want 0", v, ok)
	}

	_ = occupier.Close()
	waitUp(t, reg, busy)
	if got := getBody(t, busy, "prom.example.com"); got != "ok" {
		t.Fatalf("recovered listener body = %q", got)
	}
}

func TestManagerReloadRemovesListenerButKeepsConnections(t *testing.T) {
	route, accepted := holdBackend(t)
	routes := []routing.Route{route}
	m, reg := newTestManager(0)
	shutdownOnCleanup(t, m)
	oldAddr := freeAddr(t, "127.0.0.1")
	newAddr := freeAddr(t, "127.0.0.1")
	m.Apply([]config.Listener{testSpec(t, oldAddr, 0, routes)})
	waitUp(t, reg, oldAddr)

	client, backend := openHeld(t, oldAddr, accepted)
	// Drain the forwarded ClientHello so later reads only see new bytes.
	if err := backend.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, backend)

	m.Apply([]config.Listener{testSpec(t, newAddr, 0, routes)})
	waitUp(t, reg, newAddr)
	expectRejected(t, oldAddr)

	if _, ok := listenerGauge(t, reg, "sni_router_listener_up", oldAddr.String()); ok {
		t.Fatal("listener_up for the removed listener should be deleted")
	}
	if v, ok := listenerGauge(t, reg, "sni_router_inbound_connections_open", oldAddr.String()); !ok || v != 1 {
		t.Fatalf("inbound open for removed listener = %v (present %v), want 1 while its connection is open", v, ok)
	}

	if _, err := client.Write([]byte("still here")); err != nil {
		t.Fatal(err)
	}
	if err := backend.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("still here"))
	if _, err := io.ReadFull(backend, buf); err != nil {
		t.Fatalf("open connection broke after its listener was removed: %v", err)
	}

	_ = client.Close()
	_ = backend.Close()
	eventually(t, "removed listener series to be deleted", func() bool {
		_, ok := listenerGauge(t, reg, "sni_router_inbound_connections_open", oldAddr.String())
		return !ok
	})
}

func TestManagerReloadLowersMaxConnectionsImmediately(t *testing.T) {
	route, accepted := holdBackend(t)
	routes := []routing.Route{route}
	m, reg := newTestManager(0)
	shutdownOnCleanup(t, m)
	addr := freeAddr(t, "127.0.0.1")
	m.Apply([]config.Listener{testSpec(t, addr, 3, routes)})
	waitUp(t, reg, addr)

	openHeld(t, addr, accepted)
	_, held := openHeld(t, addr, accepted)

	m.Apply([]config.Listener{testSpec(t, addr, 1, routes)})
	expectRejected(t, addr)

	// Still over the new cap with one connection open.
	_ = held.Close()
	expectRejected(t, addr)
}

func TestManagerReloadChangesMaxConnections(t *testing.T) {
	route, accepted := holdBackend(t)
	routes := []routing.Route{route}
	m, reg := newTestManager(0)
	shutdownOnCleanup(t, m)
	addr := freeAddr(t, "127.0.0.1")
	m.Apply([]config.Listener{testSpec(t, addr, 1, routes)})
	waitUp(t, reg, addr)

	openHeld(t, addr, accepted)
	expectRejected(t, addr)

	m.Apply([]config.Listener{testSpec(t, addr, 2, routes)})
	openHeld(t, addr, accepted)
}

func TestManagerGlobalLimitSpansListeners(t *testing.T) {
	route, accepted := holdBackend(t)
	routes := []routing.Route{route}
	m, reg := newTestManager(1)
	shutdownOnCleanup(t, m)
	a := freeAddr(t, "127.0.0.1")
	b := freeAddr(t, "127.0.0.1")
	m.Apply([]config.Listener{testSpec(t, a, 0, routes), testSpec(t, b, 0, routes)})
	waitUp(t, reg, a)
	waitUp(t, reg, b)

	openHeld(t, a, accepted)
	expectRejected(t, b)
}

func TestManagerAddrPresence(t *testing.T) {
	routes := []routing.Route{{Domain: "default", Host: "127.0.0.1", Port: 9}}
	floating := netip.MustParseAddr("192.0.2.1")
	local := []net.Addr{&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)}}
	setLocal := make(chan []net.Addr, 1)

	m, reg := newTestManager(0)
	shutdownOnCleanup(t, m)
	m.interfaceAddrs = func() ([]net.Addr, error) {
		select {
		case local = <-setLocal:
		default:
		}
		return local, nil
	}
	loopback := freeAddr(t, "127.0.0.1")
	wildcard := freeAddr(t, "0.0.0.0")
	missing := netip.AddrPortFrom(floating, loopback.Port())
	m.Apply([]config.Listener{
		testSpec(t, loopback, 0, routes),
		testSpec(t, wildcard, 0, routes),
		testSpec(t, missing, 0, routes),
	})

	present := func(addr netip.AddrPort) float64 {
		v, ok := listenerGauge(t, reg, "sni_router_listener_addr_present", addr.String())
		if !ok {
			t.Fatalf("no listener_addr_present series for %s", addr)
		}
		return v
	}
	if present(loopback) != 1 || present(wildcard) != 1 {
		t.Fatal("loopback and wildcard listeners should report their address as present")
	}
	if present(missing) != 0 {
		t.Fatal("listener on an unassigned address should report 0")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.WatchAddresses(ctx, 10*time.Millisecond)
	setLocal <- append(local, &net.IPNet{IP: floating.AsSlice(), Mask: net.CIDRMask(32, 32)})
	eventually(t, "floating address to be reported present", func() bool {
		return present(missing) == 1
	})
}

func TestProxyHeaderCarriesListenerAddress(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1"} {
		t.Run(ip, func(t *testing.T) {
			backendLn, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = backendLn.Close() })
			headers := make(chan *proxyproto.Header, 1)
			go func() {
				c, err := backendLn.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
				h, err := proxyproto.Read(bufio.NewReader(c))
				if err != nil {
					headers <- nil
					return
				}
				headers <- h
			}()
			host, port := backendHostPort(t, backendLn.Addr())

			m, reg := newTestManager(0)
			shutdownOnCleanup(t, m)
			addr := freeAddr(t, ip)
			m.Apply([]config.Listener{testSpec(t, addr, 0, []routing.Route{
				{Domain: "default", Host: host, Port: port, UseProxy: true},
			})})
			waitUp(t, reg, addr)

			client, err := net.Dial("tcp", addr.String())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if _, err := client.Write(clientHelloWithSNI(t, "prom.example.com")); err != nil {
				t.Fatal(err)
			}

			select {
			case h := <-headers:
				if h == nil {
					t.Fatal("backend did not receive a PROXY header")
				}
				dst, ok := h.DestinationAddr.(*net.TCPAddr)
				if !ok {
					t.Fatalf("destination type %T", h.DestinationAddr)
				}
				if got := netip.AddrPortFrom(dst.AddrPort().Addr().Unmap(), dst.AddrPort().Port()); got != addr {
					t.Fatalf("PROXY destination = %s, want listener %s", got, addr)
				}
				if src := h.SourceAddr.String(); src != client.LocalAddr().String() {
					t.Fatalf("PROXY source = %s, want client %s", src, client.LocalAddr())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for PROXY header")
			}
		})
	}
}

func bindError(errno syscall.Errno) error {
	return &net.OpError{Op: "listen", Net: "tcp4", Err: os.NewSyscallError("bind", errno)}
}

// stubListen makes binds to the given addresses fail with errno until fixed is called.
type stubListen struct {
	mu    sync.Mutex
	errs  map[netip.AddrPort]error
	calls map[netip.AddrPort]int
}

func newStubListen(m *Manager, errs map[netip.AddrPort]error) *stubListen {
	s := &stubListen{errs: errs, calls: make(map[netip.AddrPort]int)}
	m.listen = func(addr netip.AddrPort) (net.Listener, error) {
		s.mu.Lock()
		s.calls[addr]++
		err := s.errs[addr]
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return listen(addr)
	}
	return s
}

func (s *stubListen) set(addr netip.AddrPort, err error) {
	s.mu.Lock()
	s.errs[addr] = err
	s.mu.Unlock()
}

func (s *stubListen) callCount(addr netip.AddrPort) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[addr]
}

func TestManagerFailsWhenEveryListenerFailsPermanently(t *testing.T) {
	routes := []routing.Route{{Domain: "default", Host: "127.0.0.1", Port: 9}}
	a := freeAddr(t, "127.0.0.1")
	b := freeAddr(t, "127.0.0.1")
	m, reg := newTestManager(0)
	shutdownOnCleanup(t, m)
	newStubListen(m, map[netip.AddrPort]error{a: bindError(syscall.EACCES), b: bindError(syscall.EAFNOSUPPORT)})

	err := m.Apply([]config.Listener{testSpec(t, a, 0, routes), testSpec(t, b, 0, routes)})
	if err == nil || !errors.Is(err, syscall.EACCES) || !errors.Is(err, syscall.EAFNOSUPPORT) {
		t.Fatalf("err = %v, want both permanent bind errors", err)
	}
	if v, ok := listenerGauge(t, reg, "sni_router_listener_up", a.String()); !ok || v != 0 {
		t.Fatalf("listener_up = %v (present %v), want 0", v, ok)
	}
}

func TestManagerPermanentBindErrorStopsRetryingUntilReload(t *testing.T) {
	routes := []routing.Route{{Domain: "default", Host: "127.0.0.1", Port: 9}}
	broken := freeAddr(t, "127.0.0.1")
	healthy := freeAddr(t, "127.0.0.1")
	m, reg := newTestManager(0)
	shutdownOnCleanup(t, m)
	stub := newStubListen(m, map[netip.AddrPort]error{broken: bindError(syscall.EACCES)})
	specs := []config.Listener{testSpec(t, broken, 0, routes), testSpec(t, healthy, 0, routes)}

	if err := m.Apply(specs); err != nil {
		t.Fatalf("one healthy listener should be enough: %v", err)
	}
	waitUp(t, reg, healthy)
	time.Sleep(5 * m.retryMax)
	if n := stub.callCount(broken); n != 1 {
		t.Fatalf("permanent bind error was retried: %d bind calls", n)
	}

	stub.set(broken, nil)
	if err := m.Apply(specs); err != nil {
		t.Fatal(err)
	}
	waitUp(t, reg, broken)
}

func TestManagerTransientBindErrorTurningPermanentStopsRetrying(t *testing.T) {
	routes := []routing.Route{{Domain: "default", Host: "127.0.0.1", Port: 9}}
	addr := freeAddr(t, "127.0.0.1")
	m, _ := newTestManager(0)
	shutdownOnCleanup(t, m)
	stub := newStubListen(m, map[netip.AddrPort]error{addr: bindError(syscall.EADDRINUSE)})
	if err := m.Apply([]config.Listener{testSpec(t, addr, 0, routes)}); err != nil {
		t.Fatalf("a retrying listener counts as healthy: %v", err)
	}
	eventually(t, "a few retries", func() bool { return stub.callCount(addr) >= 3 })

	stub.set(addr, bindError(syscall.EACCES))
	eventually(t, "the listener to be marked failed", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.listeners[addr.String()].isFailed()
	})
	n := stub.callCount(addr)
	time.Sleep(5 * m.retryMax)
	if got := stub.callCount(addr); got != n {
		t.Fatalf("kept retrying after a permanent error: %d -> %d bind calls", n, got)
	}
}

func TestManagerShutdownMarksListenersDown(t *testing.T) {
	route, accepted := holdBackend(t)
	m, reg := newTestManager(0)
	addr := freeAddr(t, "127.0.0.1")
	m.Apply([]config.Listener{testSpec(t, addr, 0, []routing.Route{route})})
	waitUp(t, reg, addr)
	client, backend := openHeld(t, addr, accepted)

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- m.Shutdown(ctx)
	}()
	eventually(t, "listener_up to drop to 0 while draining", func() bool {
		v, ok := listenerGauge(t, reg, "sni_router_listener_up", addr.String())
		return ok && v == 0
	})
	_ = client.Close()
	_ = backend.Close()
	if err := <-done; err != nil {
		t.Fatalf("drain should finish once the connection closes: %v", err)
	}
}

func TestManagerServesConnectionAcceptedBeforeRemoval(t *testing.T) {
	routes := []routing.Route{{Domain: "default", Host: "127.0.0.1", Port: 9}}
	m, reg := newTestManager(0)
	shutdownOnCleanup(t, m)
	removed := freeAddr(t, "127.0.0.1")
	kept := freeAddr(t, "127.0.0.1")
	m.Apply([]config.Listener{testSpec(t, removed, 0, routes)})
	waitUp(t, reg, removed)
	m.Apply([]config.Listener{testSpec(t, kept, 0, routes)})

	// Simulates Accept returning just before the removal closed the socket.
	client, server := net.Pipe()
	defer client.Close()
	if !m.track(removed.String(), server) {
		t.Fatal("connection accepted before removal should still be served")
	}
	m.metrics.Listener(removed.String()).ObserveOpenInboundConnection()
	if _, ok := listenerGauge(t, reg, "sni_router_inbound_connections_open", removed.String()); !ok {
		t.Fatal("removed listener series should exist while its connection is open")
	}
	m.untrack(removed.String(), server)
	eventually(t, "removed listener series to be deleted", func() bool {
		_, ok := listenerGauge(t, reg, "sni_router_inbound_connections_open", removed.String())
		return !ok
	})
}

func TestManagerUnknownAddrStateIsNotReportedMissing(t *testing.T) {
	m, reg := newTestManager(0)
	shutdownOnCleanup(t, m)
	m.interfaceAddrs = func() ([]net.Addr, error) { return nil, errors.New("netlink unavailable") }
	addr := freeAddr(t, "127.0.0.1")
	m.Apply([]config.Listener{testSpec(t, addr, 0, []routing.Route{{Domain: "default", Host: "127.0.0.1", Port: 9}})})
	waitUp(t, reg, addr)
	if v, ok := listenerGauge(t, reg, "sni_router_listener_addr_present", addr.String()); ok {
		t.Fatalf("listener_addr_present = %v, want no series while the state is unknown", v)
	}
}
