# kato Network Probe Methods Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add two active network-probe methods — `probe_tcp` (TCP connect check) and `probe_http` (HTTP(S) request with status/body assertions) — so a UseCase can verify black-box that a service, DB, or host is reachable and serving.

**Architecture:** A `Prober` interface (injected via `methods.Deps`, like `Deps.Metrics`) abstracts where probes run. The only backend now is `LocalProber` (in-process `net.DialTimeout` + `net/http`), wired in `cmd/kato`. The two methods parse params, call the matching `Prober` method, and map the result struct to flat `Outputs`. A future `RemoteProber` (centralized multi-cluster) can implement the same interface without changing the methods or any UseCase.

**Tech Stack:** Go, `net`/`net/http`/`crypto/tls`, `httptest` + `net.Listen` (LocalProber tests), fake `Prober` (method tests). Spec: `docs/superpowers/specs/2026-06-14-kato-network-probe-design.md`.

**Standing constraint:** DO NOT COMMIT. Leave all changes in the working tree. Every task ends with a passing test run, NOT a `git commit`/`git add`.

---

## File Structure

| File | Change | Responsibility |
|------|--------|----------------|
| `internal/methods/prober.go` | create | `Prober` interface, `TCPResult`/`HTTPProbeRequest`/`HTTPResult` types, `LocalProber` impl |
| `internal/methods/prober_test.go` | create | `LocalProber` tests (real `httptest`/`net.Listen` I/O) |
| `internal/methods/method.go` | modify | add `Prober Prober` field to `Deps` |
| `internal/methods/probe_tcp.go` | create | `probe_tcp` method + shared `parsePort`/`parseProbeTimeout` helpers |
| `internal/methods/probe_tcp_test.go` | create | `probe_tcp` tests (fake `Prober`) |
| `internal/methods/probe_http.go` | create | `probe_http` method |
| `internal/methods/probe_http_test.go` | create | `probe_http` tests (fake `Prober`) |
| `cmd/kato/main.go` | modify | wire `methods.LocalProber{}` into `Deps` (line ~117) |
| `docs/METHOD.md` | modify | index rows + `probe_tcp`/`probe_http` sections |

No CRD change, no RBAC change. No existing test hardcodes the method count, so adding two methods does not break existing tests.

---

## Task 1: `Prober` seam + `LocalProber`

**Files:**
- Create: `internal/methods/prober.go`
- Create: `internal/methods/prober_test.go`
- Modify: `internal/methods/method.go`

- [ ] **Step 1: Write the failing test** — create `internal/methods/prober_test.go`:

```go
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
	// Open then immediately close a listener to obtain a port nothing listens on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port := hostPort(t, "http://"+ln.Addr().String())
	ln.Close()

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

	// Default verification rejects the self-signed cert.
	secure := LocalProber{}.ProbeHTTP(context.Background(), HTTPProbeRequest{
		Scheme: "https", Target: host, Port: port, Path: "/", ExpectStatus: 200, Timeout: 2 * time.Second,
	})
	if secure.Err == "" {
		t.Error("expected TLS verification error without insecureSkipVerify")
	}
	// InsecureSkipVerify accepts it.
	insecure := LocalProber{}.ProbeHTTP(context.Background(), HTTPProbeRequest{
		Scheme: "https", Target: host, Port: port, Path: "/", ExpectStatus: 200,
		InsecureSkipVerify: true, Timeout: 2 * time.Second,
	})
	if insecure.Err != "" || !insecure.StatusMatched {
		t.Errorf("insecure probe got %+v", insecure)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/methods/ -run TestLocalProber`
Expected: FAIL — `undefined: LocalProber`, `undefined: HTTPProbeRequest`.

- [ ] **Step 3: Create `internal/methods/prober.go`**

```go
package methods

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Prober performs active network probes. LocalProber is the in-process default
// (kato running inside the target cluster). A future RemoteProber will run probes
// inside a registered remote cluster (centralized multi-cluster); it implements the
// same interface, so probe_tcp/probe_http and their UseCases stay unchanged.
type Prober interface {
	ProbeTCP(ctx context.Context, target string, port int, timeout time.Duration) TCPResult
	ProbeHTTP(ctx context.Context, req HTTPProbeRequest) HTTPResult
}

// TCPResult is the outcome of a TCP connect probe.
type TCPResult struct {
	Success   bool   // connection established within the timeout
	LatencyMS int64  // connect time in ms; -1 on failure
	Err       string // failure reason; "" on success
}

// HTTPProbeRequest is a fully-resolved HTTP probe (params already parsed/defaulted).
type HTTPProbeRequest struct {
	Scheme             string // "http" | "https"
	Target             string
	Port               int
	Path               string
	ExpectStatus       int
	ExpectBodyContains string
	InsecureSkipVerify bool
	Timeout            time.Duration
}

// HTTPResult is the outcome of an HTTP probe. Success is composed by the method
// (StatusMatched && BodyMatched); the prober only reports the raw facts.
type HTTPResult struct {
	StatusCode    int
	StatusMatched bool
	BodyMatched   bool
	LatencyMS     int64
	Err           string
}

// LocalProber probes from the current process. Network reachability is governed by
// NetworkPolicy, not RBAC.
type LocalProber struct{}

func (LocalProber) ProbeTCP(ctx context.Context, target string, port int, timeout time.Duration) TCPResult {
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(target, strconv.Itoa(port)))
	if err != nil {
		return TCPResult{Success: false, LatencyMS: -1, Err: err.Error()}
	}
	latency := time.Since(start).Milliseconds()
	_ = conn.Close()
	return TCPResult{Success: true, LatencyMS: latency}
}

func (LocalProber) ProbeHTTP(ctx context.Context, req HTTPProbeRequest) HTTPResult {
	rawURL := fmt.Sprintf("%s://%s/%s", req.Scheme,
		net.JoinHostPort(req.Target, strconv.Itoa(req.Port)), strings.TrimPrefix(req.Path, "/"))
	client := &http.Client{
		Timeout: req.Timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: req.InsecureSkipVerify},
		},
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return HTTPResult{StatusCode: 0, LatencyMS: -1, Err: err.Error()}
	}
	start := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		return HTTPResult{StatusCode: 0, LatencyMS: -1, Err: err.Error()}
	}
	defer resp.Body.Close()
	bodyMatched := true
	if req.ExpectBodyContains != "" {
		// Bounded read so a large/streaming body cannot exhaust memory; body is
		// never retained past the match check (and is never an output).
		b, _ := io.ReadAll(io.LimitReader(resp.Body, defaultLogBytes))
		bodyMatched = strings.Contains(string(b), req.ExpectBodyContains)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return HTTPResult{
		StatusCode:    resp.StatusCode,
		StatusMatched: resp.StatusCode == req.ExpectStatus,
		BodyMatched:   bodyMatched,
		LatencyMS:     time.Since(start).Milliseconds(),
	}
}
```

- [ ] **Step 4: Add the `Prober` field to `Deps`** in `internal/methods/method.go` — change the `Deps` struct (currently `Kube` + `Metrics`) to:

```go
type Deps struct {
	Kube    kubernetes.Interface
	Metrics metricsv.Interface
	Prober  Prober
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./internal/methods/ -run TestLocalProber`
Expected: PASS (6 tests).

- [ ] **Step 6: Leave changes in the working tree (NO commit).** Proceed to Task 2.

---

## Task 2: `probe_tcp` method

**Files:**
- Create: `internal/methods/probe_tcp.go`
- Create: `internal/methods/probe_tcp_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/methods/probe_tcp_test.go`:

```go
package methods

import (
	"context"
	"testing"
	"time"
)

// fakeProber records the last call and returns canned results. Shared by the
// probe_tcp and probe_http method tests (no real network).
type fakeProber struct {
	tcp       TCPResult
	http      HTTPResult
	gotTarget string
	gotPort   int
	gotHTTP   HTTPProbeRequest
}

func (f *fakeProber) ProbeTCP(_ context.Context, target string, port int, _ time.Duration) TCPResult {
	f.gotTarget, f.gotPort = target, port
	return f.tcp
}

func (f *fakeProber) ProbeHTTP(_ context.Context, req HTTPProbeRequest) HTTPResult {
	f.gotHTTP = req
	return f.http
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
		"bad port":     {"target": "x", "port": "abc"},
		"port range":   {"target": "x", "port": "70000"},
		"empty port":   {"target": "x", "port": ""},
		"bad timeout":  {"target": "x", "port": "80", "timeout": "soon"},
	}
	for name, params := range cases {
		if _, err := m.Run(context.Background(), Deps{Prober: &fakeProber{}}, params); err == nil {
			t.Errorf("%s: expected param error, got nil", name)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/methods/ -run TestProbeTCP`
Expected: FAIL — `probe_tcp not registered`.

- [ ] **Step 3: Create `internal/methods/probe_tcp.go`** (also defines the shared param helpers used by `probe_http`):

```go
package methods

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

const defaultProbeTimeout = 5 * time.Second

// parsePort validates a 1-65535 port string. Shared by probe_tcp and probe_http.
func parsePort(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("param port: required")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("param port: %w", err)
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("param port: must be 1-65535, got %d", n)
	}
	return n, nil
}

// parseProbeTimeout reads the timeout param: unset -> 5s, else a Go duration > 0.
func parseProbeTimeout(s string) (time.Duration, error) {
	if s == "" {
		return defaultProbeTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("param timeout: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("param timeout: must be > 0, got %s", s)
	}
	return d, nil
}

type probeTCP struct{}

func (probeTCP) Name() string        { return "probe_tcp" }
func (probeTCP) Description() string { return "Active TCP connect check: does target:port accept a connection?" }

func (probeTCP) Params() []Param {
	return []Param{
		{Name: "target", Required: true, Description: "host, IP, or DNS name"},
		{Name: "port", Required: true, Description: "TCP port (1-65535)"},
		{Name: "timeout", Description: `connect timeout as a Go duration (default "5s")`},
	}
}

func (probeTCP) OutputFields() []OutputField {
	return []OutputField{
		{Name: "success", Type: FieldBool, Description: "TCP connection established within the timeout"},
		{Name: "latencyMs", Type: FieldInt, Description: "connect time in ms; -1 on failure"},
		{Name: "error", Type: FieldString, Description: `failure reason (refused/timeout/DNS); "" on success`},
	}
}

func (probeTCP) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	port, err := parsePort(params["port"])
	if err != nil {
		return nil, err
	}
	timeout, err := parseProbeTimeout(params["timeout"])
	if err != nil {
		return nil, err
	}
	res := deps.Prober.ProbeTCP(ctx, params["target"], port, timeout)
	return Outputs{
		"success":   res.Success,
		"latencyMs": res.LatencyMS,
		"error":     res.Err,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(probeTCP{}) }) }
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/methods/ -run TestProbeTCP`
Expected: PASS.

- [ ] **Step 5: Leave changes in the working tree (NO commit).** Proceed to Task 3.

---

## Task 3: `probe_http` method

**Files:**
- Create: `internal/methods/probe_http.go`
- Create: `internal/methods/probe_http_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/methods/probe_http_test.go` (reuses `fakeProber` from `probe_tcp_test.go`, same package):

```go
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
	// Defaults applied.
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
	_, err := m.Run(context.Background(), Deps{Prober: f}, map[string]string{
		"target": "api.svc", "port": "8443", "scheme": "https", "path": "healthz",
		"expectStatus": "204", "expectBodyContains": "ok", "insecureSkipVerify": "true", "timeout": "3s",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	g := f.gotHTTP
	if g.Scheme != "https" || g.Port != 8443 || g.Path != "healthz" || g.ExpectStatus != 204 ||
		g.ExpectBodyContains != "ok" || !g.InsecureSkipVerify || g.Timeout.String() != "3s" {
		t.Errorf("pass-through wrong: %+v", g)
	}
}

func TestProbeHTTPParamErrors(t *testing.T) {
	m, _ := Builtin().Get("probe_http")
	cases := map[string]map[string]string{
		"bad scheme":  {"target": "x", "port": "80", "scheme": "ftp"},
		"bad status":  {"target": "x", "port": "80", "expectStatus": "two"},
		"bad insecure": {"target": "x", "port": "80", "insecureSkipVerify": "maybe"},
		"bad port":    {"target": "x", "port": "0"},
	}
	for name, params := range cases {
		if _, err := m.Run(context.Background(), Deps{Prober: &fakeProber{}}, params); err == nil {
			t.Errorf("%s: expected param error, got nil", name)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/methods/ -run TestProbeHTTP`
Expected: FAIL — `probe_http not registered`.

- [ ] **Step 3: Create `internal/methods/probe_http.go`**

```go
package methods

import (
	"context"
	"fmt"
	"strconv"
)

type probeHTTP struct{}

func (probeHTTP) Name() string        { return "probe_http" }
func (probeHTTP) Description() string { return "Active HTTP(S) GET with status and optional body assertions" }

func (probeHTTP) Params() []Param {
	return []Param{
		{Name: "target", Required: true, Description: "host, IP, or DNS name"},
		{Name: "port", Required: true, Description: "port (1-65535)"},
		{Name: "scheme", Description: `"http" or "https" (default "http")`},
		{Name: "path", Description: `request path (default "/")`},
		{Name: "expectStatus", Description: `expected HTTP status code (default "200")`},
		{Name: "expectBodyContains", Description: "substring the response body must contain; empty = no body check"},
		{Name: "insecureSkipVerify", Description: `"true" to accept self-signed certs (https only; default "false")`},
		{Name: "timeout", Description: `request timeout as a Go duration (default "5s")`},
	}
}

func (probeHTTP) OutputFields() []OutputField {
	return []OutputField{
		{Name: "success", Type: FieldBool, Description: "statusMatched and bodyMatched"},
		{Name: "statusCode", Type: FieldInt, Description: "HTTP status; 0 if no response (transport failure)"},
		{Name: "statusMatched", Type: FieldBool, Description: "statusCode == expectStatus"},
		{Name: "bodyMatched", Type: FieldBool, Description: "body contains expectBodyContains (true if unset)"},
		{Name: "latencyMs", Type: FieldInt, Description: "round-trip in ms; -1 on failure"},
		{Name: "error", Type: FieldString, Description: `failure reason; "" on success`},
	}
}

func (probeHTTP) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	port, err := parsePort(params["port"])
	if err != nil {
		return nil, err
	}
	timeout, err := parseProbeTimeout(params["timeout"])
	if err != nil {
		return nil, err
	}
	scheme := params["scheme"]
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf(`param scheme: must be "http" or "https", got %q`, scheme)
	}
	path := params["path"]
	if path == "" {
		path = "/"
	}
	expectStatus := 200
	if v := params["expectStatus"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("param expectStatus: %w", err)
		}
		expectStatus = n
	}
	insecure := false
	if v := params["insecureSkipVerify"]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("param insecureSkipVerify: %w", err)
		}
		insecure = b
	}
	res := deps.Prober.ProbeHTTP(ctx, HTTPProbeRequest{
		Scheme:             scheme,
		Target:             params["target"],
		Port:               port,
		Path:               path,
		ExpectStatus:       expectStatus,
		ExpectBodyContains: params["expectBodyContains"],
		InsecureSkipVerify: insecure,
		Timeout:            timeout,
	})
	return Outputs{
		"success":       res.StatusMatched && res.BodyMatched,
		"statusCode":    int64(res.StatusCode),
		"statusMatched": res.StatusMatched,
		"bodyMatched":   res.BodyMatched,
		"latencyMs":     res.LatencyMS,
		"error":         res.Err,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(probeHTTP{}) }) }
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/methods/ -run TestProbeHTTP`
Expected: PASS.

- [ ] **Step 5: Run the full methods package**

Run: `go test ./internal/methods/`
Expected: PASS (new probe tests + all existing tests).

- [ ] **Step 6: Leave changes in the working tree (NO commit).** Proceed to Task 4.

---

## Task 4: Wire `LocalProber` into `cmd/kato`

**Files:**
- Modify: `cmd/kato/main.go:117`

- [ ] **Step 1: Edit the `Deps` construction.** In `cmd/kato/main.go`, change the engine's `Deps` line (currently `Deps: methods.Deps{Kube: kubeClient, Metrics: metricsClient},`) to include the prober:

```go
		Deps:      methods.Deps{Kube: kubeClient, Metrics: metricsClient, Prober: methods.LocalProber{}},
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 3: Leave changes in the working tree (NO commit).** Proceed to Task 5.

---

## Task 5: Document the methods in `docs/METHOD.md`

**Files:**
- Modify: `docs/METHOD.md`

- [ ] **Step 1: Add index rows.** In the top method-index table (the list of `| [\`name\`](#anchor) | description |` rows, near `list_pods`), add:

```
| [`probe_tcp`](#probe_tcp) | Active TCP connect check — does `target:port` accept a connection? |
| [`probe_http`](#probe_http) | Active HTTP(S) GET with status and optional body assertions |
```

- [ ] **Step 2: Append the two method sections** at the end of the per-method sections (same format as the `list_pods` section — `###` heading, prose, **Inputs** table, **Scalar outputs** table):

````markdown
### `probe_tcp`

Active TCP connect check: opens a TCP connection to `target:port` and reports whether
it was accepted within the timeout (a "telnet"-style port check, e.g. "is the database
serving on 5432?"). A refused/timed-out connection is a finding (`success: false`), not
an error, so a flow can gate later steps on `$(steps.<step>.success)`. Runs from kato's
pod; reachability is governed by NetworkPolicy (no Kubernetes RBAC needed).

**Inputs**

| Name | Required | Description |
|---|---|---|
| `target` | yes | host, IP, or DNS name (e.g. `postgres.data.svc.cluster.local`, `10.0.0.5`) |
| `port` | yes | TCP port (1–65535) |
| `timeout` | no | connect timeout as a Go duration (default `5s`) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `success` | bool | TCP connection established within the timeout |
| `latencyMs` | int | connect time in ms; `-1` on failure |
| `error` | string | failure reason (refused/timeout/DNS); `""` on success |

---

### `probe_http`

Active HTTP(S) `GET` to `scheme://target:port/path`, asserting the response status code
and (optionally) that the body contains a substring (e.g. "does the health endpoint
return 200?"). A transport failure or non-matching status/body is a finding
(`success: false`), not an error. The response body is never returned as an output —
only `statusCode`/`statusMatched`/`bodyMatched` — so payloads (which may contain
secrets) stay out of the Run record and the LLM evidence. Runs from kato's pod;
reachability is governed by NetworkPolicy (no Kubernetes RBAC needed).

**Inputs**

| Name | Required | Description |
|---|---|---|
| `target` | yes | host, IP, or DNS name |
| `port` | yes | port (1–65535) |
| `scheme` | no | `http` or `https` (default `http`) |
| `path` | no | request path (default `/`) |
| `expectStatus` | no | expected HTTP status code (default `200`) |
| `expectBodyContains` | no | substring the response body must contain; empty = no body check |
| `insecureSkipVerify` | no | `true` to accept self-signed certs (https only; default `false`) |
| `timeout` | no | request timeout as a Go duration (default `5s`) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `success` | bool | `statusMatched` and `bodyMatched` |
| `statusCode` | int | HTTP status; `0` if no response (transport failure) |
| `statusMatched` | bool | `statusCode == expectStatus` |
| `bodyMatched` | bool | body contains `expectBodyContains` (`true` if unset) |
| `latencyMs` | int | round-trip in ms; `-1` on failure |
| `error` | string | failure reason; `""` on success |
````

- [ ] **Step 3: Leave changes in the working tree (NO commit).** Proceed to Task 6.

---

## Task 6: Whole-repo verification

**Files:** none (verification only)

- [ ] **Step 1: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 2: Full unit suite**

Run: `go test ./...`
Expected: PASS across all packages (envtest-gated controller tests skip gracefully if `KUBEBUILDER_ASSETS` is unset).

- [ ] **Step 3: Confirm both methods are registered and exposed**

Run: `go test ./internal/methods/ -run 'TestProbeTCP|TestProbeHTTP|TestLocalProber' -v`
Expected: all PASS. (`probe_tcp`/`probe_http` resolve via `Builtin().Get`, which also means `GET /api/v1/methods` lists them automatically.)

- [ ] **Step 4: Leave changes in the working tree (NO commit).** Report completion.

---

## Self-Review

**Spec coverage:**
- `probe_tcp` method (params, outputs, finding-not-error) → Task 2. ✓
- `probe_http` method (params incl. scheme/path/expectStatus/expectBodyContains/insecureSkipVerify, outputs, `success = statusMatched && bodyMatched`, body never an output) → Task 3 + `LocalProber.ProbeHTTP` bounded body read in Task 1. ✓
- `Prober` interface + `TCPResult`/`HTTPProbeRequest`/`HTTPResult` + `LocalProber` + `Deps.Prober` → Task 1. ✓
- `cmd/kato` wiring → Task 4. ✓
- No new RBAC / no CRD change → nothing to do (correct). ✓
- `probe_` verb prefix, error-handling semantics, no-secret-leak body handling → Tasks 1–3. ✓
- docs/METHOD.md → Task 5. ✓
- Non-goals (k6, load testing, centralization, custom headers, non-GET) → no tasks, as intended. ✓

**Placeholder scan:** none — every code/step is concrete with exact commands and expected output.

**Type consistency:** `Prober`/`TCPResult`/`HTTPProbeRequest`/`HTTPResult` defined in Task 1 and used identically in Tasks 2–3; `parsePort`/`parseProbeTimeout`/`defaultProbeTimeout` defined in Task 2 and reused in Task 3; `Deps.Prober` defined in Task 1 and consumed in Tasks 2–4; `fakeProber` defined in `probe_tcp_test.go` (Task 2) and reused by `probe_http_test.go` (Task 3); `defaultLogBytes` (existing const in `pod_logs.go`) reused for the bounded body read.
