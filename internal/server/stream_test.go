package server

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestCopyStreamsBidirectional(t *testing.T) {
	client, inbound := net.Pipe()
	backend, outbound := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = inbound.Close()
		_ = backend.Close()
		_ = outbound.Close()
	})

	metrics := newTestMetrics()
	ic := newInboundConnection(inbound, metrics)
	oc := newOutboundConnection(outbound, "prom.example.com", metrics)

	done := make(chan struct{})
	go func() {
		CopyStreamsBidirectional(ic, oc)
		close(done)
	}()

	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(backend, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "ping" {
		t.Fatalf("backend got %q, want ping", got)
	}

	if _, err := backend.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "pong" {
		t.Fatalf("client got %q, want pong", got)
	}

	_ = client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("copy did not finish after client close")
	}
}

func TestCloseIdempotent(t *testing.T) {
	client, inbound := net.Pipe()
	backend, outbound := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = backend.Close()
	})

	metrics := newTestMetrics()
	ic := newInboundConnection(inbound, metrics)
	oc := newOutboundConnection(outbound, "prom.example.com", metrics)

	ic.Close()
	ic.Close()
	oc.Close()
	oc.Close()
}
