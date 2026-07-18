package methods

import (
	"context"
	"testing"
	"time"
)

// fakeProber records the last call and returns canned results. Shared by the
// probe_tcp and probe_http method tests (no real network).
type fakeProber struct {
	tcp        TCPResult
	http       HTTPResult
	dns        DNSResult
	grpc       GRPCResult
	gotTarget  string
	gotPort    int
	gotHTTP    HTTPProbeRequest
	gotDNS     DNSProbeRequest
	traceroute TracerouteResult
	gotTrace   TracerouteRequest
	gotGRPC    GRPCProbeRequest
}

func (f *fakeProber) ProbeTCP(_ context.Context, target string, port int, _ time.Duration) TCPResult {
	f.gotTarget, f.gotPort = target, port
	return f.tcp
}

func (f *fakeProber) ProbeHTTP(_ context.Context, req HTTPProbeRequest) HTTPResult {
	f.gotHTTP = req
	return f.http
}

func (f *fakeProber) ProbeDNS(_ context.Context, req DNSProbeRequest) DNSResult {
	f.gotDNS = req
	return f.dns
}

func (f *fakeProber) ProbeTraceroute(_ context.Context, req TracerouteRequest) TracerouteResult {
	f.gotTrace = req
	return f.traceroute
}

func (f *fakeProber) ProbeGRPC(_ context.Context, req GRPCProbeRequest) GRPCResult {
	f.gotGRPC = req
	return f.grpc
}

func TestProbeTCPSuccess(t *testing.T) {
	f := &fakeProber{tcp: TCPResult{Success: true, LatencyMS: 12}}
	m, ok := Builtin().Get("probe_tcp")
	if !ok {
		t.Fatal("probe_tcp not registered")
	}
	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"target": "db.svc", "port": "5432"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["success"] != true || out["latencyMs"] != int64(12) || out["error"] != "" {
		t.Errorf("outputs = %#v", out)
	}
	if f.gotTarget != "db.svc" || f.gotPort != 5432 {
		t.Errorf("prober got target=%q port=%d", f.gotTarget, f.gotPort)
	}
}

func TestProbeTCPFailureIsFindingNotError(t *testing.T) {
	f := &fakeProber{tcp: TCPResult{Success: false, LatencyMS: -1, Err: "connection refused"}}
	m, _ := Builtin().Get("probe_tcp")
	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"target": "db.svc", "port": "5432"})
	if err != nil {
		t.Fatalf("network failure must not be a Go error: %v", err)
	}
	if out["success"] != false || out["error"] != "connection refused" {
		t.Errorf("outputs = %#v", out)
	}
}

func TestProbeTCPParamErrors(t *testing.T) {
	m, _ := Builtin().Get("probe_tcp")
	cases := map[string]map[string]string{
		"empty target":     {"port": "80"},
		"bad port":         {"target": "x", "port": "abc"},
		"port range":       {"target": "x", "port": "70000"},
		"empty port":       {"target": "x", "port": ""},
		"bad timeout":      {"target": "x", "port": "80", "timeout": "soon"},
		"zero timeout":     {"target": "x", "port": "80", "timeout": "0s"},
		"negative timeout": {"target": "x", "port": "80", "timeout": "-1s"},
	}
	for name, params := range cases {
		if _, err := m.Run(context.Background(), Deps{Prober: &fakeProber{}}, params); err == nil {
			t.Errorf("%s: expected param error, got nil", name)
		}
	}
}
