package methods

import (
	"context"
	"testing"
	"time"
)

func TestProbeTLSSuccess(t *testing.T) {
	f := &fakeProber{tlsRes: TLSResult{
		HandshakeComplete: true, Verified: true, DaysUntilExpiry: 90,
		NotAfter: "2026-10-16T00:00:00Z", Issuer: "R3", Subject: "example.com",
		DNSNames: "example.com,www.example.com", TLSVersion: "TLS1.3", LatencyMS: 14,
	}}
	m, ok := Builtin().Get("probe_tls")
	if !ok {
		t.Fatal("probe_tls not registered")
	}
	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"target": "example.com", "port": "443"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := Outputs{
		"success": true, "handshakeComplete": true, "verified": true, "expired": false,
		"daysUntilExpiry": int64(90), "notAfter": "2026-10-16T00:00:00Z", "issuer": "R3",
		"subject": "example.com", "dnsNames": "example.com,www.example.com",
		"tlsVersion": "TLS1.3", "verifyError": "", "latencyMs": int64(14), "error": "",
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("out[%q] = %v, want %v", k, out[k], v)
		}
	}
	if f.gotTLS.Timeout != 5*time.Second || f.gotTLS.ServerName != "" {
		t.Errorf("defaults not applied: %+v", f.gotTLS)
	}
	if f.gotTLS.Target != "example.com" || f.gotTLS.Port != 443 {
		t.Errorf("request passthrough wrong: %+v", f.gotTLS)
	}
}

func TestProbeTLSUnverifiedIsAFinding(t *testing.T) {
	f := &fakeProber{tlsRes: TLSResult{
		HandshakeComplete: true, Verified: false,
		VerifyError:     "x509: certificate signed by unknown authority",
		DaysUntilExpiry: 42, NotAfter: "2026-08-29T00:00:00Z", Subject: "internal.svc",
		TLSVersion: "TLS1.3", LatencyMS: 9,
	}}
	m, _ := Builtin().Get("probe_tls")
	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"target": "internal.svc", "port": "8443"})
	if err != nil {
		t.Fatalf("verify failure must be a finding, not a method error: %v", err)
	}
	if out["success"] != false || out["handshakeComplete"] != true {
		t.Fatalf("want success=false handshakeComplete=true, got %v", out)
	}
	if out["verifyError"] != "x509: certificate signed by unknown authority" ||
		out["daysUntilExpiry"] != int64(42) {
		t.Fatalf("facts must pass through: %v", out)
	}
}

func TestProbeTLSInsecureSkipVerifyRelaxesVerdict(t *testing.T) {
	f := &fakeProber{tlsRes: TLSResult{
		HandshakeComplete: true, Verified: false, VerifyError: "x509: unknown authority",
		DaysUntilExpiry: 42, LatencyMS: 9,
	}}
	m, _ := Builtin().Get("probe_tls")
	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"target": "internal.svc", "port": "8443", "insecureSkipVerify": "true"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["success"] != true || out["verified"] != false {
		t.Fatalf("skip-verify must relax the verdict but report verified=false: %v", out)
	}
}

func TestProbeTLSInsecureSkipVerifyStillFailsExpired(t *testing.T) {
	f := &fakeProber{tlsRes: TLSResult{
		HandshakeComplete: true, Verified: false, Expired: true, DaysUntilExpiry: -12,
		VerifyError: "x509: certificate has expired or is not yet valid", LatencyMS: 9,
	}}
	m, _ := Builtin().Get("probe_tls")
	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"target": "internal.svc", "port": "8443", "insecureSkipVerify": "true"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["success"] != false || out["expired"] != true || out["daysUntilExpiry"] != int64(-12) {
		t.Fatalf("expiry must fail the verdict even with skip-verify: %v", out)
	}
}

func TestProbeTLSTransportFailureIsAFinding(t *testing.T) {
	f := &fakeProber{tlsRes: TLSResult{LatencyMS: -1, Err: "connection refused"}}
	m, _ := Builtin().Get("probe_tls")
	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"target": "10.0.0.5", "port": "443"})
	if err != nil {
		t.Fatalf("transport failure must be a finding: %v", err)
	}
	if out["success"] != false || out["handshakeComplete"] != false ||
		out["error"] != "connection refused" || out["latencyMs"] != int64(-1) || out["notAfter"] != "" {
		t.Fatalf("bad failure mapping: %v", out)
	}
}

func TestProbeTLSRequestPassthrough(t *testing.T) {
	f := &fakeProber{tlsRes: TLSResult{HandshakeComplete: true, Verified: true}}
	m, _ := Builtin().Get("probe_tls")
	if _, err := m.Run(context.Background(), Deps{Prober: f}, map[string]string{
		"target": "10.0.0.5", "port": "8443", "serverName": "svc.example.com", "timeout": "2s",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := TLSProbeRequest{Target: "10.0.0.5", Port: 8443, ServerName: "svc.example.com", Timeout: 2 * time.Second}
	if f.gotTLS != want {
		t.Fatalf("got %+v, want %+v", f.gotTLS, want)
	}
}

func TestProbeTLSParamErrors(t *testing.T) {
	m, _ := Builtin().Get("probe_tls")
	cases := []map[string]string{
		{"port": "443"},                  // missing target
		{"target": "x", "port": "0"},     // port out of range
		{"target": "x", "port": "70000"}, // port out of range
		{"target": "x", "port": "abc"},   // port not a number
		{"target": "x", "port": "443", "insecureSkipVerify": "notabool"},
		{"target": "x", "port": "443", "timeout": "abc"},
		{"target": "x", "port": "443", "timeout": "0s"},
		{"target": "x", "port": "443", "timeout": "-1s"},
	}
	for i, params := range cases {
		if _, err := m.Run(context.Background(), Deps{Prober: &fakeProber{}}, params); err == nil {
			t.Errorf("case %d (%v): want param error, got nil", i, params)
		}
	}
}
