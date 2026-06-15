package methods

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func hostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %s: %v", rawURL, err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("port from %s: %v", rawURL, err)
	}
	return u.Hostname(), p
}

func TestLocalProberTCPConnect(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port := hostPort(t, "http://"+ln.Addr().String())

	res := LocalProber{}.ProbeTCP(context.Background(), "127.0.0.1", port, 2*time.Second)
	if !res.Success {
		t.Errorf("expected success, got err=%q", res.Err)
	}
	if res.LatencyMS < 0 {
		t.Errorf("latency = %d, want >= 0", res.LatencyMS)
	}
}

func TestLocalProberTCPRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port := hostPort(t, "http://"+ln.Addr().String())
	ln.Close()
	// The just-closed ephemeral port is extremely unlikely to be reused before the
	// dial below, so the connect reliably fails with "connection refused".

	res := LocalProber{}.ProbeTCP(context.Background(), "127.0.0.1", port, 2*time.Second)
	if res.Success {
		t.Error("expected failure on closed port")
	}
	if res.Err == "" || res.LatencyMS != -1 {
		t.Errorf("want err set and latency -1, got err=%q latency=%d", res.Err, res.LatencyMS)
	}
}

func TestLocalProberHTTPStatusAndBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("pong-ok"))
	}))
	defer ts.Close()
	host, port := hostPort(t, ts.URL)

	res := LocalProber{}.ProbeHTTP(context.Background(), HTTPProbeRequest{
		Scheme: "http", Target: host, Port: port, Path: "/",
		ExpectStatus: 200, ExpectBodyContains: "pong", Timeout: 2 * time.Second,
	})
	if res.StatusCode != 200 || !res.StatusMatched || !res.BodyMatched {
		t.Errorf("got %+v, want 200/matched/matched", res)
	}
	if res.Err != "" || res.LatencyMS < 0 {
		t.Errorf("got err=%q latency=%d", res.Err, res.LatencyMS)
	}
}

func TestLocalProberHTTPStatusMismatchAndBodyMiss(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("unavailable"))
	}))
	defer ts.Close()
	host, port := hostPort(t, ts.URL)

	res := LocalProber{}.ProbeHTTP(context.Background(), HTTPProbeRequest{
		Scheme: "http", Target: host, Port: port, Path: "/",
		ExpectStatus: 200, ExpectBodyContains: "pong", Timeout: 2 * time.Second,
	})
	if res.StatusCode != 503 || res.StatusMatched || res.BodyMatched {
		t.Errorf("got %+v, want 503/false/false", res)
	}
}

func TestLocalProberHTTPTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer ts.Close()
	host, port := hostPort(t, ts.URL)

	// 20ms client timeout vs a 200ms server sleep — a 10x margin keeps this stable on slow CI.
	res := LocalProber{}.ProbeHTTP(context.Background(), HTTPProbeRequest{
		Scheme: "http", Target: host, Port: port, Path: "/",
		ExpectStatus: 200, Timeout: 20 * time.Millisecond,
	})
	if res.Err == "" || res.StatusCode != 0 || res.LatencyMS != -1 {
		t.Errorf("want timeout err, got %+v", res)
	}
}

func TestLocalProberHTTPSInsecure(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()
	host, port := hostPort(t, ts.URL)

	secure := LocalProber{}.ProbeHTTP(context.Background(), HTTPProbeRequest{
		Scheme: "https", Target: host, Port: port, Path: "/", ExpectStatus: 200, Timeout: 2 * time.Second,
	})
	if secure.Err == "" {
		t.Error("expected TLS verification error without insecureSkipVerify")
	}
	insecure := LocalProber{}.ProbeHTTP(context.Background(), HTTPProbeRequest{
		Scheme: "https", Target: host, Port: port, Path: "/", ExpectStatus: 200,
		InsecureSkipVerify: true, Timeout: 2 * time.Second,
	})
	if insecure.Err != "" || !insecure.StatusMatched {
		t.Errorf("insecure probe got %+v", insecure)
	}
}

func TestLocalProberHTTPNoBodyCheck(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("anything"))
	}))
	defer ts.Close()
	host, port := hostPort(t, ts.URL)

	// Empty ExpectBodyContains skips the body check -> BodyMatched is true.
	res := LocalProber{}.ProbeHTTP(context.Background(), HTTPProbeRequest{
		Scheme: "http", Target: host, Port: port, Path: "/",
		ExpectStatus: 200, ExpectBodyContains: "", Timeout: 2 * time.Second,
	})
	if !res.StatusMatched || !res.BodyMatched {
		t.Errorf("got %+v, want statusMatched and bodyMatched true", res)
	}
}
