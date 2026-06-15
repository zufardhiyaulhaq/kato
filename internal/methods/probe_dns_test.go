package methods

import (
	"context"
	"testing"
	"time"
)

func TestProbeDNSSuccess(t *testing.T) {
	f := &fakeProber{dns: DNSResult{Resolved: true, Addresses: []string{"10.0.0.5", "10.0.0.6"}, LatencyMS: 3}}
	m, ok := Builtin().Get("probe_dns")
	if !ok {
		t.Fatal("probe_dns not registered")
	}
	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"name": "kubernetes.default.svc.cluster.local"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["success"] != true || out["addresses"] != "10.0.0.5, 10.0.0.6" ||
		out["recordCount"] != int64(2) || out["latencyMs"] != int64(3) || out["error"] != "" {
		t.Errorf("outputs = %#v", out)
	}
	if f.gotDNS.Name != "kubernetes.default.svc.cluster.local" {
		t.Errorf("prober got name=%q", f.gotDNS.Name)
	}
	// Unset port defaults to 53; system resolver (no server).
	if f.gotDNS.Port != 53 || f.gotDNS.Server != "" {
		t.Errorf("prober got server=%q port=%d", f.gotDNS.Server, f.gotDNS.Port)
	}
}

func TestProbeDNSFailureIsFindingNotError(t *testing.T) {
	f := &fakeProber{dns: DNSResult{Resolved: false, LatencyMS: -1, Err: "no such host"}}
	m, _ := Builtin().Get("probe_dns")
	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"name": "bogus.example"})
	if err != nil {
		t.Fatalf("resolution failure must not be a Go error: %v", err)
	}
	if out["success"] != false || out["recordCount"] != int64(0) ||
		out["addresses"] != "" || out["error"] != "no such host" {
		t.Errorf("outputs = %#v", out)
	}
}

func TestProbeDNSPassesServerAndPort(t *testing.T) {
	f := &fakeProber{dns: DNSResult{Resolved: true, Addresses: []string{"10.0.0.10"}}}
	m, _ := Builtin().Get("probe_dns")
	_, err := m.Run(context.Background(), Deps{Prober: f}, map[string]string{
		"name": "svc.ns.svc.cluster.local", "server": "10.96.0.10", "port": "5353", "timeout": "2s",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.gotDNS.Server != "10.96.0.10" || f.gotDNS.Port != 5353 || f.gotDNS.Timeout != 2*time.Second {
		t.Errorf("prober got %#v", f.gotDNS)
	}
}

func TestProbeDNSParamErrors(t *testing.T) {
	m, _ := Builtin().Get("probe_dns")
	cases := map[string]map[string]string{
		"empty name":       {},
		"bad port":         {"name": "x", "port": "abc"},
		"port range":       {"name": "x", "port": "70000"},
		"bad timeout":      {"name": "x", "timeout": "soon"},
		"zero timeout":     {"name": "x", "timeout": "0s"},
		"negative timeout": {"name": "x", "timeout": "-1s"},
	}
	for name, params := range cases {
		if _, err := m.Run(context.Background(), Deps{Prober: &fakeProber{}}, params); err == nil {
			t.Errorf("%s: expected param error, got nil", name)
		}
	}
}
