# probe_tls + check_pdb Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add two methods — `probe_tls` (active TLS handshake with capture-then-verify certificate inspection) and `check_pdb` (PodDisruptionBudget eviction-budget state) — per the approved spec `docs/superpowers/specs/2026-07-18-kato-probe-tls-check-pdb-design.md`.

**Architecture:** `probe_tls` extends the `Prober` seam in `internal/methods/prober.go` (interface method + `TLSProbeRequest`/`TLSResult` + `LocalProber.ProbeTLS`) and adds a method file that composes the `success` verdict. `check_pdb` is a pure registry addition reading `policy/v1` via `deps.Kube`. No engine/server/CRD/main.go changes.

**Tech Stack:** Go 1.25, stdlib `crypto/tls` + `crypto/x509` (no new dependencies), `k8s.io/client-go` fake clientset for tests.

## Global Constraints

- A failed probe / missing PDB is a **finding** (`success: false` / `exists: false`, step `completed`), never a method error; only invalid params return a non-nil Go error.
- Every scalar output is always present with a defined default (`""`, `0`, `false`, `-1`).
- Outputs use `int64` for `FieldInt` values (kato convention).
- `probe_tls` verdict: `success = handshakeComplete && verified`; with `insecureSkipVerify: "true"`: `success = handshakeComplete && !expired`.
- `daysUntilExpiry` = floor of time-until-`notAfter` in days; **negative when expired**; default `0`.
- `LocalProber` gains `RootCAs *x509.CertPool` (nil = system roots) as a test-only seam; `cmd/kato/main.go` stays untouched (`LocalProber{}` zero value).
- `check_pdb` `blocked = exists && status.disruptionsAllowed == 0`; `conditionReason` = reason of `DisruptionAllowed` condition only when its status is False.
- Method count in docs goes **33 → 35** (the spec's "32 → 34" predates `probe_grpc` landing; Task 4 corrects the spec line).
- Run `make test` and `make lint` before each commit claim.

---

### Task 1: Prober seam — `TLSProbeRequest`, `TLSResult`, `LocalProber.ProbeTLS`

**Files:**
- Modify: `internal/methods/prober.go` (interface at ~line 28, types after `GRPCResult` ~line 129, `LocalProber` struct ~line 133, new method after `ProbeGRPC` ~line 368)
- Modify: `internal/methods/probe_tcp_test.go` (extend `fakeProber`, lines 11-48)
- Test: `internal/methods/prober_test.go` (append helpers + `LocalProber.ProbeTLS` tests)

**Interfaces:**
- Consumes: existing `Prober` interface, `LocalProber` struct.
- Produces (Task 2 relies on these exact names):
  - `Prober` gains `ProbeTLS(ctx context.Context, req TLSProbeRequest) TLSResult`
  - `type TLSProbeRequest struct { Target string; Port int; ServerName string; Timeout time.Duration }`
  - `type TLSResult struct { HandshakeComplete, Verified, Expired bool; VerifyError, NotAfter, Issuer, Subject, DNSNames, TLSVersion, Err string; DaysUntilExpiry, LatencyMS int64 }`
  - `LocalProber` gains field `RootCAs *x509.CertPool`
  - `fakeProber` gains fields `tlsRes TLSResult`, `gotTLS TLSProbeRequest` and method `ProbeTLS`

- [ ] **Step 1: Write the failing LocalProber tests**

Append to `internal/methods/prober_test.go`. Add these imports to the existing import block if not present: `crypto/ecdsa`, `crypto/elliptic`, `crypto/rand`, `crypto/tls`, `crypto/x509`, `crypto/x509/pkix`, `math/big`, `net`, `strconv`, `context`, `testing`, `time`.

```go
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
```

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `go test ./internal/methods/ -run TestLocalProberTLS 2>&1 | head -20`
Expected: compile errors — `undefined: TLSProbeRequest`, `unknown field RootCAs`, `LocalProber...ProbeTLS undefined`.

- [ ] **Step 3: Implement the prober extension**

In `internal/methods/prober.go`:

3a. Add to the `Prober` interface (after the `ProbeGRPC` line):

```go
	ProbeTLS(ctx context.Context, req TLSProbeRequest) TLSResult
```

3b. Add the two types after `GRPCResult` (before the `LocalProber` declaration):

```go
// TLSProbeRequest is a fully-resolved TLS probe (params already parsed/defaulted).
type TLSProbeRequest struct {
	Target     string        // host, IP, or DNS name
	Port       int           // TLS port
	ServerName string        // SNI + hostname to verify; "" = derived from Target
	Timeout    time.Duration // whole-operation bound (dial + handshake)
}

// TLSResult is the outcome of a TLS handshake probe. Verdict composition
// (success from verified/expired per insecureSkipVerify) happens in the method;
// the prober reports raw facts. Capture-then-verify: the handshake itself never
// fails on a bad chain, so cert facts are present even for expired/self-signed
// certs, with Verified/VerifyError carrying the manual verification result.
type TLSResult struct {
	HandshakeComplete bool   // a TLS handshake completed (cert facts are meaningful)
	Verified          bool   // chain + hostname verified against roots
	VerifyError       string // why verification failed; "" when Verified
	Expired           bool   // leaf cert past NotAfter
	DaysUntilExpiry   int64  // floor(days until leaf NotAfter); negative if expired; 0 when no cert
	NotAfter          string // leaf expiry, RFC3339; "" when no cert
	Issuer            string // leaf issuer CN
	Subject           string // leaf subject CN
	DNSNames          string // comma-separated leaf SANs
	TLSVersion        string // negotiated version, e.g. "TLS1.3"
	LatencyMS         int64  // dial + handshake in ms; -1 on failure
	Err               string // transport/handshake failure reason; "" otherwise
}
```

3c. Replace the `LocalProber` struct declaration:

```go
// LocalProber probes from the current process. Network reachability is governed by
// NetworkPolicy, not RBAC.
type LocalProber struct {
	// RootCAs overrides the root pool ProbeTLS verifies against. nil = system
	// roots. Set only by tests (verification against a test CA without touching
	// system trust); production wiring passes the zero value.
	RootCAs *x509.CertPool
}
```

3d. Add `"crypto/x509"` and `"math"` to the import block.

3e. Add the implementation after `ProbeGRPC`:

```go
func (p LocalProber) ProbeTLS(ctx context.Context, req TLSProbeRequest) TLSResult {
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	// SNI: an explicit ServerName wins; otherwise the target hostname (Go omits
	// SNI for IP literals, so leave it empty for an IP target).
	sni := req.ServerName
	if sni == "" && net.ParseIP(req.Target) == nil {
		sni = req.Target
	}
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: req.Timeout},
		// Capture-then-verify: never fail the handshake on a bad chain, so cert
		// facts (expiry, issuer) are reported even for expired/self-signed
		// certs. Verification runs manually below against p.RootCAs.
		Config: &tls.Config{InsecureSkipVerify: true, ServerName: sni}, // #nosec G402 -- deliberate, see comment
	}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(req.Target, strconv.Itoa(req.Port)))
	if err != nil {
		return TLSResult{LatencyMS: -1, Err: err.Error()}
	}
	latency := time.Since(start).Milliseconds()
	state := conn.(*tls.Conn).ConnectionState()
	_ = conn.Close()

	res := TLSResult{
		HandshakeComplete: true,
		LatencyMS:         latency,
		TLSVersion:        tls.VersionName(state.Version),
	}
	if len(state.PeerCertificates) == 0 {
		res.VerifyError = "no peer certificate presented"
		return res
	}
	leaf := state.PeerCertificates[0]
	res.NotAfter = leaf.NotAfter.UTC().Format(time.RFC3339)
	res.DaysUntilExpiry = int64(math.Floor(time.Until(leaf.NotAfter).Hours() / 24))
	res.Expired = time.Now().After(leaf.NotAfter)
	res.Issuer = leaf.Issuer.CommonName
	res.Subject = leaf.Subject.CommonName
	res.DNSNames = strings.Join(leaf.DNSNames, ",")

	verifyName := req.ServerName
	if verifyName == "" {
		verifyName = req.Target // x509 hostname verification handles IP targets too
	}
	inter := x509.NewCertPool()
	for _, c := range state.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         p.RootCAs, // nil = system roots
		Intermediates: inter,
		DNSName:       verifyName,
	}); err != nil {
		res.VerifyError = err.Error()
		return res
	}
	res.Verified = true
	return res
}
```

3f. In `internal/methods/probe_tcp_test.go`, add two fields to `fakeProber`:

```go
	tlsRes     TLSResult
	gotTLS     TLSProbeRequest
```

and the method after `ProbeGRPC`:

```go
func (f *fakeProber) ProbeTLS(_ context.Context, req TLSProbeRequest) TLSResult {
	f.gotTLS = req
	return f.tlsRes
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/methods/ -run TestLocalProberTLS -v`
Expected: all 6 `TestLocalProberTLS*` PASS. Then `make test` — everything else still passes (the widened interface is satisfied by both implementations).

- [ ] **Step 5: Commit**

```bash
git add internal/methods/prober.go internal/methods/prober_test.go internal/methods/probe_tcp_test.go
git commit -m "feat: add ProbeTLS to the Prober seam (capture-then-verify)"
```

---

### Task 2: `probe_tls` method

**Files:**
- Create: `internal/methods/probe_tls.go`
- Test: `internal/methods/probe_tls_test.go`

**Interfaces:**
- Consumes (from Task 1): `TLSProbeRequest`, `TLSResult`, `deps.Prober.ProbeTLS`, `fakeProber.tlsRes`/`gotTLS`; package helpers `parsePort(string) (int, error)`, `parseProbeTimeout(string) (time.Duration, error)`.
- Produces: registered method `probe_tls` with outputs `success, handshakeComplete, verified, expired, daysUntilExpiry, notAfter, issuer, subject, dnsNames, tlsVersion, verifyError, latencyMs, error`.

- [ ] **Step 1: Write the failing tests**

Create `internal/methods/probe_tls_test.go`:

```go
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
		VerifyError: "x509: certificate signed by unknown authority",
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
		{"port": "443"},                                // missing target
		{"target": "x", "port": "0"},                   // port out of range
		{"target": "x", "port": "70000"},               // port out of range
		{"target": "x", "port": "abc"},                 // port not a number
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/methods/ -run TestProbeTLS -v 2>&1 | head -5`
Expected: FAIL — `probe_tls not registered`.

- [ ] **Step 3: Implement the method**

Create `internal/methods/probe_tls.go`:

```go
package methods

import (
	"context"
	"fmt"
	"strconv"
)

type probeTLS struct{}

func (probeTLS) Name() string { return "probe_tls" }
func (probeTLS) Description() string {
	return "Active TLS handshake check with certificate chain verification and expiry facts"
}

func (probeTLS) Params() []Param {
	return []Param{
		{Name: "target", Required: true, Description: "host, IP, or DNS name"},
		{Name: "port", Required: true, Description: "TLS port (1-65535)"},
		{Name: "serverName", Description: "SNI + hostname to verify; empty = derived from target"},
		{Name: "insecureSkipVerify", Description: `"true" to exclude chain/name verification from success (expiry still counts; default "false")`},
		{Name: "timeout", Description: `whole-operation timeout as a Go duration (default "5s")`},
	}
}

func (probeTLS) OutputFields() []OutputField {
	return []OutputField{
		{Name: "success", Type: FieldBool, Description: "handshakeComplete && verified (with insecureSkipVerify: handshakeComplete && !expired)"},
		{Name: "handshakeComplete", Type: FieldBool, Description: "a TLS handshake completed (cert facts are meaningful)"},
		{Name: "verified", Type: FieldBool, Description: "chain + hostname verified against system roots"},
		{Name: "expired", Type: FieldBool, Description: "leaf cert past notAfter"},
		{Name: "daysUntilExpiry", Type: FieldInt, Description: "days until leaf notAfter (floor); negative if expired; gate on handshakeComplete"},
		{Name: "notAfter", Type: FieldString, Description: `leaf expiry, RFC3339; "" if no cert obtained`},
		{Name: "issuer", Type: FieldString, Description: "leaf issuer CN"},
		{Name: "subject", Type: FieldString, Description: "leaf subject CN"},
		{Name: "dnsNames", Type: FieldString, Description: "comma-separated leaf SANs"},
		{Name: "tlsVersion", Type: FieldString, Description: `negotiated version, e.g. "TLS1.3"`},
		{Name: "verifyError", Type: FieldString, Description: `why chain verification failed; "" when verified`},
		{Name: "latencyMs", Type: FieldInt, Description: "dial + handshake in ms; -1 on failure"},
		{Name: "error", Type: FieldString, Description: `transport/handshake failure reason; "" otherwise`},
	}
}

func (probeTLS) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	if params["target"] == "" {
		return nil, fmt.Errorf("param target: required")
	}
	port, err := parsePort(params["port"])
	if err != nil {
		return nil, err
	}
	timeout, err := parseProbeTimeout(params["timeout"])
	if err != nil {
		return nil, err
	}
	insecure := false
	if v := params["insecureSkipVerify"]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("param insecureSkipVerify: %w", err)
		}
		insecure = b
	}
	res := deps.Prober.ProbeTLS(ctx, TLSProbeRequest{
		Target:     params["target"],
		Port:       port,
		ServerName: params["serverName"],
		Timeout:    timeout,
	})
	// Verdict: "TLS is healthy here". insecureSkipVerify excludes chain/name
	// verification (in-cluster internal CAs), but expiry still fails it —
	// expiry monitoring is this probe's primary job.
	success := res.HandshakeComplete && res.Verified
	if insecure {
		success = res.HandshakeComplete && !res.Expired
	}
	return Outputs{
		"success":           success,
		"handshakeComplete": res.HandshakeComplete,
		"verified":          res.Verified,
		"expired":           res.Expired,
		"daysUntilExpiry":   res.DaysUntilExpiry,
		"notAfter":          res.NotAfter,
		"issuer":            res.Issuer,
		"subject":           res.Subject,
		"dnsNames":          res.DNSNames,
		"tlsVersion":        res.TLSVersion,
		"verifyError":       res.VerifyError,
		"latencyMs":         res.LatencyMS,
		"error":             res.Err,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(probeTLS{}) }) }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/methods/ -run TestProbeTLS -v`
Expected: all 7 `TestProbeTLS*` PASS. Then `make test` — full suite green.

- [ ] **Step 5: Commit**

```bash
git add internal/methods/probe_tls.go internal/methods/probe_tls_test.go
git commit -m "feat: create method probe_tls"
```

---

### Task 3: `check_pdb` method + RBAC

**Files:**
- Create: `internal/methods/pdb.go`
- Test: `internal/methods/pdb_test.go`
- Modify: `charts/kato/templates/rbac.yaml` (add `policy` rule after the `autoscaling` rule)

**Interfaces:**
- Consumes: `deps.Kube.PolicyV1()`, `renderKVMap(map[string]string) string` from `render.go`.
- Produces: registered method `check_pdb` with outputs `exists, minAvailable, maxUnavailable, selector, expectedPods, currentHealthy, desiredHealthy, disruptionsAllowed, blocked, conditionReason`.

- [ ] **Step 1: Write the failing tests**

Create `internal/methods/pdb_test.go`:

```go
package methods

import (
	"context"
	"testing"

	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

func TestCheckPDBHealthy(t *testing.T) {
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: ptr.To(intstr.FromString("50%")),
			Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
		},
		Status: policyv1.PodDisruptionBudgetStatus{
			ExpectedPods: 4, CurrentHealthy: 4, DesiredHealthy: 2, DisruptionsAllowed: 2,
			Conditions: []metav1.Condition{{
				Type: policyv1.DisruptionAllowedCondition, Status: metav1.ConditionTrue,
				Reason: "SufficientPods",
			}},
		},
	}
	client := fake.NewSimpleClientset(pdb)
	m, ok := Builtin().Get("check_pdb")
	if !ok {
		t.Fatal("check_pdb not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "prod", "name": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := Outputs{
		"exists": true, "minAvailable": "50%", "maxUnavailable": "", "selector": "app=api",
		"expectedPods": int64(4), "currentHealthy": int64(4), "desiredHealthy": int64(2),
		"disruptionsAllowed": int64(2), "blocked": false, "conditionReason": "",
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("out[%q] = %v, want %v", k, out[k], v)
		}
	}
}

func TestCheckPDBBlocked(t *testing.T) {
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: ptr.To(intstr.FromInt32(1)),
		},
		Status: policyv1.PodDisruptionBudgetStatus{
			ExpectedPods: 3, CurrentHealthy: 1, DesiredHealthy: 2, DisruptionsAllowed: 0,
			Conditions: []metav1.Condition{{
				Type: policyv1.DisruptionAllowedCondition, Status: metav1.ConditionFalse,
				Reason: "InsufficientPods",
			}},
		},
	}
	client := fake.NewSimpleClientset(pdb)
	m, _ := Builtin().Get("check_pdb")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "prod", "name": "db"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["blocked"] != true || out["disruptionsAllowed"] != int64(0) {
		t.Fatalf("want blocked=true at zero budget, got %v", out)
	}
	if out["conditionReason"] != "InsufficientPods" || out["maxUnavailable"] != "1" {
		t.Fatalf("bad condition/budget rendering: %v", out)
	}
}

func TestCheckPDBMissingIsAFinding(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_pdb")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "prod", "name": "nope"})
	if err != nil {
		t.Fatalf("missing PDB must be a finding, not an error: %v", err)
	}
	want := Outputs{
		"exists": false, "minAvailable": "", "maxUnavailable": "", "selector": "",
		"expectedPods": int64(0), "currentHealthy": int64(0), "desiredHealthy": int64(0),
		"disruptionsAllowed": int64(0), "blocked": false, "conditionReason": "",
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("out[%q] = %v, want %v", k, out[k], v)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/methods/ -run TestCheckPDB -v 2>&1 | head -5`
Expected: FAIL — `check_pdb not registered`.

- [ ] **Step 3: Implement the method**

Create `internal/methods/pdb.go`:

```go
package methods

import (
	"context"
	"fmt"

	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkPDB struct{}

func (checkPDB) Name() string { return "check_pdb" }
func (checkPDB) Description() string {
	return "PodDisruptionBudget state — does it currently permit voluntary disruption (evictions/drains)?"
}

func (checkPDB) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "PDB namespace"},
		{Name: "name", Required: true, Description: "PDB name"},
	}
}

func (checkPDB) OutputFields() []OutputField {
	return []OutputField{
		{Name: "exists", Type: FieldBool, Description: "PDB exists"},
		{Name: "minAvailable", Type: FieldString, Description: `spec.minAvailable (int-or-percent, e.g. "2", "50%"); "" if unset`},
		{Name: "maxUnavailable", Type: FieldString, Description: `spec.maxUnavailable (int-or-percent); "" if unset`},
		{Name: "selector", Type: FieldString, Description: `spec.selector matchLabels as "k=v, k=v"; "" if none`},
		{Name: "expectedPods", Type: FieldInt, Description: "status.expectedPods"},
		{Name: "currentHealthy", Type: FieldInt, Description: "status.currentHealthy"},
		{Name: "desiredHealthy", Type: FieldInt, Description: "status.desiredHealthy"},
		{Name: "disruptionsAllowed", Type: FieldInt, Description: "status.disruptionsAllowed"},
		{Name: "blocked", Type: FieldBool, Description: "disruptionsAllowed == 0 — no voluntary disruption (eviction/drain) possible right now; a PDB gates evictions, not rolling updates"},
		{Name: "conditionReason", Type: FieldString, Description: `reason of the DisruptionAllowed condition when False (e.g. "InsufficientPods"), "" otherwise`},
	}
}

func (checkPDB) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	pdb, err := deps.Kube.PolicyV1().PodDisruptionBudgets(params["namespace"]).
		Get(ctx, params["name"], metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Existence is itself a finding — no PDB means drains evict freely.
		return Outputs{
			"exists": false, "minAvailable": "", "maxUnavailable": "", "selector": "",
			"expectedPods": int64(0), "currentHealthy": int64(0), "desiredHealthy": int64(0),
			"disruptionsAllowed": int64(0), "blocked": false, "conditionReason": "",
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pdb %s/%s: %w", params["namespace"], params["name"], err)
	}

	minA, maxU := "", ""
	if pdb.Spec.MinAvailable != nil {
		minA = pdb.Spec.MinAvailable.String()
	}
	if pdb.Spec.MaxUnavailable != nil {
		maxU = pdb.Spec.MaxUnavailable.String()
	}
	selector := ""
	if pdb.Spec.Selector != nil {
		selector = renderKVMap(pdb.Spec.Selector.MatchLabels)
	}
	reason := ""
	for _, c := range pdb.Status.Conditions {
		if c.Type == policyv1.DisruptionAllowedCondition && c.Status == metav1.ConditionFalse {
			reason = c.Reason
		}
	}
	return Outputs{
		"exists":             true,
		"minAvailable":       minA,
		"maxUnavailable":     maxU,
		"selector":           selector,
		"expectedPods":       int64(pdb.Status.ExpectedPods),
		"currentHealthy":     int64(pdb.Status.CurrentHealthy),
		"desiredHealthy":     int64(pdb.Status.DesiredHealthy),
		"disruptionsAllowed": int64(pdb.Status.DisruptionsAllowed),
		"blocked":            pdb.Status.DisruptionsAllowed == 0,
		"conditionReason":    reason,
	}, nil
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkPDB{}) })
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/methods/ -run TestCheckPDB -v`
Expected: all 3 PASS. Then `make test`.

- [ ] **Step 5: Add the RBAC rule**

In `charts/kato/templates/rbac.yaml`, insert after the `autoscaling` block (`- apiGroups: ["autoscaling"] ...`):

```yaml
  - apiGroups: ["policy"]
    resources: [poddisruptionbudgets]
    verbs: [get, list, watch]
```

Verify: `helm template charts/kato | grep -A2 policy` shows the rule.

- [ ] **Step 6: Commit**

```bash
git add internal/methods/pdb.go internal/methods/pdb_test.go charts/kato/templates/rbac.yaml
git commit -m "feat: create method check_pdb"
```

---

### Task 4: Example UseCases, docs, counts

**Files:**
- Create: `examples/usecases/tls-certificate-check.yaml`
- Create: `examples/usecases/deployment-disruption-check.yaml`
- Modify: `docs/METHOD.md` (index rows at lines ~52 and ~69; `check_pdb` section after `check_hpa` ~line 539; `probe_tls` section after `probe_grpc` at end of file)
- Modify: `charts/kato/README.md.gotmpl` (line 103 count; line 110 Workloads row; line 118 Active probes row; line 121 ready-made sentence)
- Modify: `docs/superpowers/specs/2026-07-18-kato-probe-tls-check-pdb-design.md` (stale count line)
- Regenerate: `README.md`, `charts/kato/README.md` via `make readme`

**Interfaces:**
- Consumes: method names/outputs exactly as registered in Tasks 2-3.
- Produces: user-facing docs; no code.

- [ ] **Step 1: Create `examples/usecases/tls-certificate-check.yaml`**

```yaml
# TLS certificate check: is TLS healthy at target:port, and when does the cert expire?
# Layered, from kato's own pod: DNS (does the name resolve?) -> TCP (is the port open?)
# -> TLS (handshake + chain verification + expiry facts), run only when the port is open.
#
# target is an IP or a DNS name (Service, FQDN, or external host); port is the TLS port.
#
#   curl -s -X POST localhost:8080/api/v1/usecases/tls-certificate-check/run \
#     -d '{"inputs":{"target":"example.com","port":"443"}}' | jq
#
apiVersion: kato.zufardhiyaulhaq.com/v1alpha1
kind: UseCase
metadata:
  name: tls-certificate-check
spec:
  description: "Is TLS healthy at target:port (chain valid, cert not expiring)? DNS + TCP + TLS"
  inputs:
    - name: target
      required: true
    - name: port
      required: true
  steps:
    # Name resolution first: an NXDOMAIN/timeout here is a different root cause
    # than a TLS problem, and it explains a TCP failure below.
    - name: dns
      method: probe_dns
      with:
        name: $(inputs.target)
      summaryFilter: [success, addresses, recordCount, latencyMs, error]

    # L4 reachability: a refused/timed-out connect is a finding (success=false),
    # so the TLS step below can gate on it.
    - name: tcp
      method: probe_tcp
      with:
        target: $(inputs.target)
        port: $(inputs.port)

    # TLS handshake + capture-then-verify: cert facts (expiry, issuer, SANs) are
    # reported even when the chain does not verify (expired / self-signed / name
    # mismatch), with verifyError naming the reason.
    - name: tls
      when: $(steps.tcp.success)
      method: probe_tls
      with:
        target: $(inputs.target)
        port: $(inputs.port)
  summary:
    prompt: |
      Is TLS healthy at $(inputs.target):$(inputs.port)? Check in order:

      1. dns.success false -> the name doesn't resolve. Root cause is DNS; it also
         explains any TCP failure. Stop here.
      2. tcp.success false -> port not reachable (refused = rejected, timeout = no
         answer); TLS was skipped. Reachability, not certificates, is the problem.
      3. tls.handshakeComplete false -> port open but no TLS handshake: not a TLS
         endpoint or wrong port. Read tls.error.
      4. tls.verified false -> handshake OK but the chain does not verify. Read
         tls.verifyError (expired / unknown authority / name mismatch) and report
         which cert was actually presented (subject, issuer, notAfter, dnsNames).
      5. tls.success true -> healthy. Report daysUntilExpiry and warn if it is
         below 30 days (name the renewal as the fix).

      Give a one-line verdict + the evidence + the fix.
```

- [ ] **Step 2: Create `examples/usecases/deployment-disruption-check.yaml`**

```yaml
# Deployment disruption check: can this deployment tolerate a node drain right now?
# check_deployment_status (is it healthy?) + check_pdb (does its budget currently
# permit evictions?). A PDB gates evictions (kubectl drain, node upgrades, the
# descheduler) — NOT rolling updates — so blocked=true explains a hanging drain.
#
#   curl -s -X POST localhost:8080/api/v1/usecases/deployment-disruption-check/run \
#     -d '{"inputs":{"namespace":"prod","deployment":"api","pdb":"api"}}' | jq
#
apiVersion: kato.zufardhiyaulhaq.com/v1alpha1
kind: UseCase
metadata:
  name: deployment-disruption-check
spec:
  description: "Can this deployment tolerate a node drain? Deployment health + PDB eviction budget"
  inputs:
    - name: namespace
      required: true
    - name: deployment
      required: true
    - name: pdb
      required: true
  steps:
    - name: deployment
      method: check_deployment_status
      with:
        namespace: $(inputs.namespace)
        name: $(inputs.deployment)

    - name: pdb
      method: check_pdb
      with:
        namespace: $(inputs.namespace)
        name: $(inputs.pdb)
  summary:
    prompt: |
      Can deployment $(inputs.namespace)/$(inputs.deployment) tolerate a node drain
      right now? Remember: a PDB gates evictions (drains), not rolling updates.

      1. pdb.exists false -> no PDB protects this workload: drains will evict its
         pods freely. If the workload matters, recommend adding a PDB.
      2. pdb.blocked true with conditionReason InsufficientPods -> the budget
         permits zero evictions because only currentHealthy of desiredHealthy
         pods are healthy. Cross-check deployment.readyReplicas; the fix is to
         restore unready pods, not to touch the PDB.
      3. pdb.blocked true while the deployment is healthy -> the budget itself is
         too tight (e.g. maxUnavailable 0, or minAvailable equal to the replica
         count). Name the offending setting using minAvailable/maxUnavailable vs
         expectedPods.
      4. Otherwise (disruptionsAllowed > 0) -> a drain can proceed; say how many
         evictions the budget currently allows.

      Give a one-line verdict + the evidence + the fix.
```

- [ ] **Step 3: Update `docs/METHOD.md`**

3a. Index: after the `check_hpa` row (line ~52), add:

```markdown
| [`check_pdb`](#check_pdb) | PodDisruptionBudget state — does it currently permit voluntary disruption (evictions/drains)? |
```

3b. Index: after the `probe_grpc` row (line ~69), add:

```markdown
| [`probe_tls`](#probe_tls) | Active TLS handshake — is the chain valid and when does the certificate expire? |
```

3c. After the `check_hpa` section (ends ~line 538, before the `## Networking` heading), add:

```markdown
### `check_pdb`

PodDisruptionBudget state. A missing PDB is reported as `exists: false` (not an
error), so existence is itself a usable finding ("no PDB — drains evict freely").

> A PDB gates **evictions** — `kubectl drain`, node-pool upgrades, the
> descheduler — not rolling updates. `blocked: true` explains "the drain hangs",
> not "the rollout hangs".

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | PDB namespace |
| `name` | yes | PDB name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `exists` | bool | PDB exists |
| `minAvailable` | string | `spec.minAvailable` (int-or-percent, e.g. `2`, `50%`), `""` if unset |
| `maxUnavailable` | string | `spec.maxUnavailable` (int-or-percent), `""` if unset |
| `selector` | string | `spec.selector` matchLabels, `""` if none |
| `expectedPods` | int | `status.expectedPods` |
| `currentHealthy` | int | `status.currentHealthy` |
| `desiredHealthy` | int | `status.desiredHealthy` |
| `disruptionsAllowed` | int | `status.disruptionsAllowed` |
| `blocked` | bool | `disruptionsAllowed == 0` — no voluntary disruption possible right now |
| `conditionReason` | string | reason of the `DisruptionAllowed` condition when False (e.g. `InsufficientPods`), `""` otherwise |

`blocked` + `conditionReason: InsufficientPods` means "fix the unready pods before
draining"; `blocked` with a healthy workload means the budget itself is too tight
(compare `minAvailable`/`maxUnavailable` against `expectedPods`).

---
```

3d. At the end of the file (after the `probe_grpc` section), add:

```markdown
### `probe_tls`

Active TLS handshake from kato's pod with **capture-then-verify** semantics: the
handshake itself never fails on a bad chain (verification is disabled at handshake
time), so certificate facts — expiry, issuer, subject, SANs — are reported even for
an expired, self-signed, or name-mismatched certificate; chain + hostname
verification then runs manually and its outcome is reported separately. This closes
the certificate-expiry outage class that `probe_http` cannot see (it either fails
opaquely on a bad cert or ignores it entirely).

`success` means "TLS is healthy here": `handshakeComplete && verified`. With
`insecureSkipVerify: "true"` (for services using an internal CA that can never
verify against system roots) the verdict relaxes to `handshakeComplete && !expired`
— chain verification is excluded from the verdict but still reported via
`verified`/`verifyError`, and an expired certificate still fails it. There is no
expiry-threshold param: gate with CEL, e.g.
`when: $(steps.tls.daysUntilExpiry) < 30`.

A failed handshake, invalid chain, or expired certificate is a finding
(`success: false`), never a method error. Runs from kato's pod; reachability is
governed by NetworkPolicy (no Kubernetes RBAC needed). Covers any TLS endpoint,
not just HTTPS (databases, message brokers, gRPC).

**Inputs**

| Name | Required | Description |
|---|---|---|
| `target` | yes | host, IP, or DNS name |
| `port` | yes | TLS port (1–65535) |
| `serverName` | no | SNI + hostname to verify; empty = derived from `target` |
| `insecureSkipVerify` | no | `"true"` to exclude chain/name verification from `success` (expiry still counts; default `"false"`) |
| `timeout` | no | whole-operation timeout as a Go duration (default `5s`) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `success` | bool | `handshakeComplete && verified`; with `insecureSkipVerify`: `handshakeComplete && !expired` |
| `handshakeComplete` | bool | a TLS handshake completed (cert facts are meaningful) |
| `verified` | bool | chain + hostname verified against system roots |
| `expired` | bool | leaf cert past `notAfter` |
| `daysUntilExpiry` | int | days until leaf `notAfter` (floor); **negative if expired**; gate on `handshakeComplete` |
| `notAfter` | string | leaf expiry, RFC3339; `""` if no cert obtained |
| `issuer` | string | leaf issuer CN |
| `subject` | string | leaf subject CN |
| `dnsNames` | string | comma-separated leaf SANs |
| `tlsVersion` | string | negotiated version, e.g. `TLS1.3` |
| `verifyError` | string | why chain verification failed; `""` when verified |
| `latencyMs` | int | dial + handshake in ms; `-1` on failure |
| `error` | string | transport/handshake failure reason; `""` otherwise |

Pair with `probe_tcp`: gate `probe_tls` on `$(steps.<tcp>.success)` so the handshake
runs only when the port is open, separating "unreachable" from "bad certificate".
`handshakeComplete` is the reachable-and-speaks-TLS signal; `verified`/`expired`
say what is wrong with the certificate; `daysUntilExpiry` powers renewal warnings.

---
```

- [ ] **Step 4: Update `charts/kato/README.md.gotmpl`**

4a. Line 103: change `**33 read-only checks**` → `**35 read-only checks**`.

4b. Line 110 (Workloads row): append `` `check_pdb` `` after `` `check_hpa` ``:

```markdown
| Workloads | `check_deployment_status`, `describe_deployment`, `check_replicaset`, `check_daemonset_status`, `describe_daemonset`, `check_statefulset_status`, `describe_statefulset`, `check_hpa`, `check_pdb` |
```

4c. Line 118 (Active probes row): append `` `probe_tls` `` after `` `probe_grpc` ``:

```markdown
| Active probes | `probe_tcp`, `probe_http`, `probe_dns`, `probe_traceroute`, `probe_grpc`, `probe_tls` (run from kato's pod; reachability governed by NetworkPolicy) |
```

4d. Line 121: change `and TCP/HTTP/gRPC connectivity checks` → `TCP/HTTP/gRPC connectivity checks, TLS certificate and deployment-disruption checks`.

- [ ] **Step 5: Correct the stale count in the spec**

In `docs/superpowers/specs/2026-07-18-kato-probe-tls-check-pdb-design.md`, replace the line

```
- `charts/kato/README.md.gotmpl` — "32 → **34** read-only checks"; add `check_pdb` to the
```

with

```
- `charts/kato/README.md.gotmpl` — "33 → **35** read-only checks" (`probe_grpc` landed after this spec was drafted); add `check_pdb` to the
```

- [ ] **Step 6: Regenerate READMEs and run the full suite**

Run: `make readme` (regenerates `README.md` + `charts/kato/README.md` via helm-docs)
Run: `make test && make lint`
Expected: both green; `git diff README.md` shows only the intended count/row changes.

- [ ] **Step 7: Commit**

```bash
git add examples/usecases/tls-certificate-check.yaml examples/usecases/deployment-disruption-check.yaml \
  docs/METHOD.md charts/kato/README.md.gotmpl README.md charts/kato/README.md \
  docs/superpowers/specs/2026-07-18-kato-probe-tls-check-pdb-design.md
git commit -m "docs: add probe_tls and check_pdb examples and docs"
```
