package methods

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/icmp"
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

func TestLocalProberDNSResolves(t *testing.T) {
	// "localhost" resolves via the system resolver on every platform to a
	// loopback address, so this needs no external network.
	res := LocalProber{}.ProbeDNS(context.Background(), DNSProbeRequest{
		Name: "localhost", Timeout: 2 * time.Second,
	})
	if !res.Resolved {
		t.Fatalf("expected localhost to resolve, got err=%q", res.Err)
	}
	if len(res.Addresses) == 0 {
		t.Error("expected at least one address")
	}
	if res.LatencyMS < 0 {
		t.Errorf("latency = %d, want >= 0", res.LatencyMS)
	}
}

func TestLocalProberDNSNXDOMAIN(t *testing.T) {
	// ".invalid" is reserved by RFC 6761 to always fail resolution, so this is a
	// stable NXDOMAIN with no external dependency.
	res := LocalProber{}.ProbeDNS(context.Background(), DNSProbeRequest{
		Name: "nonexistent.invalid", Timeout: 2 * time.Second,
	})
	if res.Resolved {
		t.Errorf("expected NXDOMAIN, got addresses %v", res.Addresses)
	}
	if res.Err == "" || res.LatencyMS != -1 {
		t.Errorf("want err set and latency -1, got err=%q latency=%d", res.Err, res.LatencyMS)
	}
}

func TestLocalProberDNSCustomServerUnreachable(t *testing.T) {
	// Point at a closed UDP port on loopback: the custom-resolver Dial path is
	// exercised and the query fails (no server answering), with no real DNS server.
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port := hostPort(t, "http://"+ln.LocalAddr().String())
	ln.Close()

	res := LocalProber{}.ProbeDNS(context.Background(), DNSProbeRequest{
		Name: "example.com", Server: "127.0.0.1", Port: port, Timeout: 200 * time.Millisecond,
	})
	if res.Resolved {
		t.Error("expected failure against an unreachable DNS server")
	}
	if res.Err == "" || res.LatencyMS != -1 {
		t.Errorf("want err set and latency -1, got err=%q latency=%d", res.Err, res.LatencyMS)
	}
}

func TestLocalProberTracerouteLoopback(t *testing.T) {
	// Unprivileged ICMP datagram socket; skip where the node sysctl
	// net.ipv4.ping_group_range does not cover this process's GID (common on
	// locked-down CI) so the suite never goes flaky.
	if c, err := icmp.ListenPacket("udp4", "0.0.0.0"); err != nil {
		t.Skipf("ICMP datagram socket unavailable (ping_group_range?): %v", err)
	} else {
		_ = c.Close()
	}

	res := LocalProber{}.ProbeTraceroute(context.Background(), TracerouteRequest{
		Target: "127.0.0.1", MaxHops: 5, Timeout: 2 * time.Second, ProbesPerHop: 1,
	})
	if res.Err != "" {
		t.Fatalf("unexpected setup error: %s", res.Err)
	}
	if !res.Reached || res.HopCount != 1 {
		t.Fatalf("loopback: reached=%v hopCount=%d hops=%+v", res.Reached, res.HopCount, res.Hops)
	}
	if res.DestinationIP != "127.0.0.1" {
		t.Errorf("destinationIp = %q, want 127.0.0.1", res.DestinationIP)
	}
	if len(res.Hops) != 1 || !res.Hops[0].Reached || res.Hops[0].Address != "127.0.0.1" {
		t.Errorf("hops = %+v", res.Hops)
	}
}

func TestEmbeddedEchoSeq(t *testing.T) {
	// Build a synthetic "original datagram" as quoted in an ICMP Time Exceeded:
	// an IPv4 header of ihlWords*4 bytes, then an 8-byte ICMP echo header whose
	// last 2 bytes are the sequence number.
	build := func(ihlWords int, seq uint16) []byte {
		ihl := ihlWords * 4
		b := make([]byte, ihl+8)
		b[0] = 0x40 | byte(ihlWords) // version 4 (high nibble) + IHL (low nibble)
		binary.BigEndian.PutUint16(b[ihl+6:ihl+8], seq)
		return b
	}

	// Standard 20-byte header (IHL=5).
	if got, ok := embeddedEchoSeq(build(5, 4242)); !ok || got != 4242 {
		t.Errorf("IHL=5: got (%d,%v), want (4242,true)", got, ok)
	}
	// Header with options (IHL=6 -> 24 bytes): offset must track IHL.
	if got, ok := embeddedEchoSeq(build(6, 777)); !ok || got != 777 {
		t.Errorf("IHL=6: got (%d,%v), want (777,true)", got, ok)
	}
	// Truncated: header claims IHL=5 but the buffer is too short for the 8-byte ICMP.
	if _, ok := embeddedEchoSeq([]byte{0x45, 0, 0, 0}); ok {
		t.Error("truncated data: want ok=false")
	}
	// Empty.
	if _, ok := embeddedEchoSeq(nil); ok {
		t.Error("nil data: want ok=false")
	}
	// Invalid IHL (< 5 words = < 20 bytes): degenerate header.
	if _, ok := embeddedEchoSeq([]byte{0x40, 0, 0, 0, 0, 0, 0, 0, 0, 0}); ok {
		t.Error("IHL=0: want ok=false")
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
