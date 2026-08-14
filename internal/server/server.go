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
	log.Println("Listening on :443")
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
		go l.handleConnection(conn)
	}
}

func (l *Listener) handleConnection(conn net.Conn) {
	ic := newInboundConnection(conn, l.metrics)
	defer ic.Close()
	// Peek into the initial data to parse the ClientHello
	err := ic.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	if err != nil {
		log.Println("error setting read deadline", err)
	}
	sniValue, isTls, err := sni.ExtractSNI(ic.bufReader, l.metrics)
	if err != nil {
		log.Printf("SNI extraction failed. remote: %s error: %s \n", conn.RemoteAddr().String(), err.Error())
		return
	}
	if err := ic.conn.SetDeadline(time.Time{}); err != nil {
		log.Println("error clearing read deadline", err)
	}
	useProxy, dstAddr, err := l.router.Route(sniValue, isTls)
	if err != nil {
		log.Printf("Route error. remote: %s error: %s \n", conn.RemoteAddr().String(), err.Error())
		return
	}
	oc, err := dialTcp(dstAddr, sniValue, l.metrics)
	if err != nil {
		log.Println("Dial Error:" + err.Error())
		return
	}
	defer oc.Close()
	if useProxy {
		if err := writeProxyHeader(ic, oc); err != nil {
			log.Println("write proxy header failed:" + err.Error())
			return
		}
	}
	CopyStreamsBidirectional(ic, oc)
}

func dialTcp(dstAddr string, sniValue string, metrics *monitoring.Metrics) (*OutboundConnection, error) {
	dst, err := net.Dial("tcp", dstAddr)
	if err != nil {
		return nil, err
	}
	return newOutboundConnection(dst, sniValue, metrics), nil
}

func writeProxyHeader(ic *InboundConnection, oc *OutboundConnection) error {
	headers := proxyproto.HeaderProxyFromAddrs(2, ic.conn.RemoteAddr(), ic.conn.LocalAddr())
	_, err := headers.WriteTo(oc)
	return err
}
