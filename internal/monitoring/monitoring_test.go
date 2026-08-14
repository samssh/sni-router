package monitoring

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func metricValue(t *testing.T, reg *prometheus.Registry, name string, labelPairs map[string]string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if !labelsMatch(metric.GetLabel(), labelPairs) {
				continue
			}
			switch family.GetType() {
			case dto.MetricType_COUNTER:
				return metric.GetCounter().GetValue()
			case dto.MetricType_GAUGE:
				return metric.GetGauge().GetValue()
			case dto.MetricType_HISTOGRAM:
				return float64(metric.GetHistogram().GetSampleCount())
			}
		}
	}
	t.Fatalf("metric %s with labels %v not found", name, labelPairs)
	return 0
}

func labelsMatch(labels []*dto.LabelPair, want map[string]string) bool {
	if len(want) == 0 {
		return true
	}
	got := make(map[string]string, len(labels))
	for _, label := range labels {
		got[label.GetName()] = label.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func TestObserveHelpers(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegisterer(reg)

	m.ObserveOpenInboundConnection()
	m.ObserveReadByteInboundConnection(10)
	m.ObserveWriteByteInboundConnection(4)
	m.ObserveCloseInboundConnection(time.Second)
	m.ObserveParsedSni("prom.example.com", time.Millisecond)
	m.ObserveOpenOutboundConnection("127.0.0.1:8443", "prom.example.com")
	m.ObserveReadByteOutboundConnection("127.0.0.1:8443", "prom.example.com", 8)
	m.ObserveWriteByteOutboundConnection("127.0.0.1:8443", "prom.example.com", 2)
	m.ObserveCloseOutboundConnection("127.0.0.1:8443", "prom.example.com", time.Second)

	if got := metricValue(t, reg, "sni_router_inbound_connections_total", nil); got != 1 {
		t.Fatalf("inbound total = %v, want 1", got)
	}
	if got := metricValue(t, reg, "sni_router_inbound_connections_open", nil); got != 0 {
		t.Fatalf("inbound open = %v, want 0", got)
	}
	if got := metricValue(t, reg, "sni_router_inbound_connections_bytes_in_total", nil); got != 10 {
		t.Fatalf("inbound bytes in = %v, want 10", got)
	}
	if got := metricValue(t, reg, "sni_router_inbound_connections_bytes_out_total", nil); got != 4 {
		t.Fatalf("inbound bytes out = %v, want 4", got)
	}
	if got := metricValue(t, reg, "sni_router_sni_parsed_total", map[string]string{"sni": "prom.example.com"}); got != 1 {
		t.Fatalf("sni parsed = %v, want 1", got)
	}
	if got := metricValue(t, reg, "sni_router_outbound_connections_total", map[string]string{"dst": "127.0.0.1:8443", "sni": "prom.example.com"}); got != 1 {
		t.Fatalf("outbound total = %v, want 1", got)
	}
	if got := metricValue(t, reg, "sni_router_outbound_connections_open", map[string]string{"dst": "127.0.0.1:8443", "sni": "prom.example.com"}); got != 0 {
		t.Fatalf("outbound open = %v, want 0", got)
	}
}
