package server

import (
	"crypto/tls"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"sni-router/internal/config"
	"sni-router/internal/monitoring"
	"sni-router/internal/routing"
)

func newTestMetrics() *monitoring.Metrics {
	return monitoring.NewMetricsWithRegisterer(prometheus.NewRegistry())
}

func newTestListenerMetrics() *monitoring.ListenerMetrics {
	return newTestMetrics().Listener("test")
}

func newTestManager(maxConns int) (*Manager, *prometheus.Registry) {
	reg := prometheus.NewRegistry()
	m := NewManager(monitoring.NewMetricsWithRegisterer(reg), maxConns)
	m.retryInitial = 10 * time.Millisecond
	m.retryMax = 50 * time.Millisecond
	return m, reg
}

func testSpec(t *testing.T, addr netip.AddrPort, maxConns int, routes []routing.Route) config.Listener {
	t.Helper()
	router, err := routing.NewSNIRouter(routes)
	if err != nil {
		t.Fatal(err)
	}
	return config.Listener{Addr: addr, MaxConnections: maxConns, Router: router}
}

func newTestListener(t *testing.T, routes []routing.Route) *Listener {
	t.Helper()
	m, _ := newTestManager(0)
	return m.newListener(testSpec(t, netip.MustParseAddrPort("127.0.0.1:0"), 0, routes))
}

// freeAddr returns a loopback address with a port that was free a moment ago.
func freeAddr(t *testing.T, ip string) netip.AddrPort {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Skipf("cannot listen on %s: %v", ip, err)
	}
	addr := ln.Addr().(*net.TCPAddr).AddrPort()
	_ = ln.Close()
	return netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
}

// listenerGauge returns the value of a listener gauge and whether the series exists.
func listenerGauge(t *testing.T, reg *prometheus.Registry, name, listener string) (float64, bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if hasLabel(metric.GetLabel(), "listener", listener) {
				return metric.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

func hasLabel(labels []*dto.LabelPair, name, value string) bool {
	for _, l := range labels {
		if l.GetName() == name && l.GetValue() == value {
			return true
		}
	}
	return false
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func clientHelloWithSNI(t *testing.T, serverName string) []byte {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	helloCh := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 16*1024)
		n, err := server.Read(buf)
		if err != nil && n == 0 {
			helloCh <- nil
			return
		}
		helloCh <- append([]byte(nil), buf[:n]...)
		_ = server.Close()
	}()

	tlsConn := tls.Client(client, &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: true,
	})
	_ = tlsConn.Handshake()
	_ = tlsConn.Close()

	hello := <-helloCh
	if len(hello) == 0 {
		t.Fatal("did not capture ClientHello")
	}
	return hello
}

func backendHostPort(t *testing.T, addr net.Addr) (string, int) {
	t.Helper()
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected addr type %T", addr)
	}
	return tcpAddr.IP.String(), tcpAddr.Port
}
