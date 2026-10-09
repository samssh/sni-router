package server

import (
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sni-router/internal/config"
	"sni-router/internal/monitoring"
	"sni-router/internal/routing"
	"sni-router/internal/sni"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pires/go-proxyproto"
)

// limiter caps in-flight connections against its current max (0 = unlimited).
// Lowering the max applies at once: connections already open still count.
type limiter struct {
	max atomic.Int64
	n   atomic.Int64
}

func newLimiter(max int) *limiter {
	l := &limiter{}
	l.max.Store(int64(max))
	return l
}

// setMax reports whether the max changed.
func (l *limiter) setMax(max int) bool {
	return l.max.Swap(int64(max)) != int64(max)
}

func (l *limiter) tryAcquire() bool {
	for {
		n := l.n.Load()
		if max := l.max.Load(); max > 0 && n >= max {
			return false
		}
		if l.n.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

func (l *limiter) release() {
	l.n.Add(-1)
}

// Listener serves one listen address. It is created and removed by a Manager.
type Listener struct {
	name    string
	addr    netip.AddrPort
	mgr     *Manager
	metrics *monitoring.ListenerMetrics
	router  atomic.Pointer[routing.SNIRouter]
	limit   *limiter

	mu      sync.Mutex
	ln      net.Listener
	stopped bool
	failed  bool // the last bind failed permanently; the next Apply retries it
	stop    chan struct{}
	present int8 // last listener_addr_present value, -1 before the first check
}

func (m *Manager) newListener(spec config.Listener) *Listener {
	l := &Listener{
		name:    spec.Name(),
		addr:    spec.Addr,
		mgr:     m,
		metrics: m.metrics.Listener(spec.Name()),
		limit:   newLimiter(spec.MaxConnections),
		stop:    make(chan struct{}),
		present: -1,
	}
	l.router.Store(spec.Router)
	l.metrics.SetUp(false)
	return l
}

// update applies a reloaded spec to a running listener.
func (l *Listener) update(spec config.Listener) {
	l.router.Store(spec.Router)
	if l.limit.setMax(spec.MaxConnections) {
		slog.Info("listener maxConnections changed", "listener", l.name, "maxConnections", spec.MaxConnections)
	}
}

// start makes the first bind attempt. A transient failure keeps retrying in the background;
// a permanent one is returned and only retried by the next Apply.
func (l *Listener) start() error {
	l.mu.Lock()
	l.failed = false
	l.mu.Unlock()
	l.mgr.acquireSeries(l.name)
	ln, err := l.mgr.listen(l.addr)
	switch {
	case err == nil:
		go l.serveAndRelease(ln)
	case isPermanentBindError(err):
		l.fail(err)
		return err
	default:
		slog.Error("bind failed, retrying", "listener", l.name, "error", err, "retry_in", l.mgr.retryInitial)
		go l.retry()
	}
	return nil
}

func (l *Listener) retry() {
	backoff := l.mgr.retryInitial
	for {
		select {
		case <-l.stop:
			l.mgr.releaseSeries(l.name)
			return
		case <-time.After(backoff):
		}
		ln, err := l.mgr.listen(l.addr)
		if err == nil {
			l.serveAndRelease(ln)
			return
		}
		if isPermanentBindError(err) {
			l.fail(err)
			return
		}
		backoff = min(backoff*2, l.mgr.retryMax)
		slog.Error("bind failed, retrying", "listener", l.name, "error", err, "retry_in", backoff)
	}
}

func (l *Listener) fail(err error) {
	l.mu.Lock()
	l.failed = true
	l.mu.Unlock()
	slog.Error("bind failed permanently; fix the cause and reload (SIGHUP) to retry", "listener", l.name, "error", err)
	l.mgr.releaseSeries(l.name)
}

func (l *Listener) isFailed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.failed
}

func (l *Listener) serveAndRelease(ln net.Listener) {
	defer l.mgr.releaseSeries(l.name)
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		_ = ln.Close()
		return
	}
	l.ln = ln
	l.metrics.SetUp(true)
	l.mu.Unlock()
	slog.Info("listening", "listener", l.name)
	l.serve(ln)
}

// stopListening closes the listening socket. Accepted connections are left running.
func (l *Listener) stopListening() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	l.stopped = true
	l.metrics.SetUp(false)
	close(l.stop)
	if l.ln != nil {
		_ = l.ln.Close()
	}
}

func (l *Listener) setAddrPresent(present bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	value := int8(0)
	if present {
		value = 1
	}
	if value == l.present {
		return
	}
	prev := l.present
	l.present = value
	l.metrics.SetAddrPresent(present)
	switch {
	case !present:
		slog.Warn("listener address is not on any local interface", "listener", l.name)
	case prev == 0:
		slog.Info("listener address is back on a local interface", "listener", l.name)
	}
}

func (l *Listener) serve(ln net.Listener) {
	backoff := 10 * time.Millisecond
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			slog.Error("accept failed", "listener", l.name, "error", err)
			time.Sleep(backoff)
			if backoff < time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 10 * time.Millisecond
		enableKeepAlive(conn)
		if !l.limit.tryAcquire() {
			l.rejectMaxConns(conn, "listener")
			continue
		}
		if !l.mgr.global.tryAcquire() {
			l.limit.release()
			l.rejectMaxConns(conn, "global")
			continue
		}
		release := func() {
			l.mgr.global.release()
			l.limit.release()
		}
		if !l.mgr.track(l.name, conn) {
			release()
			slog.Info("closing connection accepted during shutdown", "listener", l.name, "remote", conn.RemoteAddr())
			_ = conn.Close()
			continue
		}
		go func() {
			defer l.mgr.untrack(l.name, conn)
			defer release()
			l.handleConnection(conn)
		}()
	}
}

func (l *Listener) rejectMaxConns(conn net.Conn, limit string) {
	l.metrics.ObserveConnectionError(monitoring.ErrorMaxConns)
	slog.Warn("max connections reached", "listener", l.name, "limit", limit, "remote", conn.RemoteAddr())
	_ = conn.Close()
}

func (l *Listener) handleConnection(conn net.Conn) {
	ic := newInboundConnection(conn, l.metrics)
	defer ic.Close()
	logger := slog.With("id", ic.id, "listener", l.name, "remote", conn.RemoteAddr())
	// Peek into the initial data to parse the ClientHello
	err := ic.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	if err != nil {
		logger.Warn("set read deadline failed", "error", err)
	}
	sniValue, isTls, err := sni.ExtractSNI(ic.bufReader, l.mgr.metrics)
	if err != nil {
		l.metrics.ObserveConnectionError(monitoring.ErrorSNIParse)
		logger.Info("SNI extraction failed", "error", err)
		return
	}
	if err := ic.conn.SetDeadline(time.Time{}); err != nil {
		logger.Warn("clear read deadline failed", "error", err)
	}
	logger = logger.With("sni", sniValue)
	useProxy, dstAddr, dialTimeout, err := l.router.Load().Route(sniValue, isTls)
	if err != nil {
		l.metrics.ObserveConnectionError(monitoring.ErrorNoRoute)
		logger.Info("no route", "error", err)
		return
	}
	logger = logger.With("dest", dstAddr)
	oc, err := dialTcp(dstAddr, sniValue, dialTimeout, l.metrics)
	if err != nil {
		l.metrics.ObserveConnectionError(monitoring.ErrorDial)
		logger.Warn("dial failed", "error", err)
		return
	}
	oc.id = ic.id
	defer oc.Close()
	if useProxy {
		if err := writeProxyHeader(ic, oc); err != nil {
			l.metrics.ObserveConnectionError(monitoring.ErrorProxyHeader)
			logger.Warn("write proxy header failed", "error", err)
			return
		}
	}
	logger.Debug("proxying")
	CopyStreamsBidirectional(ic, oc)
}

func dialTcp(dstAddr string, sniValue string, timeout time.Duration, metrics *monitoring.ListenerMetrics) (*OutboundConnection, error) {
	if timeout <= 0 {
		timeout = time.Duration(routing.DefaultDialTimeoutSeconds) * time.Second
	}
	dst, err := net.DialTimeout("tcp", dstAddr, timeout)
	if err != nil {
		return nil, err
	}
	enableKeepAlive(dst)
	return newOutboundConnection(dst, sniValue, metrics), nil
}

func enableKeepAlive(conn net.Conn) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcp.SetKeepAlive(true)
	_ = tcp.SetKeepAlivePeriod(30 * time.Second)
}

func writeProxyHeader(ic *InboundConnection, oc *OutboundConnection) error {
	// Dest is the address the client connected to on this listener, not a DNAT VIP.
	headers := proxyproto.HeaderProxyFromAddrs(2, ic.conn.RemoteAddr(), ic.conn.LocalAddr())
	_, err := headers.WriteTo(oc)
	return err
}
