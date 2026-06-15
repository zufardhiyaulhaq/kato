package methods

import (
	"context"
	"testing"
)

func TestProbeHTTPSuccessAndDefaults(t *testing.T) {
	f := &fakeProber{http: HTTPResult{StatusCode: 200, StatusMatched: true, BodyMatched: true, LatencyMS: 8}}
	m, ok := Builtin().Get("probe_http")
	if !ok {
		t.Fatal("probe_http not registered")
	}
	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"target": "api.svc", "port": "8080"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["success"] != true || out["statusCode"] != int64(200) ||
		out["statusMatched"] != true || out["bodyMatched"] != true || out["latencyMs"] != int64(8) {
		t.Errorf("outputs = %#v", out)
	}
	if f.gotHTTP.Scheme != "http" || f.gotHTTP.Path != "/" ||
		f.gotHTTP.ExpectStatus != 200 || f.gotHTTP.Timeout.String() != "5s" {
		t.Errorf("defaults wrong: %+v", f.gotHTTP)
	}
}

func TestProbeHTTPStatusMismatchFailsSuccess(t *testing.T) {
	f := &fakeProber{http: HTTPResult{StatusCode: 503, StatusMatched: false, BodyMatched: true, LatencyMS: 5}}
	m, _ := Builtin().Get("probe_http")
	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"target": "api.svc", "port": "8080"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["success"] != false || out["statusCode"] != int64(503) {
		t.Errorf("outputs = %#v", out)
	}
}

func TestProbeHTTPParamPassThrough(t *testing.T) {
	f := &fakeProber{http: HTTPResult{StatusCode: 204, StatusMatched: true, BodyMatched: true}}
	m, _ := Builtin().Get("probe_http")
	// path is passed through verbatim ("healthz", no leading slash); the prober normalizes it.
	_, err := m.Run(context.Background(), Deps{Prober: f}, map[string]string{
		"target": "api.svc", "port": "8443", "scheme": "https", "path": "healthz",
		"expectStatus": "204", "expectBodyContains": "ok", "insecureSkipVerify": "true", "timeout": "3s",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	g := f.gotHTTP
	if g.Target != "api.svc" || g.Scheme != "https" || g.Port != 8443 || g.Path != "healthz" || g.ExpectStatus != 204 ||
		g.ExpectBodyContains != "ok" || !g.InsecureSkipVerify || g.Timeout.String() != "3s" {
		t.Errorf("pass-through wrong: %+v", g)
	}
}

func TestProbeHTTPParamErrors(t *testing.T) {
	m, _ := Builtin().Get("probe_http")
	cases := map[string]map[string]string{
		"empty target":    {"port": "80"},
		"bad scheme":      {"target": "x", "port": "80", "scheme": "ftp"},
		"bad status":      {"target": "x", "port": "80", "expectStatus": "two"},
		"bad insecure":    {"target": "x", "port": "80", "insecureSkipVerify": "maybe"},
		"bad port":        {"target": "x", "port": "0"},
		"zero timeout":    {"target": "x", "port": "80", "timeout": "0s"},
		"status too low":  {"target": "x", "port": "80", "expectStatus": "0"},
		"status too high": {"target": "x", "port": "80", "expectStatus": "700"},
	}
	for name, params := range cases {
		if _, err := m.Run(context.Background(), Deps{Prober: &fakeProber{}}, params); err == nil {
			t.Errorf("%s: expected param error, got nil", name)
		}
	}
}
