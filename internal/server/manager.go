package server

import (
	"context"
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

	mu        sync.Mutex
	listeners map[string]*Listener
	active    map[string]int // open connections per listener name, including removed listeners
	closed    bool

	inflight sync.WaitGroup
	connsMu  sync.Mutex
	conns    map[net.Conn]struct{}

	retryInitial   time.Duration
	retryMax       time.Duration
	interfaceAddrs func() ([]net.Addr, error)
}

// NewManager creates a manager. maxConns caps connections across all listeners (0 = unlimited).
func NewManager(metrics *monitoring.Metrics, maxConns int) *Manager {
	return &Manager{
		metrics:        metrics,
		global:         newLimiter(maxConns),
		listeners:      make(map[string]*Listener),
		active:         make(map[string]int),
		conns:          make(map[net.Conn]struct{}),
		retryInitial:   time.Second,
		retryMax:       30 * time.Second,
		interfaceAddrs: net.InterfaceAddrs,
	}
}

// Apply reconciles running listeners with specs: removed ones stop accepting (their connections keep
// running), new ones start binding, and existing ones get the new routes and maxConnections.
func (m *Manager) Apply(specs []config.Listener) {
	local, localErr := m.localAddrs()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
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
		m.forgetIfIdle(name)
		slog.Info("listener removed", "listener", name, "open_connections", m.active[name])
	}
	for _, spec := range specs {
		if l, ok := m.listeners[spec.Name()]; ok {
			l.update(spec)
			continue
		}
		l := m.newListener(spec)
		m.listeners[spec.Name()] = l
		go l.run()
	}
	if localErr != nil {
		slog.Warn("list interface addresses failed", "error", localErr)
		return
	}
	m.updateAddrPresence(local)
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

// track registers an accepted connection. It returns false if the listener was stopped meanwhile.
func (m *Manager) track(l *Listener, conn net.Conn) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l.isStopped() {
		return false
	}
	m.active[l.name]++
	m.inflight.Add(1)
	m.connsMu.Lock()
	m.conns[conn] = struct{}{}
	m.connsMu.Unlock()
	return true
}

func (m *Manager) untrack(name string, conn net.Conn) {
	m.connsMu.Lock()
	delete(m.conns, conn)
	m.connsMu.Unlock()
	m.mu.Lock()
	m.active[name]--
	m.forgetIfIdle(name)
	m.mu.Unlock()
	m.inflight.Done()
}

// forgetIfIdle drops a removed listener's series once its last connection has closed.
// It must be called with m.mu held.
func (m *Manager) forgetIfIdle(name string) {
	if m.active[name] > 0 {
		return
	}
	delete(m.active, name)
	if _, running := m.listeners[name]; !running {
		m.metrics.ForgetListener(name)
	}
}

// Shutdown stops every listener, waits for in-flight connections, then force-closes what is left when ctx ends.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
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
