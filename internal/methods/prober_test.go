package methods

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
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

// startHealthServer boots a plaintext gRPC server with the standard health
// service on a loopback port and returns its host, port, the *health.Server (to
// flip statuses), and a stop func. No external network.
func startHealthServer(t *testing.T) (host string, port int, hs *health.Server, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	hs = health.NewServer() // overall service "" defaults to SERVING
	grpc_health_v1.RegisterHealthServer(s, hs)
	go func() { _ = s.Serve(ln) }()
	h, p := hostPort(t, "http://"+ln.Addr().String())
	return h, p, hs, s.Stop
}

func TestLocalProberGRPCServing(t *testing.T) {
	host, port, _, stop := startHealthServer(t)
	defer stop()

	res := LocalProber{}.ProbeGRPC(context.Background(), GRPCProbeRequest{
		Target: host, Port: port, Timeout: 2 * time.Second,
	})
	if !res.Serving || res.Status != "SERVING" {
		t.Fatalf("got %+v, want Serving/SERVING", res)
	}
	if res.Err != "" || res.LatencyMS < 0 {
		t.Errorf("got err=%q latency=%d", res.Err, res.LatencyMS)
	}
}

func TestLocalProberGRPCNotServing(t *testing.T) {
	host, port, hs, stop := startHealthServer(t)
	defer stop()
	hs.SetServingStatus("cart", grpc_health_v1.HealthCheckResponse_NOT_SERVING)

	res := LocalProber{}.ProbeGRPC(context.Background(), GRPCProbeRequest{
		Target: host, Port: port, Service: "cart", Timeout: 2 * time.Second,
	})
	if res.Serving || res.Status != "NOT_SERVING" {
		t.Errorf("got %+v, want !Serving/NOT_SERVING", res)
	}
}

func TestLocalProberGRPCUnknownService(t *testing.T) {
	host, port, _, stop := startHealthServer(t)
	defer stop()

	// A service the server never registered: Check fails with gRPC NotFound —
	// a finding (empty status + error), NOT a SERVICE_UNKNOWN status.
	res := LocalProber{}.ProbeGRPC(context.Background(), GRPCProbeRequest{
		Target: host, Port: port, Service: "never-registered", Timeout: 2 * time.Second,
	})
	if res.Serving || res.Status != "" || res.Err == "" || res.LatencyMS != -1 {
		t.Errorf("got %+v, want !Serving, empty status, err set, latency -1", res)
	}
}

func TestLocalProberGRPCConnRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port := hostPort(t, "http://"+ln.Addr().String())
	ln.Close() // nothing listening on this port now

	res := LocalProber{}.ProbeGRPC(context.Background(), GRPCProbeRequest{
		Target: "127.0.0.1", Port: port, Timeout: 500 * time.Millisecond,
	})
	if res.Serving || res.Status != "" || res.Err == "" || res.LatencyMS != -1 {
		t.Errorf("got %+v, want failure finding", res)
	}
}

// --- ProbeTLS test helpers ---

// testCA generates a throwaway CA and a pool containing it, so chain
// verification is testable without touching system trust (the LocalProber
// RootCAs seam).
func testCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kato-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return ca, key, pool
}

// testLeafCert creates a server cert for 127.0.0.1 + kato.test. Self-signed
// when ca is nil, else signed by the CA. Validity is caller-controlled so an
// expired cert is one call away.
func testLeafCert(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "kato-test-leaf"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"kato.test"},
	}
	parent, signKey := tmpl, key
	if ca != nil {
		parent, signKey = ca, caKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signKey)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// startTLSListener serves cert on a loopback port, driving the server side of
// each handshake, and returns the port.
func startTLSListener(t *testing.T, cert tls.Certificate) int {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				if tc, ok := c.(*tls.Conn); ok {
					_ = tc.Handshake()
				}
				_ = c.Close()
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestLocalProberTLSVerified(t *testing.T) {
	ca, caKey, pool := testCA(t)
	cert := testLeafCert(t, ca, caKey, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))
	port := startTLSListener(t, cert)

	res := LocalProber{RootCAs: pool}.ProbeTLS(context.Background(), TLSProbeRequest{
		Target: "127.0.0.1", Port: port, Timeout: 3 * time.Second,
	})
	if !res.HandshakeComplete || !res.Verified {
		t.Fatalf("want verified handshake, got %+v", res)
	}
	if res.Expired || res.DaysUntilExpiry < 88 || res.DaysUntilExpiry > 90 || res.NotAfter == "" || res.TLSVersion == "" {
		t.Fatalf("bad cert facts: %+v", res)
	}
	if res.Subject != "kato-test-leaf" || res.Issuer != "kato-test-ca" || res.DNSNames != "kato.test" {
		t.Fatalf("bad identity facts: %+v", res)
	}
	if res.LatencyMS < 0 || res.Err != "" || res.VerifyError != "" {
		t.Fatalf("unexpected error fields: %+v", res)
	}
}

func TestLocalProberTLSSelfSignedIsUnverifiedFinding(t *testing.T) {
	cert := testLeafCert(t, nil, nil, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	port := startTLSListener(t, cert)

	// Zero-value LocalProber = system roots; a throwaway self-signed cert can
	// never verify against them, deterministically on any machine.
	res := LocalProber{}.ProbeTLS(context.Background(), TLSProbeRequest{
		Target: "127.0.0.1", Port: port, Timeout: 3 * time.Second,
	})
	if !res.HandshakeComplete {
		t.Fatalf("capture-then-verify: handshake must complete for a self-signed cert, got %+v", res)
	}
	if res.Verified || res.VerifyError == "" {
		t.Fatalf("want Verified=false with VerifyError set, got %+v", res)
	}
	if res.NotAfter == "" || res.Subject != "kato-test-leaf" {
		t.Fatalf("cert facts must be present despite failed verification: %+v", res)
	}
}

func TestLocalProberTLSExpired(t *testing.T) {
	ca, caKey, pool := testCA(t)
	cert := testLeafCert(t, ca, caKey, time.Now().Add(-72*time.Hour), time.Now().Add(-48*time.Hour))
	port := startTLSListener(t, cert)

	res := LocalProber{RootCAs: pool}.ProbeTLS(context.Background(), TLSProbeRequest{
		Target: "127.0.0.1", Port: port, Timeout: 3 * time.Second,
	})
	if !res.HandshakeComplete {
		t.Fatalf("handshake must tolerate an expired cert (verification is manual), got %+v", res)
	}
	if !res.Expired || res.DaysUntilExpiry >= 0 {
		t.Fatalf("want Expired=true with negative DaysUntilExpiry, got %+v", res)
	}
	if res.Verified {
		t.Fatalf("an expired cert must fail chain verification, got %+v", res)
	}
}

func TestLocalProberTLSNameMismatch(t *testing.T) {
	ca, caKey, pool := testCA(t)
	cert := testLeafCert(t, ca, caKey, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	port := startTLSListener(t, cert)

	res := LocalProber{RootCAs: pool}.ProbeTLS(context.Background(), TLSProbeRequest{
		Target: "127.0.0.1", Port: port, ServerName: "wrong.test", Timeout: 3 * time.Second,
	})
	if !res.HandshakeComplete || res.Verified || res.VerifyError == "" {
		t.Fatalf("want handshake OK but name-mismatch verify failure, got %+v", res)
	}
}

func TestLocalProberTLSNotATLSEndpoint(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close() // immediate close -> client handshake fails fast
		}
	}()

	res := LocalProber{}.ProbeTLS(context.Background(), TLSProbeRequest{
		Target: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Timeout: 2 * time.Second,
	})
	if res.HandshakeComplete || res.Err == "" || res.LatencyMS != -1 {
		t.Fatalf("want handshake failure with Err set and LatencyMS=-1, got %+v", res)
	}
}

func TestLocalProberTLSClosedPort(t *testing.T) {
	// Reserve a port, then close it so the dial is refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	res := LocalProber{}.ProbeTLS(context.Background(), TLSProbeRequest{
		Target: "127.0.0.1", Port: port, Timeout: 2 * time.Second,
	})
	if res.HandshakeComplete || res.Err == "" {
		t.Fatalf("want dial failure, got %+v", res)
	}
}
