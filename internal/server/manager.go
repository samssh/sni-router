package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sni-router/internal/config"
	"sni-router/internal/monitoring"
	"sync"
	"time"
)

// Manager owns every listener and reconciles them against the config on startup and reload.
type Manager struct {
	metrics *monitoring.Metrics
	global  *limiter

	mu        sync.Mutex // guards listeners; the accept path never takes it
	listeners map[string]*Listener

	// seriesRefs counts, per listener name, the bind/serve loops still running and the open
	// connections. A name's connection series are deleted when it drops to zero, so a removed
	// listener keeps its series until its last connection closes.
	seriesMu   sync.Mutex
	seriesRefs map[string]int

	connsMu  sync.Mutex // guards closed, conns and inflight.Add
	closed   bool
	conns    map[net.Conn]struct{}
	inflight sync.WaitGroup

	retryInitial   time.Duration
	retryMax       time.Duration
	listen         func(netip.AddrPort) (net.Listener, error)
	interfaceAddrs func() ([]net.Addr, error)
}

// NewManager creates a manager. maxConns caps connections across all listeners (0 = unlimited).
func NewManager(metrics *monitoring.Metrics, maxConns int) *Manager {
	return &Manager{
		metrics:        metrics,
		global:         newLimiter(maxConns),
		listeners:      make(map[string]*Listener),
		seriesRefs:     make(map[string]int),
		conns:          make(map[net.Conn]struct{}),
		retryInitial:   time.Second,
		retryMax:       30 * time.Second,
		listen:         listen,
		interfaceAddrs: net.InterfaceAddrs,
	}
}

// Apply reconciles running listeners with specs: removed ones stop accepting (their connections keep
// running), new ones bind, and existing ones get the new routes and maxConnections. Listeners whose
// bind failed permanently are retried. It returns an error when no listener in specs is bound or
// still retrying, i.e. the process would route nothing.
func (m *Manager) Apply(specs []config.Listener) error {
	local, localErr := m.localAddrs()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.isClosed() {
		return nil
	}
	want := make(map[string]bool, len(specs))
	for _, spec := range specs {
		want[spec.Name()] = true
	}
	// Close removed listeners first so a new listener on the same port can bind.
	for name, l := range m.listeners {
		if want[name] {
			continue
		}
		l.stopListening()
		delete(m.listeners, name)
		m.metrics.DeleteListenerState(name)
		slog.Info("listener removed", "listener", name)
	}
	var failed []error
	for _, spec := range specs {
		l, ok := m.listeners[spec.Name()]
		if ok {
			l.update(spec)
			if !l.isFailed() {
				continue
			}
		} else {
			l = m.newListener(spec)
			m.listeners[spec.Name()] = l
		}
		if err := l.start(); err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", spec.Name(), err))
		}
	}
	if localErr != nil {
		// Leave listener_addr_present as it was: unknown is not the same as missing.
		slog.Warn("list interface addresses failed", "error", localErr)
	} else {
		m.updateAddrPresence(local)
	}
	if len(specs) > 0 && len(failed) == len(specs) {
		return fmt.Errorf("no listener can bind: %w", errors.Join(failed...))
	}
	return nil
}

// WatchAddresses re-checks listener_addr_present every interval until ctx is done.
func (m *Manager) WatchAddresses(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		local, err := m.localAddrs()
		if err != nil {
			slog.Warn("list interface addresses failed", "error", err)
			continue
		}
		m.mu.Lock()
		m.updateAddrPresence(local)
		m.mu.Unlock()
	}
}

func (m *Manager) localAddrs() (map[netip.Addr]bool, error) {
	addrs, err := m.interfaceAddrs()
	if err != nil {
		return nil, err
	}
	local := make(map[netip.Addr]bool, len(addrs))
	for _, a := range addrs {
		var ip net.IP
		switch a := a.(type) {
		case *net.IPNet:
			ip = a.IP
		case *net.IPAddr:
			ip = a.IP
		}
		if addr, ok := netip.AddrFromSlice(ip); ok {
			local[addr.Unmap()] = true
		}
	}
	return local, nil
}

// updateAddrPresence must be called with m.mu held.
func (m *Manager) updateAddrPresence(local map[netip.Addr]bool) {
	for _, l := range m.listeners {
		addr := l.addr.Addr()
		l.setAddrPresent(addr.IsUnspecified() || local[addr.WithZone("")])
	}
}

func (m *Manager) acquireSeries(name string) {
	m.seriesMu.Lock()
	m.seriesRefs[name]++
	m.seriesMu.Unlock()
}

func (m *Manager) releaseSeries(name string) {
	m.seriesMu.Lock()
	defer m.seriesMu.Unlock()
	m.seriesRefs[name]--
	if m.seriesRefs[name] > 0 {
		return
	}
	delete(m.seriesRefs, name)
	m.metrics.ForgetListener(name)
}

func (m *Manager) isClosed() bool {
	m.connsMu.Lock()
	defer m.connsMu.Unlock()
	return m.closed
}

// track registers an accepted connection. It returns false once Shutdown has started.
// A connection accepted just before its listener was removed is still served.
func (m *Manager) track(name string, conn net.Conn) bool {
	m.connsMu.Lock()
	defer m.connsMu.Unlock()
	if m.closed {
		return false
	}
	m.inflight.Add(1)
	m.conns[conn] = struct{}{}
	m.acquireSeries(name)
	return true
}

func (m *Manager) untrack(name string, conn net.Conn) {
	m.connsMu.Lock()
	delete(m.conns, conn)
	m.connsMu.Unlock()
	m.releaseSeries(name)
	m.inflight.Done()
}

// Shutdown stops every listener, waits for in-flight connections, then force-closes what is left when ctx ends.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.connsMu.Lock()
	m.closed = true
	m.connsMu.Unlock()
	m.mu.Lock()
	for _, l := range m.listeners {
		l.stopListening()
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		m.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		m.closeActive()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		return ctx.Err()
	}
}

func (m *Manager) closeActive() {
	m.connsMu.Lock()
	conns := make([]net.Conn, 0, len(m.conns))
	for c := range m.conns {
		conns = append(conns, c)
	}
	m.connsMu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}
