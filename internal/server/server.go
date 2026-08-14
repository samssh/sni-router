package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sni-router/internal/monitoring"
	"sni-router/internal/routing"
	"sni-router/internal/sni"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pires/go-proxyproto"
)

type Listener struct {
	router   atomic.Pointer[routing.SNIRouter]
	metrics  *monitoring.Metrics
	port     int
	addr     string
	maxConns int
	sem      chan struct{}
	ln       net.Listener
	inflight sync.WaitGroup
	dropUID  int
	dropGID  int
}

func NewListener(router *routing.SNIRouter, metrics *monitoring.Metrics, port int) *Listener {
	l := &Listener{
		metrics: metrics,
		port:    port,
	}
	l.router.Store(router)
	return l
}

func (l *Listener) SetRouter(router *routing.SNIRouter) {
	l.router.Store(router)
}

func (l *Listener) WithListenAddr(addr string) *Listener {
	l.addr = addr
	return l
}

func (l *Listener) listenAddr() string {
	if l.addr == "" {
		return fmt.Sprintf(":%d", l.port)
	}
	return net.JoinHostPort(l.addr, strconv.Itoa(l.port))
}

func (l *Listener) WithMaxConns(n int) *Listener {
	l.maxConns = n
	if n > 0 {
		l.sem = make(chan struct{}, n)
	}
	return l
}

func (l *Listener) WithDropPrivileges(uid, gid int) *Listener {
	l.dropUID = uid
	l.dropGID = gid
	return l
}

func (l *Listener) Listen() {
	ln, err := net.Listen("tcp", l.listenAddr())
	if err != nil {
		log.Fatal(err)
	}
	l.ln = ln
	defer func() {
		err := ln.Close()
		if err != nil && !errors.Is(err, net.ErrClosed) {
			log.Println(err)
		}
	}()
	if l.dropUID > 0 || l.dropGID > 0 {
		if err := dropPrivileges(l.dropUID, l.dropGID); err != nil {
			log.Fatalf("drop privileges: %s", err)
		}
		log.Printf("dropped privileges uid=%d gid=%d", l.dropUID, l.dropGID)
	}
	log.Printf("Listening on %s", l.listenAddr())
	l.serve(ln)
}

func (l *Listener) Shutdown(ctx context.Context) error {
	if l.ln != nil {
		_ = l.ln.Close()
	}
	done := make(chan struct{})
	go func() {
		l.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
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
			log.Println("Accept error:", err)
			time.Sleep(backoff)
			if backoff < time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 10 * time.Millisecond
		enableKeepAlive(conn)
		if l.sem != nil {
			select {
			case l.sem <- struct{}{}:
			default:
				l.metrics.ObserveConnectionError(monitoring.ErrorMaxConns)
				log.Printf("max connections reached remote=%s", conn.RemoteAddr())
				_ = conn.Close()
				continue
			}
		}
		l.inflight.Add(1)
		go func() {
			defer l.inflight.Done()
			defer func() {
				if l.sem != nil {
					<-l.sem
				}
			}()
			l.handleConnection(conn)
		}()
	}
}

func (l *Listener) handleConnection(conn net.Conn) {
	ic := newInboundConnection(conn, l.metrics)
	defer ic.Close()
	// Peek into the initial data to parse the ClientHello
	err := ic.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	if err != nil {
		log.Printf("error setting read deadline id=%d remote=%s: %s", ic.id, conn.RemoteAddr(), err)
	}
	sniValue, isTls, err := sni.ExtractSNI(ic.bufReader, l.metrics)
	if err != nil {
		l.metrics.ObserveConnectionError(monitoring.ErrorSNIParse)
		log.Printf("SNI extraction failed id=%d remote=%s error=%s", ic.id, conn.RemoteAddr(), err)
		return
	}
	if err := ic.conn.SetDeadline(time.Time{}); err != nil {
		log.Printf("error clearing read deadline id=%d remote=%s: %s", ic.id, conn.RemoteAddr(), err)
	}
	useProxy, dstAddr, dialTimeout, err := l.router.Load().Route(sniValue, isTls)
	if err != nil {
		l.metrics.ObserveConnectionError(monitoring.ErrorNoRoute)
		log.Printf("Route error id=%d remote=%s sni=%s error=%s", ic.id, conn.RemoteAddr(), sniValue, err)
		return
	}
	oc, err := dialTcp(dstAddr, sniValue, dialTimeout, l.metrics)
	if err != nil {
		l.metrics.ObserveConnectionError(monitoring.ErrorDial)
		log.Printf("Dial Error id=%d remote=%s sni=%s dest=%s: %s", ic.id, conn.RemoteAddr(), sniValue, dstAddr, err)
		return
	}
	oc.id = ic.id
	defer oc.Close()
	if useProxy {
		if err := writeProxyHeader(ic, oc); err != nil {
			l.metrics.ObserveConnectionError(monitoring.ErrorProxyHeader)
			log.Printf("write proxy header failed id=%d remote=%s sni=%s dest=%s: %s", ic.id, conn.RemoteAddr(), sniValue, dstAddr, err)
			return
		}
	}
	CopyStreamsBidirectional(ic, oc)
}

func dialTcp(dstAddr string, sniValue string, timeout time.Duration, metrics *monitoring.Metrics) (*OutboundConnection, error) {
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
	// Dest is the listen address, not a DNAT VIP.
	headers := proxyproto.HeaderProxyFromAddrs(2, ic.conn.RemoteAddr(), ic.conn.LocalAddr())
	_, err := headers.WriteTo(oc)
	return err
}
