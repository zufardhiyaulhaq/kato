{% raw %}
# kato gRPC Health Probe (`probe_grpc`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `probe_grpc` method (plus a `grpc-connectivity-check` UseCase and docs) that performs the standard `grpc.health.v1.Health/Check` RPC against a target and reports whether it is `SERVING`.

**Architecture:** `probe_grpc` is the fifth member of kato's active-probe family, alongside `probe_tcp`/`probe_http`/`probe_dns`/`probe_traceroute`. It dials through the existing `deps.Prober` seam: the `Prober` interface gains a `ProbeGRPC` method, `LocalProber` implements it with the grpc-go client + the bundled `health/grpc_health_v1` proto, and the thin `probe_grpc` method parses params → calls the prober → returns flat outputs. A failed probe is a finding (`success:false` + `error`), never a Go error — only param mistakes return a Go error.

**Tech Stack:** Go, grpc-go (`google.golang.org/grpc` — new direct dependency, brings `health/grpc_health_v1`, `credentials`, `credentials/insecure`), the kato method/registry framework, helm-docs.

## Global Constraints

- **Go toolchain:** the `go` on PATH defaults to a mismatched GOROOT. Prefix **every** go command with `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go` (e.g. `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./...`).
- **Do NOT commit** — the user commits their own work. The `Commit` steps below are written as `git add` + a suggested message, but STOP before running `git commit`; leave the changes staged/working and report. (If executing inline, treat each "Commit" step as "stage and report", not "commit".)
- **Do NOT create `CLAUDE.md`.**
- **Failure-is-a-finding contract:** probe/RPC failures return `success:false` + a populated `error` string with a **nil Go error**; only param validation mistakes return a non-nil Go error.
- **Registry lookups in tests use `methods.Builtin()`** (fully-populated), never `methods.NewRegistry()` (empty).
- **`success = (status == SERVING)`.** `status` is the raw health-check enum string; `status != ""` means the endpoint answered the RPC (reachable); `status == ""` means the RPC never completed.
- **`Check` error semantics (reference health server):** unregistered service → gRPC `NotFound` error; no health service → `UNIMPLEMENTED`. Both are RPC errors → `success:false`, `status:""`, `error` set. `SERVICE_UNKNOWN` is a `Watch`-only status and is NOT returned by `Check`.
- **Single whole-operation `timeout`** (dial + RPC), default `5s`, via the shared `parseProbeTimeout` helper. `port` via the shared `parsePort` helper (1–65535).

---

## File Structure

- `go.mod` / `go.sum` — promote `google.golang.org/grpc` to a direct dependency.
- `internal/methods/prober.go` — extend the `Prober` interface + `LocalProber`; add `GRPCProbeRequest` / `GRPCResult`.
- `internal/methods/probe_grpc.go` (new) — the `probeGRPC` method + `init()` registration.
- `internal/methods/probe_tcp_test.go` — extend the shared `fakeProber` (fields + `ProbeGRPC`).
- `internal/methods/prober_test.go` — `LocalProber.ProbeGRPC` integration tests against an in-process health server.
- `internal/methods/probe_grpc_test.go` (new) — method-level tests using `fakeProber`.
- `examples/usecases/grpc-connectivity-check.yaml` (new) — the ready-made UseCase.
- `docs/METHOD.md` — index row + `### probe_grpc` section.
- `charts/kato/README.md.gotmpl` — count `32 → 33`, Active probes row, ready-made UseCases sentence.
- `README.md`, `charts/kato/README.md` — regenerated via `make readme`.

---

## Task 1: `Prober` interface — `ProbeGRPC` + `LocalProber` implementation

Adds the grpc-go dependency, widens the `Prober` interface, implements `LocalProber.ProbeGRPC`, keeps the test package compiling by extending `fakeProber`, and verifies the real client against an in-process health server. This is one deliverable: the prober-level gRPC health check works end to end.

**Files:**
- Modify: `go.mod`, `go.sum` (add `google.golang.org/grpc` direct)
- Modify: `internal/methods/prober.go`
- Modify: `internal/methods/probe_tcp_test.go` (extend `fakeProber` so the package compiles)
- Test: `internal/methods/prober_test.go`

**Interfaces:**
- Produces (consumed by Task 2 and the fake in this task):
  - `Prober.ProbeGRPC(ctx context.Context, req GRPCProbeRequest) GRPCResult`
  - `type GRPCProbeRequest struct { Target string; Port int; Service string; TLS bool; InsecureSkipVerify bool; ServerName string; Timeout time.Duration }`
  - `type GRPCResult struct { Serving bool; Status string; LatencyMS int64; Err string }`
  - `func (LocalProber) ProbeGRPC(ctx context.Context, req GRPCProbeRequest) GRPCResult`

- [ ] **Step 1: Add the grpc-go dependency**

Run (module is present in the local cache):

```bash
cd /Users/zufardhiyaulhaq/Documents/personal/github/kato
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go get google.golang.org/grpc
```

Expected: `go.mod` now lists `google.golang.org/grpc vX.Y.Z` as a direct require (no `// indirect`). (A `go mod tidy` in Step 9 finalizes the graph.)

- [ ] **Step 2: Extend the `Prober` interface and add the two types**

In `internal/methods/prober.go`, add the new imports and the interface method + types. The import block (currently lines 3–18) gains `crypto/tls` and the grpc packages:

```go
import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
)
```

Add `ProbeGRPC` to the interface (after `ProbeTraceroute`):

```go
type Prober interface {
	ProbeTCP(ctx context.Context, target string, port int, timeout time.Duration) TCPResult
	ProbeHTTP(ctx context.Context, req HTTPProbeRequest) HTTPResult
	ProbeDNS(ctx context.Context, req DNSProbeRequest) DNSResult
	ProbeTraceroute(ctx context.Context, req TracerouteRequest) TracerouteResult
	ProbeGRPC(ctx context.Context, req GRPCProbeRequest) GRPCResult
}
```

Add the request/result types (place them near the other `*Request`/`*Result` types, e.g. after `TracerouteResult`/`HopResult`):

```go
// GRPCProbeRequest is a fully-resolved gRPC health probe (params already parsed/defaulted).
type GRPCProbeRequest struct {
	Target             string        // host, IP, or DNS name
	Port               int           // gRPC port
	Service            string        // health service name; "" = overall server health
	TLS                bool          // true = TLS, false = plaintext h2c
	InsecureSkipVerify bool          // skip cert verification (TLS only)
	ServerName         string        // TLS SNI / cert-name override; "" = derived from target
	Timeout            time.Duration // whole-operation bound (dial + Check RPC)
}

// GRPCResult is the outcome of a gRPC health check.
type GRPCResult struct {
	Serving   bool   // health status == SERVING
	Status    string // "SERVING"/"NOT_SERVING"/"UNKNOWN"; "" if the RPC never completed
	LatencyMS int64  // Check round-trip in ms; -1 on failure
	Err       string // failure reason (dial/timeout/UNIMPLEMENTED/NotFound); "" on success
}
```

- [ ] **Step 3: Implement `LocalProber.ProbeGRPC`**

Add to `internal/methods/prober.go` (after `LocalProber.ProbeTraceroute`):

```go
func (LocalProber) ProbeGRPC(ctx context.Context, req GRPCProbeRequest) GRPCResult {
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	var creds credentials.TransportCredentials
	if req.TLS {
		creds = credentials.NewTLS(&tls.Config{
			InsecureSkipVerify: req.InsecureSkipVerify,
			ServerName:         req.ServerName, // "" -> derived from the dial target
		})
	} else {
		creds = insecure.NewCredentials()
	}

	addr := net.JoinHostPort(req.Target, strconv.Itoa(req.Port))
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return GRPCResult{LatencyMS: -1, Err: err.Error()}
	}
	defer conn.Close()

	start := time.Now()
	// grpc.NewClient is lazy; this Check forces the connect, so dial failures,
	// deadline, UNIMPLEMENTED (no health service) and NotFound (service not
	// registered) all surface here as the RPC error — findings, not method errors.
	resp, err := grpc_health_v1.NewHealthClient(conn).
		Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: req.Service})
	if err != nil {
		return GRPCResult{LatencyMS: -1, Err: err.Error()}
	}
	return GRPCResult{
		Serving:   resp.GetStatus() == grpc_health_v1.HealthCheckResponse_SERVING,
		Status:    resp.GetStatus().String(), // SERVING / NOT_SERVING / UNKNOWN
		LatencyMS: time.Since(start).Milliseconds(),
	}
}
```

- [ ] **Step 4: Extend `fakeProber` so the test package still compiles**

The widened interface means the shared `fakeProber` (in `internal/methods/probe_tcp_test.go`) must implement `ProbeGRPC`, or the whole `methods` test package fails to compile. Add two fields to the struct and the method.

In `internal/methods/probe_tcp_test.go`, change the struct (lines 11–21) to add `grpc` and `gotGRPC`:

```go
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
```

Add the method (after `ProbeTraceroute`, line 41):

```go
func (f *fakeProber) ProbeGRPC(_ context.Context, req GRPCProbeRequest) GRPCResult {
	f.gotGRPC = req
	return f.grpc
}
```

- [ ] **Step 5: Write the failing `LocalProber` integration tests**

Add to `internal/methods/prober_test.go`. These need an in-process gRPC health server. Add imports `"google.golang.org/grpc"`, `"google.golang.org/grpc/health"`, and `"google.golang.org/grpc/health/grpc_health_v1"` to the file's import block.

Helper + four tests:

```go
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
```

- [ ] **Step 6: Watch the tests fail (red)**

Widening the interface (Step 2) forces `LocalProber` and `fakeProber` to implement `ProbeGRPC` just to compile, so a "no implementation" red state isn't reachable. Instead, get a genuine red by temporarily gutting the body: comment out the real logic in `LocalProber.ProbeGRPC` and `return GRPCResult{}`. Then run:

```bash
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/methods/ -run TestLocalProberGRPC -v
```

Expected: FAIL — `TestLocalProberGRPCServing` sees `Status=""` not `"SERVING"`, etc. Then restore the real Step 3 body before Step 7.

- [ ] **Step 7: Run the tests to verify they pass**

Run:

```bash
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/methods/ -run TestLocalProberGRPC -v
```

Expected: `PASS` — all four `TestLocalProberGRPC*` tests pass.

- [ ] **Step 8: Run the full methods package to confirm nothing regressed**

Run:

```bash
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/methods/
```

Expected: `ok  github.com/zufardhiyaulhaq/kato/internal/methods`

- [ ] **Step 9: Tidy the module graph and build**

Run:

```bash
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go mod tidy
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go build ./...
```

Expected: `go.mod` has `google.golang.org/grpc` as a direct require; build succeeds with no errors.

- [ ] **Step 10: Stage and report (do NOT commit)**

```bash
git add go.mod go.sum internal/methods/prober.go internal/methods/prober_test.go internal/methods/probe_tcp_test.go
# Suggested message (user commits):
# feat: add ProbeGRPC to Prober interface + LocalProber gRPC health check
```

STOP — leave staged, report status.

---

## Task 2: `probe_grpc` method

The thin method that parses params, calls `deps.Prober.ProbeGRPC`, and returns flat outputs. Registered via `init()` so `methods.Builtin()` picks it up.

**Files:**
- Create: `internal/methods/probe_grpc.go`
- Test: `internal/methods/probe_grpc_test.go`

**Interfaces:**
- Consumes: `GRPCProbeRequest`, `GRPCResult`, `Prober.ProbeGRPC` (Task 1); helpers `parsePort`, `parseProbeTimeout`; `fakeProber` fields `grpc`/`gotGRPC` (Task 1).
- Produces: a registered method named `probe_grpc` with params `target,port,service,tls,insecureSkipVerify,serverName,timeout` and outputs `success,status,latencyMs,error`.

- [ ] **Step 1: Write the failing method tests**

Create `internal/methods/probe_grpc_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:

```bash
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/methods/ -run TestProbeGRPC -v
```

Expected: FAIL — `probe_grpc not registered` (the method file does not exist yet).

- [ ] **Step 3: Write the method**

Create `internal/methods/probe_grpc.go`:

```go
package methods

import (
	"context"
	"fmt"
	"strconv"
)

type probeGRPC struct{}

func (probeGRPC) Name() string { return "probe_grpc" }
func (probeGRPC) Description() string {
	return "Active gRPC health check: is target:port SERVING (grpc.health.v1.Health/Check)?"
}

func (probeGRPC) Params() []Param {
	return []Param{
		{Name: "target", Required: true, Description: "host, IP, or DNS name"},
		{Name: "port", Required: true, Description: "gRPC port (1-65535)"},
		{Name: "service", Description: `health service name; empty = overall server health`},
		{Name: "tls", Description: `"true" for TLS, "false" for plaintext h2c (default "false")`},
		{Name: "insecureSkipVerify", Description: `"true" to accept self-signed certs (TLS only; default "false")`},
		{Name: "serverName", Description: "TLS SNI / cert-name override; empty = derived from target"},
		{Name: "timeout", Description: `whole-operation timeout as a Go duration (default "5s")`},
	}
}

func (probeGRPC) OutputFields() []OutputField {
	return []OutputField{
		{Name: "success", Type: FieldBool, Description: "health status is SERVING"},
		{Name: "status", Type: FieldString, Description: `SERVING/NOT_SERVING/UNKNOWN; "" if the RPC never completed`},
		{Name: "latencyMs", Type: FieldInt, Description: "Check round-trip in ms; -1 on failure"},
		{Name: "error", Type: FieldString, Description: `failure reason (dial/timeout/UNIMPLEMENTED/NotFound); "" on success`},
	}
}

func (probeGRPC) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
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
	useTLS := false
	if v := params["tls"]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("param tls: %w", err)
		}
		useTLS = b
	}
	insecure := false
	if v := params["insecureSkipVerify"]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("param insecureSkipVerify: %w", err)
		}
		insecure = b
	}
	res := deps.Prober.ProbeGRPC(ctx, GRPCProbeRequest{
		Target:             params["target"],
		Port:               port,
		Service:            params["service"],
		TLS:                useTLS,
		InsecureSkipVerify: insecure,
		ServerName:         params["serverName"],
		Timeout:            timeout,
	})
	return Outputs{
		"success":   res.Serving,
		"status":    res.Status,
		"latencyMs": res.LatencyMS,
		"error":     res.Err,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(probeGRPC{}) }) }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:

```bash
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/methods/ -run TestProbeGRPC -v
```

Expected: `PASS` — all five `TestProbeGRPC*` tests pass.

- [ ] **Step 5: Run the full methods package**

Run:

```bash
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/methods/
```

Expected: `ok  github.com/zufardhiyaulhaq/kato/internal/methods`

- [ ] **Step 6: Stage and report (do NOT commit)**

```bash
git add internal/methods/probe_grpc.go internal/methods/probe_grpc_test.go
# Suggested message (user commits):
# feat: add probe_grpc method
```

STOP — leave staged, report status.

---

## Task 3: `grpc-connectivity-check` UseCase

A layered UseCase mirroring `tcp-`/`http-connectivity-check`: DNS → TCP → gRPC health (when the port is open) → traceroute (when it is not). All four inputs are `required` (kato has no input defaults; each is referenced in a `with` value, and an unresolved `$(inputs.X)` fails the step).

**Files:**
- Create: `examples/usecases/grpc-connectivity-check.yaml`
- Test: a temporary validation test (created, run, then removed) at `internal/engine/usecase_grpc_validation_test.go` — see Step 2.

**Interfaces:**
- Consumes: methods `probe_dns`, `probe_tcp`, `probe_grpc` (Task 2), `probe_traceroute`; validated by `engine.ValidateUseCase(&uc, methods.Builtin(), func(string) bool { return true }) []string`.
- NOTE: `ValidateUseCase` is in package `engine`, which imports `methods`. The test therefore lives in `package engine` (a test in `package methods` importing `engine` would be an import cycle). It returns a `[]string` of problems (empty = valid), NOT an `error`.

- [ ] **Step 1: Write the UseCase**

Create `examples/usecases/grpc-connectivity-check.yaml`:

```yaml
# gRPC connectivity check: can this gRPC endpoint be reached AND is it SERVING?
# Layered, from kato's own pod: DNS (resolve) -> TCP (port open?) -> gRPC health
# (grpc.health.v1.Health/Check) when the port is open -> traceroute when it is not,
# to tell "service down" apart from "network path broken". Answers "is target:port
# a healthy gRPC service?" and, when it isn't, localizes why.
#
# target: IP or DNS name.  port: gRPC port.  service: health service name ("" = whole
# server).  tls: "true" for TLS, "false" for plaintext h2c.
#
#   curl -s -X POST localhost:8080/api/v1/usecases/grpc-connectivity-check/run \
#     -d '{"inputs":{"target":"cart.shop.svc.cluster.local","port":"50051","service":"","tls":"false"}}' | jq
#
apiVersion: kato.zufardhiyaulhaq.com/v1alpha1
kind: UseCase
metadata:
  name: grpc-connectivity-check
spec:
  description: "Is target:port a healthy (SERVING) gRPC service? DNS + TCP + gRPC health, traceroute if unreachable"
  inputs:
    - name: target
      required: true
    - name: port
      required: true
    # health service name; "" = overall server health. Required (kato has no input defaults).
    - name: service
      required: true
    # "true" for TLS, "false" for plaintext h2c. Required (kato has no input defaults).
    - name: tls
      required: true
  steps:
    # L3 name resolution: does the target resolve at all? A failure here (NXDOMAIN/timeout)
    # is a different root cause than a broken path, and it explains a TCP failure below.
    # Uses the pod's own resolver (/etc/resolv.conf). An IP literal resolves to itself.
    - name: dns
      method: probe_dns
      with:
        name: $(inputs.target)
      summaryFilter: [success, addresses, recordCount, latencyMs, error]

    # L4: is the port even open? Ground truth for reachability, and it separates a network
    # problem from an app problem: if TCP fails, the gRPC Check never had a chance.
    - name: tcp
      method: probe_tcp
      with:
        target: $(inputs.target)
        port: $(inputs.port)

    # L7: only when the port is open — is the gRPC health service SERVING? status tells a
    # reachable-but-NOT_SERVING endpoint apart from a healthy one; an empty status means the
    # RPC never completed (not a gRPC server, TLS mismatch, no health service, or the service
    # name isn't registered) — read the error. Set tls: "true" for a TLS endpoint (add
    # insecureSkipVerify: "true" for a self-signed cert).
    - name: grpc
      when: $(steps.tcp.success)
      method: probe_grpc
      with:
        target: $(inputs.target)
        port: $(inputs.port)
        service: $(inputs.service)
        tls: $(inputs.tls)
      summaryFilter: [success, status, latencyMs, error]

    # L3 path: only when the port is NOT open — trace where replies stop. Reaching the
    # target IP = path fine, port down/filtered at the host; stopping short = network path
    # broken upstream. summaryFilter is left unset so the full `hops` list reaches the LLM.
    # maxHops capped and timeout short to bound worst-case wall time.
    - name: traceroute
      when: $(steps.tcp.success) == false
      method: probe_traceroute
      with:
        target: $(inputs.target)
        maxHops: "20"
        timeout: "1s"
  summary:
    prompt: |
      Is $(inputs.target):$(inputs.port) a healthy gRPC service, from inside the
      cluster? Check in order:

      1. dns.success false -> name doesn't resolve. Root cause is DNS (missing record,
         wrong namespace, CoreDNS). Stop here; it also explains any TCP failure.
      2. tcp.success true -> port is open (reachable). Then gRPC health:
         - grpc.success true (status SERVING) -> healthy. Done (give latency).
         - grpc.success false with status NOT_SERVING (or UNKNOWN) -> reachable, but the
           app reports itself unhealthy. App-level problem; give the status.
         - grpc.success false with status empty -> the health RPC never completed under an
           open port: not a gRPC server, TLS mismatch (plaintext vs TLS), no health service
           (UNIMPLEMENTED), or the service name isn't registered (NotFound). Say which from
           the grpc error. Still reachable at L4.
      3. tcp.success false -> port not reachable, gRPC was skipped. Use traceroute: reached
         the target IP = path fine, port down/filtered at the host (service not listening /
         host firewall). Trace stops short = network path broken there (routing /
         NetworkPolicy / firewall). tcp error: refused = rejected, timeout = no answer.

      Give a one-line verdict + the evidence + the fix.
```

- [ ] **Step 2: Write a temporary validation test**

Create `internal/engine/usecase_grpc_validation_test.go` (temporary — removed in Step 4). It lives in `package engine` (which already imports `methods`, avoiding an import cycle). It globs every example UseCase and validates it against the built-in registry, catching an unknown method, an unresolvable ref, or a bad `when` expression. `ValidateUseCase` returns a `[]string` of problems — empty means valid:

```go
package engine

import (
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

func TestGRPCConnectivityUseCaseValidates(t *testing.T) {
	paths, err := filepath.Glob("../../examples/usecases/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		var uc v1alpha1.UseCase
		if err := yaml.Unmarshal(b, &uc); err != nil {
			t.Fatalf("unmarshal %s: %v", p, err)
		}
		if problems := ValidateUseCase(&uc, methods.Builtin(), func(string) bool { return true }); len(problems) > 0 {
			t.Errorf("%s: %v", filepath.Base(p), problems)
		}
	}
}
```

- [ ] **Step 3: Run the validation test**

Run:

```bash
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/engine/ -run TestGRPCConnectivityUseCaseValidates -v
```

Expected: `PASS` — every example UseCase (including the new one) validates. A failure here means a typo in the YAML (unknown method, unresolvable `$(inputs.X)`, or an untyped `when`).

- [ ] **Step 4: Remove the temporary test**

```bash
rm internal/engine/usecase_grpc_validation_test.go
```

- [ ] **Step 5: Stage and report (do NOT commit)**

```bash
git add examples/usecases/grpc-connectivity-check.yaml
# Suggested message (user commits):
# feat: add grpc-connectivity-check usecase
```

STOP — leave staged, report status.

---

## Task 4: Documentation

Add `probe_grpc` to `docs/METHOD.md`, bump the chart README template, and regenerate the READMEs.

**Files:**
- Modify: `docs/METHOD.md` (index row after `probe_traceroute`; `### probe_grpc` section)
- Modify: `charts/kato/README.md.gotmpl`
- Regenerate: `README.md`, `charts/kato/README.md`

**Interfaces:** none (docs only).

- [ ] **Step 1: Add the METHOD.md index row**

In `docs/METHOD.md`, after the `probe_traceroute` index row (currently line 68), add:

```markdown
| [`probe_grpc`](#probe_grpc) | Active gRPC health check — is `target:port` SERVING (`grpc.health.v1.Health/Check`)? |
```

- [ ] **Step 2: Add the `### probe_grpc` section**

In `docs/METHOD.md`, append after the `probe_traceroute` section (after its trailing `---`, around line 1120):

```markdown
### `probe_grpc`

Active gRPC health check from kato's pod: dials `target:port` and calls the standard
`grpc.health.v1.Health/Check` RPC (the same check as the widely-used `grpc-health-probe`),
reporting whether the service is **SERVING**. A `NOT_SERVING`/`UNKNOWN` status, a transport
failure, a missing health service (`UNIMPLEMENTED`), or an unregistered service name
(`NotFound`) is a finding (`success: false`), not an error — so a flow can gate later steps on
`$(steps.<step>.success)`. Set an empty `service` (the default) to check overall server health,
or a service name to check a specific registered service. Supports plaintext (`tls: "false"`,
the default) and TLS (`tls: "true"`, with `insecureSkipVerify`/`serverName` for cert handling).
Runs from kato's pod; reachability is governed by NetworkPolicy (no Kubernetes RBAC needed).

**Inputs**

| Name | Required | Description |
|---|---|---|
| `target` | yes | host, IP, or DNS name |
| `port` | yes | gRPC port (1–65535) |
| `service` | no | health service name; empty = overall server health |
| `tls` | no | `"true"` for TLS, `"false"` for plaintext h2c (default `"false"`) |
| `insecureSkipVerify` | no | `"true"` to accept self-signed certs (TLS only; default `"false"`) |
| `serverName` | no | TLS SNI / cert-name override; empty = derived from `target` |
| `timeout` | no | whole-operation timeout (dial + RPC) as a Go duration (default `5s`) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `success` | bool | health status is `SERVING` |
| `status` | string | `SERVING`/`NOT_SERVING`/`UNKNOWN`; `""` if the RPC never completed |
| `latencyMs` | int | Check round-trip in ms; `-1` on failure |
| `error` | string | failure reason (dial/timeout/`UNIMPLEMENTED`/`NotFound`); `""` on success |

Pair with `probe_tcp`: gate `probe_grpc` on `$(steps.<tcp>.success)` so the health RPC runs only
when the port is open, separating "network path broken" from "service unhealthy". `status` is the
gRPC analog of `probe_http`'s `statusCode`: a non-empty `status` means the endpoint answered
(reachable), while `success` is specifically `SERVING`.

---
```

- [ ] **Step 3: Update the chart README template**

In `charts/kato/README.md.gotmpl`:

Change the method count (line 103):

```markdown
kato ships **33 read-only checks** you compose into flows — pods, workloads, nodes,
```

Change the Active probes row (line 118):

```markdown
| Active probes | `probe_tcp`, `probe_http`, `probe_dns`, `probe_traceroute`, `probe_grpc` (run from kato's pod; reachability governed by NetworkPolicy) |
```

Change the ready-made UseCases sentence (lines 121–122) to include gRPC connectivity:

```markdown
Ready-made UseCases (general pod, deployment & node troubleshooting, cluster DNS, the Terway CNI, the istio-ingressgateway, control-plane health, and TCP/HTTP/gRPC connectivity checks)
live under [`examples/`](https://github.com/zufardhiyaulhaq/kato/tree/main/examples).
```

- [ ] **Step 4: Regenerate the READMEs**

Run:

```bash
cd /Users/zufardhiyaulhaq/Documents/personal/github/kato && make readme
```

Expected: `README.md` and `charts/kato/README.md` regenerate; `git diff --stat` shows both changed with the new count and probe row. (Requires `helm-docs` on PATH — if missing, install per the repo's tooling and re-run.)

- [ ] **Step 5: Verify the docs are consistent**

Run:

```bash
grep -n "probe_grpc" docs/METHOD.md README.md charts/kato/README.md
grep -n "33 read-only" charts/kato/README.md.gotmpl README.md charts/kato/README.md
```

Expected: `probe_grpc` appears in all three docs (index row + section in METHOD.md; probes row in both READMEs); `33 read-only` appears in the template and both generated READMEs.

- [ ] **Step 6: Stage and report (do NOT commit)**

```bash
git add docs/METHOD.md charts/kato/README.md.gotmpl README.md charts/kato/README.md
# Suggested message (user commits):
# docs: document probe_grpc and grpc-connectivity-check
```

STOP — leave staged, report status.

---

## Final verification

- [ ] **Full build + test**

Run:

```bash
cd /Users/zufardhiyaulhaq/Documents/personal/github/kato
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go build ./...
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./...
```

Expected: build clean; all packages `ok` (or cached). The temporary validation test from Task 3 is already removed.

- [ ] **Confirm nothing is committed**

Run:

```bash
git status --short
```

Expected: the new/modified files are staged/working but NOT committed. Report the final `git status` to the user for them to commit.

## Spec reference

Design: `docs/superpowers/specs/2026-07-15-kato-probe-grpc-design.md`.
{% endraw %}
