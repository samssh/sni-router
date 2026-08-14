package server

import (
	"bufio"
	"io"
	"log"
	"net"
	"sni-router/internal/monitoring"
	"sync"
	"sync/atomic"
	"time"
)

var nextConnID atomic.Uint64

type Connection interface {
	io.Reader
	io.Writer
	Close()
}

type connection struct {
	id        uint64
	conn      net.Conn
	bufReader *bufio.Reader
	once      sync.Once
	metrics   *monitoring.Metrics
	openTime  time.Time
}

type InboundConnection struct {
	connection
}

func (ic *InboundConnection) Read(p []byte) (int, error) {
	n, err := ic.bufReader.Read(p)
	ic.metrics.ObserveReadByteInboundConnection(n)
	return n, err
}

func (ic *InboundConnection) Write(p []byte) (int, error) {
	n, err := ic.conn.Write(p)
	ic.metrics.ObserveWriteByteInboundConnection(n)
	return n, err
}

func (c *connection) CloseWrite() {
	if tcp, ok := c.conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
		return
	}
	_ = c.conn.Close()
}

func (ic *InboundConnection) Close() {
	ic.once.Do(func() {
		err := ic.conn.Close()
		ic.metrics.ObserveCloseInboundConnection(time.Since(ic.openTime))
		if err != nil {
			log.Printf("error in close connection id=%d: %s", ic.id, err)
		}
	})
}

func newInboundConnection(conn net.Conn, metrics *monitoring.Metrics) *InboundConnection {
	metrics.ObserveOpenInboundConnection()
	return &InboundConnection{
		connection: connection{
			id:        nextConnID.Add(1),
			conn:      conn,
			bufReader: bufio.NewReader(conn),
			metrics:   metrics,
			openTime:  time.Now(),
		},
	}
}

type OutboundConnection struct {
	connection
	sniValue string
}

func (oc *OutboundConnection) Read(p []byte) (int, error) {
	n, err := oc.bufReader.Read(p)
	oc.metrics.ObserveReadByteOutboundConnection(oc.conn.RemoteAddr().String(), oc.sniValue, n)
	return n, err
}

func (oc *OutboundConnection) Write(p []byte) (int, error) {
	n, err := oc.conn.Write(p)
	oc.metrics.ObserveWriteByteOutboundConnection(oc.conn.RemoteAddr().String(), oc.sniValue, n)
	return n, err
}

func (oc *OutboundConnection) Close() {
	oc.once.Do(func() {
		err := oc.conn.Close()
		oc.metrics.ObserveCloseOutboundConnection(oc.conn.RemoteAddr().String(), oc.sniValue, time.Since(oc.openTime))
		if err != nil {
			log.Printf("error in close connection id=%d sni=%s: %s", oc.id, oc.sniValue, err)
		}
	})
}

func newOutboundConnection(conn net.Conn, sniValue string, metrics *monitoring.Metrics) *OutboundConnection {
	metrics.ObserveOpenOutboundConnection(conn.RemoteAddr().String(), sniValue)
	return &OutboundConnection{
		connection: connection{
			conn:      conn,
			bufReader: bufio.NewReader(conn),
			once:      sync.Once{},
			metrics:   metrics,
			openTime:  time.Now(),
		},
		sniValue: sniValue,
	}
}
