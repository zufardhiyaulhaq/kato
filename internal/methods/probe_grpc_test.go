package methods

import (
	"context"
	"testing"
)

func TestProbeGRPCServingAndDefaults(t *testing.T) {
	f := &fakeProber{grpc: GRPCResult{Serving: true, Status: "SERVING", LatencyMS: 7}}
	m, ok := Builtin().Get("probe_grpc")
	if !ok {
		t.Fatal("probe_grpc not registered")
	}
	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"target": "cart.svc", "port": "50051"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["success"] != true || out["status"] != "SERVING" ||
		out["latencyMs"] != int64(7) || out["error"] != "" {
		t.Errorf("outputs = %#v", out)
	}
	// defaults: plaintext, no insecure, no service, 5s timeout.
	g := f.gotGRPC
	if g.TLS || g.InsecureSkipVerify || g.Service != "" || g.ServerName != "" || g.Timeout.String() != "5s" {
		t.Errorf("defaults wrong: %+v", g)
	}
}

func TestProbeGRPCNotServingIsFindingNotError(t *testing.T) {
	f := &fakeProber{grpc: GRPCResult{Serving: false, Status: "NOT_SERVING", LatencyMS: 3}}
	m, _ := Builtin().Get("probe_grpc")
	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"target": "cart.svc", "port": "50051"})
	if err != nil {
		t.Fatalf("health failure must not be a Go error: %v", err)
	}
	if out["success"] != false || out["status"] != "NOT_SERVING" {
		t.Errorf("outputs = %#v", out)
	}
}

func TestProbeGRPCTransportFailureIsFinding(t *testing.T) {
	f := &fakeProber{grpc: GRPCResult{Serving: false, Status: "", LatencyMS: -1, Err: "connection refused"}}
	m, _ := Builtin().Get("probe_grpc")
	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"target": "cart.svc", "port": "50051"})
	if err != nil {
		t.Fatalf("transport failure must not be a Go error: %v", err)
	}
	if out["success"] != false || out["status"] != "" ||
		out["error"] != "connection refused" || out["latencyMs"] != int64(-1) {
		t.Errorf("outputs = %#v", out)
	}
}

func TestProbeGRPCParamPassThrough(t *testing.T) {
	f := &fakeProber{grpc: GRPCResult{Serving: true, Status: "SERVING"}}
	m, _ := Builtin().Get("probe_grpc")
	_, err := m.Run(context.Background(), Deps{Prober: f}, map[string]string{
		"target": "cart.svc", "port": "8443", "service": "cart.v1.Cart",
		"tls": "true", "insecureSkipVerify": "true", "serverName": "cart.internal", "timeout": "3s",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	g := f.gotGRPC
	if g.Target != "cart.svc" || g.Port != 8443 || g.Service != "cart.v1.Cart" ||
		!g.TLS || !g.InsecureSkipVerify || g.ServerName != "cart.internal" || g.Timeout.String() != "3s" {
		t.Errorf("pass-through wrong: %+v", g)
	}
}

func TestProbeGRPCParamErrors(t *testing.T) {
	m, _ := Builtin().Get("probe_grpc")
	cases := map[string]map[string]string{
		"empty target":     {"port": "50051"},
		"bad port":         {"target": "x", "port": "abc"},
		"port range":       {"target": "x", "port": "70000"},
		"empty port":       {"target": "x", "port": ""},
		"bad tls":          {"target": "x", "port": "50051", "tls": "maybe"},
		"bad insecure":     {"target": "x", "port": "50051", "insecureSkipVerify": "maybe"},
		"bad timeout":      {"target": "x", "port": "50051", "timeout": "soon"},
		"zero timeout":     {"target": "x", "port": "50051", "timeout": "0s"},
		"negative timeout": {"target": "x", "port": "50051", "timeout": "-1s"},
	}
	for name, params := range cases {
		if _, err := m.Run(context.Background(), Deps{Prober: &fakeProber{}}, params); err == nil {
			t.Errorf("%s: expected param error, got nil", name)
		}
	}
}
