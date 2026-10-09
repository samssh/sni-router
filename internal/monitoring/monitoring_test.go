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
	lm := m.Listener("203.0.113.10:443")
	listener := map[string]string{"listener": "203.0.113.10:443"}

	lm.ObserveOpenInboundConnection()
	lm.ObserveReadByteInboundConnection(10)
	lm.ObserveWriteByteInboundConnection(4)
	lm.ObserveCloseInboundConnection(time.Second)
	m.ObserveParsedSni("prom.example.com", time.Millisecond)
	lm.ObserveOpenOutboundConnection("127.0.0.1:8443", "prom.example.com")
	lm.ObserveReadByteOutboundConnection("127.0.0.1:8443", "prom.example.com", 8)
	lm.ObserveWriteByteOutboundConnection("127.0.0.1:8443", "prom.example.com", 2)
	lm.ObserveCloseOutboundConnection("127.0.0.1:8443", "prom.example.com", time.Second)

	if got := metricValue(t, reg, "sni_router_inbound_connections_total", listener); got != 1 {
		t.Fatalf("inbound total = %v, want 1", got)
	}
	if got := metricValue(t, reg, "sni_router_inbound_connections_open", listener); got != 0 {
		t.Fatalf("inbound open = %v, want 0", got)
	}
	if got := metricValue(t, reg, "sni_router_inbound_connections_bytes_in_total", listener); got != 10 {
		t.Fatalf("inbound bytes in = %v, want 10", got)
	}
	if got := metricValue(t, reg, "sni_router_inbound_connections_bytes_out_total", listener); got != 4 {
		t.Fatalf("inbound bytes out = %v, want 4", got)
	}
	if got := metricValue(t, reg, "sni_router_sni_parsed_total", map[string]string{"sni": "present"}); got != 1 {
		t.Fatalf("sni parsed = %v, want 1", got)
	}
	outbound := map[string]string{"listener": "203.0.113.10:443", "dst": "127.0.0.1:8443", "sni": "present"}
	if got := metricValue(t, reg, "sni_router_outbound_connections_total", outbound); got != 1 {
		t.Fatalf("outbound total = %v, want 1", got)
	}
	if got := metricValue(t, reg, "sni_router_outbound_connections_open", outbound); got != 0 {
		t.Fatalf("outbound open = %v, want 0", got)
	}

	lm.ObserveConnectionError(ErrorDial)
	lm.ObserveConnectionError(ErrorDial)
	if got := metricValue(t, reg, "sni_router_connection_errors_total", map[string]string{"listener": "203.0.113.10:443", "reason": ErrorDial}); got != 2 {
		t.Fatalf("dial errors = %v, want 2", got)
	}
}

func TestListenerSeriesDeletion(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegisterer(reg)
	gone := m.Listener("203.0.113.10:443")
	kept := m.Listener("203.0.113.11:443")
	for _, lm := range []*ListenerMetrics{gone, kept} {
		lm.SetUp(true)
		lm.SetAddrPresent(true)
		lm.ObserveOpenInboundConnection()
		lm.ObserveOpenOutboundConnection("127.0.0.1:8443", "prom.example.com")
		lm.ObserveConnectionError(ErrorDial)
	}

	m.DeleteListenerState("203.0.113.10:443")
	if hasSeries(t, reg, "sni_router_listener_up", "203.0.113.10:443") || hasSeries(t, reg, "sni_router_listener_addr_present", "203.0.113.10:443") {
		t.Fatal("listener state series should be deleted")
	}
	if !hasSeries(t, reg, "sni_router_inbound_connections_open", "203.0.113.10:443") {
		t.Fatal("connection series should survive DeleteListenerState")
	}

	m.ForgetListener("203.0.113.10:443")
	for _, name := range []string{
		"sni_router_inbound_connections_total",
		"sni_router_inbound_connections_open",
		"sni_router_outbound_connections_total",
		"sni_router_outbound_connections_open",
		"sni_router_connection_errors_total",
	} {
		if hasSeries(t, reg, name, "203.0.113.10:443") {
			t.Fatalf("%s should be deleted", name)
		}
		if !hasSeries(t, reg, name, "203.0.113.11:443") {
			t.Fatalf("%s for the other listener should be kept", name)
		}
	}
	if !hasSeries(t, reg, "sni_router_listener_up", "203.0.113.11:443") {
		t.Fatal("other listener state should be kept")
	}
}

func hasSeries(t *testing.T, reg *prometheus.Registry, name, listener string) bool {
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
			if labelsMatch(metric.GetLabel(), map[string]string{"listener": listener}) {
				return true
			}
		}
	}
	return false
}
