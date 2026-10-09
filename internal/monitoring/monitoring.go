package monitoring

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	// inbound connections
	inboundConnectionsTotal         *prometheus.CounterVec
	inboundConnectionsOpen          *prometheus.GaugeVec
	inboundConnectionsBytesInTotal  *prometheus.CounterVec
	inboundConnectionsBytesOutTotal *prometheus.CounterVec
	inboundConnectionsTimeSeconds   *prometheus.HistogramVec
	// sni parsing
	sniParsedTotal      *prometheus.CounterVec
	sniParseTimeSeconds *prometheus.HistogramVec
	// outbound connections
	outboundConnectionsTotal         *prometheus.CounterVec
	outboundConnectionsOpen          *prometheus.GaugeVec
	outboundConnectionsBytesInTotal  *prometheus.CounterVec
	outboundConnectionsBytesOutTotal *prometheus.CounterVec
	outboundConnectionsTimeSeconds   *prometheus.HistogramVec
	connectionErrorsTotal            *prometheus.CounterVec
	// listeners
	listenerUp          *prometheus.GaugeVec
	listenerAddrPresent *prometheus.GaugeVec
}

const (
	ErrorSNIParse    = "sni_parse"
	ErrorNoRoute     = "no_route"
	ErrorDial        = "dial"
	ErrorMaxConns    = "max_conns"
	ErrorProxyHeader = "proxy_header"
)

func sniLabel(sni string) string {
	switch sni {
	case "error":
		return "error"
	case "":
		return "empty"
	default:
		return "present"
	}
}

func (m *Metrics) ObserveParsedSni(sniParsed string, parseTime time.Duration) {
	label := sniLabel(sniParsed)
	m.sniParsedTotal.WithLabelValues(label).Inc()
	m.sniParseTimeSeconds.WithLabelValues(label).Observe(parseTime.Seconds())
}

// ListenerMetrics records connection metrics with the listener label already set.
type ListenerMetrics struct {
	inboundTotal     prometheus.Counter
	inboundOpen      prometheus.Gauge
	inboundBytesIn   prometheus.Counter
	inboundBytesOut  prometheus.Counter
	inboundTime      prometheus.Observer
	outboundTotal    *prometheus.CounterVec
	outboundOpen     *prometheus.GaugeVec
	outboundBytesIn  *prometheus.CounterVec
	outboundBytesOut *prometheus.CounterVec
	outboundTime     prometheus.ObserverVec
	errors           *prometheus.CounterVec
	up               prometheus.Gauge
	addrPresent      prometheus.Gauge
}

func (m *Metrics) Listener(name string) *ListenerMetrics {
	labels := prometheus.Labels{"listener": name}
	return &ListenerMetrics{
		inboundTotal:     m.inboundConnectionsTotal.WithLabelValues(name),
		inboundOpen:      m.inboundConnectionsOpen.WithLabelValues(name),
		inboundBytesIn:   m.inboundConnectionsBytesInTotal.WithLabelValues(name),
		inboundBytesOut:  m.inboundConnectionsBytesOutTotal.WithLabelValues(name),
		inboundTime:      m.inboundConnectionsTimeSeconds.WithLabelValues(name),
		outboundTotal:    m.outboundConnectionsTotal.MustCurryWith(labels),
		outboundOpen:     m.outboundConnectionsOpen.MustCurryWith(labels),
		outboundBytesIn:  m.outboundConnectionsBytesInTotal.MustCurryWith(labels),
		outboundBytesOut: m.outboundConnectionsBytesOutTotal.MustCurryWith(labels),
		outboundTime:     m.outboundConnectionsTimeSeconds.MustCurryWith(labels),
		errors:           m.connectionErrorsTotal.MustCurryWith(labels),
		up:               m.listenerUp.WithLabelValues(name),
		addrPresent:      m.listenerAddrPresent.WithLabelValues(name),
	}
}

// DeleteListenerState removes the listener_up and listener_addr_present series.
func (m *Metrics) DeleteListenerState(name string) {
	m.listenerUp.DeleteLabelValues(name)
	m.listenerAddrPresent.DeleteLabelValues(name)
}

// ForgetListener removes every connection series of a listener. Call it only once the
// listener is gone and its last connection has closed, or the open gauges stop adding up.
func (m *Metrics) ForgetListener(name string) {
	labels := prometheus.Labels{"listener": name}
	m.inboundConnectionsTotal.DeletePartialMatch(labels)
	m.inboundConnectionsOpen.DeletePartialMatch(labels)
	m.inboundConnectionsBytesInTotal.DeletePartialMatch(labels)
	m.inboundConnectionsBytesOutTotal.DeletePartialMatch(labels)
	m.inboundConnectionsTimeSeconds.DeletePartialMatch(labels)
	m.outboundConnectionsTotal.DeletePartialMatch(labels)
	m.outboundConnectionsOpen.DeletePartialMatch(labels)
	m.outboundConnectionsBytesInTotal.DeletePartialMatch(labels)
	m.outboundConnectionsBytesOutTotal.DeletePartialMatch(labels)
	m.outboundConnectionsTimeSeconds.DeletePartialMatch(labels)
	m.connectionErrorsTotal.DeletePartialMatch(labels)
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (lm *ListenerMetrics) SetUp(up bool) {
	lm.up.Set(boolGauge(up))
}

func (lm *ListenerMetrics) SetAddrPresent(present bool) {
	lm.addrPresent.Set(boolGauge(present))
}

func (lm *ListenerMetrics) ObserveOpenInboundConnection() {
	lm.inboundTotal.Inc()
	lm.inboundOpen.Inc()
}

func (lm *ListenerMetrics) ObserveCloseInboundConnection(openTime time.Duration) {
	lm.inboundOpen.Dec()
	lm.inboundTime.Observe(openTime.Seconds())
}

func (lm *ListenerMetrics) ObserveReadByteInboundConnection(byteRead int) {
	lm.inboundBytesIn.Add(float64(byteRead))
}

func (lm *ListenerMetrics) ObserveWriteByteInboundConnection(byteWritten int) {
	lm.inboundBytesOut.Add(float64(byteWritten))
}

func (lm *ListenerMetrics) ObserveOpenOutboundConnection(dst, sni string) {
	sni = sniLabel(sni)
	lm.outboundTotal.WithLabelValues(dst, sni).Inc()
	lm.outboundOpen.WithLabelValues(dst, sni).Inc()
}

func (lm *ListenerMetrics) ObserveCloseOutboundConnection(dst, sni string, openTime time.Duration) {
	sni = sniLabel(sni)
	lm.outboundOpen.WithLabelValues(dst, sni).Dec()
	lm.outboundTime.WithLabelValues(dst, sni).Observe(openTime.Seconds())
}

func (lm *ListenerMetrics) ObserveReadByteOutboundConnection(dst, sni string, byteRead int) {
	lm.outboundBytesIn.WithLabelValues(dst, sniLabel(sni)).Add(float64(byteRead))
}

func (lm *ListenerMetrics) ObserveWriteByteOutboundConnection(dst, sni string, byteWrite int) {
	lm.outboundBytesOut.WithLabelValues(dst, sniLabel(sni)).Add(float64(byteWrite))
}

func (lm *ListenerMetrics) ObserveConnectionError(reason string) {
	lm.errors.WithLabelValues(reason).Inc()
}

func NewMetrics() *Metrics {
	return NewMetricsWithRegisterer(prometheus.DefaultRegisterer)
}

func NewMetricsWithRegisterer(reg prometheus.Registerer) *Metrics {
	factory := promauto.With(reg)
	namespace := "sni_router"
	parseBuckets := []float64{.25, .5, 1, 2.5, 5, 10, 15, 20, 25, 30, 40, 60, 100, 200, 300, 500, 1000, 2000, 4000, 8000, 16000}
	connBuckets := append(append([]float64{}, parseBuckets...), 28800, 43200, 86400, 172800, 604800)
	return &Metrics{
		// inbound connections
		inboundConnectionsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "inbound_connections_total",
			Help:      "The total number of inbound connections",
		}, []string{"listener"}),
		inboundConnectionsOpen: factory.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "inbound_connections_open",
			Help:      "Number of open inbound connections",
		}, []string{"listener"}),
		inboundConnectionsBytesInTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "inbound_connections_bytes_in_total",
			Help:      "Total number of bytes received from inbound connections",
		}, []string{"listener"}),
		inboundConnectionsBytesOutTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "inbound_connections_bytes_out_total",
			Help:      "Total number of bytes sent to inbound connections",
		}, []string{"listener"}),
		inboundConnectionsTimeSeconds: factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "inbound_connections_time_seconds",
			Help:      "Histogram of time to inbound connections is open in seconds",
			Buckets:   connBuckets,
		}, []string{"listener"}),
		// sni parsing
		sniParsedTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "sni_parsed_total",
			Help:      "Total number of snis parsed successfully or with error",
		}, []string{"sni"}),
		sniParseTimeSeconds: factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "sni_parsed_time_seconds",
			Help:      "Histogram of time to sni parse successfully or with error in seconds",
			Buckets:   parseBuckets,
		}, []string{"sni"}),
		// outbound connection
		outboundConnectionsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "outbound_connections_total",
			Help:      "Total number of outbound connections",
		}, []string{"listener", "dst", "sni"}),
		outboundConnectionsOpen: factory.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "outbound_connections_open",
			Help:      "Number of open outbound connections",
		}, []string{"listener", "dst", "sni"}),
		outboundConnectionsBytesInTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "outbound_connections_bytes_in_total",
			Help:      "Total number of bytes received from outbound connections",
		}, []string{"listener", "dst", "sni"}),
		outboundConnectionsBytesOutTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "outbound_connections_bytes_out_total",
			Help:      "Total number of bytes sent to outbound connections",
		}, []string{"listener", "dst", "sni"}),
		outboundConnectionsTimeSeconds: factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "outbound_connections_time_seconds",
			Help:      "Histogram of time to outbound connections is open in seconds",
			Buckets:   connBuckets,
		}, []string{"listener", "dst", "sni"}),
		connectionErrorsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "connection_errors_total",
			Help:      "Total number of failed connections by reason",
		}, []string{"listener", "reason"}),
		// listeners
		listenerUp: factory.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "listener_up",
			Help:      "1 if the listener is bound and accepting, 0 while its bind is being retried",
		}, []string{"listener"}),
		listenerAddrPresent: factory.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "listener_addr_present",
			Help:      "1 if the listener's IP is assigned to a local interface (always 1 for wildcards)",
		}, []string{"listener"}),
	}
}

func (*Metrics) Start(port int) {
	slog.Info("metrics server listening", "port", port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), promhttp.Handler()); err != nil {
		slog.Error("metrics server failed", "error", err)
	}
}
