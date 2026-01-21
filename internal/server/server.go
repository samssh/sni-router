package server

import (
	"fmt"
	"github.com/pires/go-proxyproto"
	"log"
	"net"
	"sni-router/internal/monitoring"
	"sni-router/internal/routing"
	"sni-router/internal/sni"
	"time"
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
	for {
		conn, err := ln.Accept()
		if err != nil {
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
		log.Println("error setting write deadline", err)
	}
	sniValue, err := sni.ExtractSNI(ic.bufReader, l.metrics)
	if err != nil {
		log.Printf("SNI extraction failed. remote: %s error: %s \n", conn.RemoteAddr().String(), err.Error())
		return
	}
	useProxy, dstAddr := l.router.Route(sniValue)
	oc, err := dialTcp(dstAddr, sniValue, l.metrics)
	if err != nil {
		log.Println("Dial Error:" + err.Error())
		return
	}
	if useProxy {
		writeProxyHeader(ic, oc)
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

func writeProxyHeader(ic *InboundConnection, oc *OutboundConnection) {
	headers := proxyproto.HeaderProxyFromAddrs(2, ic.conn.RemoteAddr(), ic.conn.LocalAddr())
	go func() {
		_, err := headers.WriteTo(oc)
		if err != nil {
			log.Println("write proxy header failed:" + err.Error())
		}
	}()
}
