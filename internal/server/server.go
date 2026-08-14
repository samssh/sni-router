package server

import (
	"errors"
	"fmt"
	"log"
	"net"
	"sni-router/internal/monitoring"
	"sni-router/internal/routing"
	"sni-router/internal/sni"
	"time"

	"github.com/pires/go-proxyproto"
)

type Listener struct {
	router  *routing.SNIRouter
	metrics *monitoring.Metrics
	port    int
}

func NewListener(router *routing.SNIRouter, metrics *monitoring.Metrics, port int) *Listener {
	return &Listener{
		router:  router,
		metrics: metrics,
		port:    port,
	}
}

func (l *Listener) Listen() {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", l.port))
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		err := ln.Close()
		if err != nil {
			log.Fatal(err)
		}
	}()
	log.Printf("Listening on :%d", l.port)
	l.serve(ln)
}

func (l *Listener) serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Println("Accept error:", err)
			continue
		}
		enableKeepAlive(conn)
		go l.handleConnection(conn)
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
		log.Printf("SNI extraction failed id=%d remote=%s error=%s", ic.id, conn.RemoteAddr(), err)
		return
	}
	if err := ic.conn.SetDeadline(time.Time{}); err != nil {
		log.Printf("error clearing read deadline id=%d remote=%s: %s", ic.id, conn.RemoteAddr(), err)
	}
	useProxy, dstAddr, dialTimeout, err := l.router.Route(sniValue, isTls)
	if err != nil {
		log.Printf("Route error id=%d remote=%s sni=%s error=%s", ic.id, conn.RemoteAddr(), sniValue, err)
		return
	}
	oc, err := dialTcp(dstAddr, sniValue, dialTimeout, l.metrics)
	if err != nil {
		log.Printf("Dial Error id=%d remote=%s sni=%s dest=%s: %s", ic.id, conn.RemoteAddr(), sniValue, dstAddr, err)
		return
	}
	oc.id = ic.id
	defer oc.Close()
	if useProxy {
		if err := writeProxyHeader(ic, oc); err != nil {
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
