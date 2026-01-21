package monitoring

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"log"
	"net/http"
	"time"
)

type Metrics struct {
	// inbound connections
	inboundConnectionsTotal         prometheus.Counter
	inboundConnectionsOpen          prometheus.Gauge
	inboundConnectionsBytesInTotal  prometheus.Counter
	inboundConnectionsBytesOutTotal prometheus.Counter
	inboundConnectionsTimeSeconds   prometheus.Histogram
	// sni parsing
	sniParsedTotal      *prometheus.CounterVec
	sniParseTimeSeconds *prometheus.HistogramVec
	// outbound connections
	outboundConnectionsTotal         *prometheus.CounterVec
	outboundConnectionsOpen          *prometheus.GaugeVec
	outboundConnectionsBytesInTotal  *prometheus.CounterVec
	outboundConnectionsBytesOutTotal *prometheus.CounterVec
	outboundConnectionsTimeSeconds   *prometheus.HistogramVec
}

func (m *Metrics) ObserveOpenInboundConnection() {
	m.inboundConnectionsTotal.Inc()
	m.inboundConnectionsOpen.Inc()
}

func (m *Metrics) ObserveCloseInboundConnection(openTime time.Duration) {
	m.inboundConnectionsOpen.Dec()
	m.inboundConnectionsTimeSeconds.Observe(openTime.Seconds())
}

func (m *Metrics) ObserveReadByteInboundConnection(byteRead int) {
	m.inboundConnectionsBytesInTotal.Add(float64(byteRead))
}

func (m *Metrics) ObserveWriteByteInboundConnection(byteWritten int) {
	m.inboundConnectionsBytesOutTotal.Add(float64(byteWritten))
}

func (m *Metrics) ObserveParsedSni(sniParsed string, parseTime time.Duration) {
	m.sniParsedTotal.WithLabelValues(sniParsed).Inc()
	m.sniParseTimeSeconds.WithLabelValues(sniParsed).Observe(parseTime.Seconds())
}

func (m *Metrics) ObserveOpenOutboundConnection(dst, sni string) {
	m.outboundConnectionsTotal.WithLabelValues(dst, sni).Inc()
	m.outboundConnectionsOpen.WithLabelValues(dst, sni).Inc()
}

func (m *Metrics) ObserveCloseOutboundConnection(dst, sni string, openTime time.Duration) {
	m.outboundConnectionsOpen.WithLabelValues(dst, sni).Dec()
	m.outboundConnectionsTimeSeconds.WithLabelValues(dst, sni).Observe(openTime.Seconds())
}

func (m *Metrics) ObserveReadByteOutboundConnection(dst, sni string, byteRead int) {
	m.outboundConnectionsBytesInTotal.WithLabelValues(dst, sni).Add(float64(byteRead))
}

func (m *Metrics) ObserveWriteByteOutboundConnection(dst, sni string, byteWrite int) {
	m.outboundConnectionsBytesOutTotal.WithLabelValues(dst, sni).Add(float64(byteWrite))
}

func NewMetrics() *Metrics {
	namespace := "sni_router"
	buckets := []float64{.25, .5, 1, 2.5, 5, 10, 15, 20, 25, 30, 40, 60, 100, 200, 300, 500, 1000, 2000, 4000, 8000, 16000}
	return &Metrics{
		// inbound connections
		inboundConnectionsTotal: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "inbound_connections_total",
			Help:      "The total number of inbound connections",
		}),
		inboundConnectionsOpen: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "inbound_connections_open",
			Help:      "Number of open inbound connections",
		}),
		inboundConnectionsBytesInTotal: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "inbound_connections_bytes_in_total",
			Help:      "Total number of bytes received from inbound connections",
		}),
		inboundConnectionsBytesOutTotal: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "inbound_connections_bytes_out_total",
			Help:      "Total number of bytes sent to inbound connections",
		}),
		inboundConnectionsTimeSeconds: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "inbound_connections_time_seconds",
			Help:      "Histogram of time to inbound connections is open in seconds",
			Buckets:   buckets,
		}),
		// sni parsing
		sniParsedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "sni_parsed_total",
			Help:      "Total number of snis parsed successfully or with error",
		}, []string{"sni"}),
		sniParseTimeSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "sni_parsed_time_Seconds",
			Help:      "Histogram of time to sni parse successfully or with error in seconds",
			Buckets:   buckets,
		}, []string{"sni"}),
		// outbound connection
		outboundConnectionsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "outbound_connections_total",
			Help:      "Total number of outbound connections",
		}, []string{"dst", "sni"}),
		outboundConnectionsOpen: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "outbound_connections_open",
			Help:      "Number of open outbound connections",
		}, []string{"dst", "sni"}),
		outboundConnectionsBytesInTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "outbound_connections_bytes_in_total",
			Help:      "Total number of bytes received from outbound connections",
		}, []string{"dst", "sni"}),
		outboundConnectionsBytesOutTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "outbound_connections_bytes_out_total",
			Help:      "Total number of bytes sent to outbound connections",
		}, []string{"dst", "sni"}),
		outboundConnectionsTimeSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "outbound_connections_time_seconds",
			Help:      "Histogram of time to outbound connections is open in seconds",
			Buckets:   buckets,
		}, []string{"dst", "sni"}),
	}
}

func (*Metrics) Start(port int) {
	log.Printf("Server is running on :%d\n", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), promhttp.Handler()))
}
