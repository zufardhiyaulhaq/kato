# kato gRPC Health Probe Method: `probe_grpc`
{% raw %}
**Status:** Approved (design)

**Goal:** Add a `probe_grpc` method that actively performs a gRPC health check against a
target — calling the standard `grpc.health.v1.Health/Check` RPC — and reports whether the
service is `SERVING`, the raw health status, and the round-trip latency. It is the gRPC peer
of `probe_http`: the fourth active-traffic probe, closing the gap that neither `probe_tcp`
(port open, but is the app healthy?) nor `probe_http` (gRPC isn't HTTP/1 request/response)
can close for a gRPC workload.

---

## Background and rationale

kato's methods fall into two families:

- **`check_*` / `describe_*` / `list_*`** — passive reads of the Kubernetes API through
  kato's authenticated client (`deps.Kube`).
- **`probe_tcp` / `probe_http` / `probe_dns` / `probe_traceroute`** — active, unauthenticated
  network traffic originated to an arbitrary target, routed through the `Prober` seam
  (`deps.Prober`).

`probe_grpc` is the fifth member of the **probe** family and the analog of `probe_http` for
gRPC services. A gRPC server speaks HTTP/2 + protobuf; `probe_tcp` proves only that the port
accepts a connection, and `probe_http`'s HTTP/1 GET cannot elicit a meaningful health answer
from it. The [gRPC Health Checking Protocol](https://grpc.io/docs/guides/health-checking/)
defines a standard service — `grpc.health.v1.Health` — whose `Check` RPC returns a
`ServingStatus`. The widely-used
[`grpc-health-probe`](https://github.com/grpc-ecosystem/grpc-health-probe) CLI is exactly
this call; `probe_grpc` brings that check in-process, composable into kato UseCases.

### The protocol, briefly

`Health/Check(HealthCheckRequest{service}) -> HealthCheckResponse{status}` where `status` is
one of `SERVING`, `NOT_SERVING`, `UNKNOWN`, or `SERVICE_UNKNOWN`. An empty `service` name
(the convention) queries the **overall server** health rather than a specific registered
service. Two error paths are findings rather than status values: a server that does not
implement the health service at all fails the RPC with gRPC `UNIMPLEMENTED`, and a `Check`
for a service name the server never registered fails with gRPC `NotFound` (the
`SERVICE_UNKNOWN` enum value is only ever returned by the streaming `Watch` RPC, never by
`Check`). `grpc-health-probe` treats only `SERVING` as healthy (exit 0); `probe_grpc`
mirrors that: `success = (status == SERVING)`.

### Scope decisions (from design dialogue)

- **Plaintext + TLS**, mirroring `probe_http`. A `tls` bool param (default `false` = plaintext
  h2c) plus `insecureSkipVerify` for self-signed certs and an optional `serverName` for SNI /
  cert-name override. Covers both in-mesh plaintext gRPC (the common in-cluster case, where a
  sidecar terminates TLS and the app speaks plaintext) and externally-exposed TLS gRPC.
- **Optional `service` name.** Empty (default) = overall server health; set it to check a
  specific registered service. Core to the protocol, so it is a first-class param.
- **Single whole-operation `timeout`** (dial + RPC), default `5s` — consistent with the rest
  of the probe family, which each expose one `timeout`. Not split into separate
  connect-timeout / rpc-timeout as the CLI does; the single bound is simpler and matches the
  family. (Explicitly reconsidered and confirmed in the design dialogue.)
- **`grpc-go` becomes a direct dependency.** There is no reasonable way to speak the health
  protocol without it (HTTP/2 framing + protobuf); hand-rolling it or shelling out to the
  `grpc-health-probe` binary were both rejected (see Alternatives). The health proto
  (`google.golang.org/grpc/health/grpc_health_v1`) ships inside grpc-go, so one dependency
  supplies both the client and the generated types.

### Forward-compatibility

A future `RemoteProber` (noted in `prober.go`) will run probes inside a registered remote
cluster. Because `probe_grpc` dials through `deps.Prober`, it follows that change for free,
the same as the other probes. Nothing multi-cluster is built here.

---

## New dependency

`google.golang.org/grpc` is promoted from an indirect/transitive entry to a **direct**
`require` in `go.mod` (it is already resolvable in the module cache). It brings:

- `google.golang.org/grpc` — the client (`grpc.NewClient`, transport credentials).
- `google.golang.org/grpc/health/grpc_health_v1` — the generated `HealthClient`,
  `HealthCheckRequest`, and the `HealthCheckResponse_ServingStatus` enum.
- `google.golang.org/grpc/credentials` + `credentials/insecure` — TLS and plaintext creds.

`go mod tidy` will move any now-directly-used transitive deps as needed. No other module is
added by the method itself.

---

## `Prober` interface extension

`internal/methods/prober.go` grows one method and two types. `LocalProber` (the only
production implementation) and the test `fakeProber` both implement it.

{% raw %}
```go
type Prober interface {
    ProbeTCP(ctx context.Context, target string, port int, timeout time.Duration) TCPResult
    ProbeHTTP(ctx context.Context, req HTTPProbeRequest) HTTPResult
    ProbeDNS(ctx context.Context, req DNSProbeRequest) DNSResult
    ProbeTraceroute(ctx context.Context, req TracerouteRequest) TracerouteResult
    ProbeGRPC(ctx context.Context, req GRPCProbeRequest) GRPCResult // new
}

// GRPCProbeRequest is a fully-resolved gRPC health probe (params already parsed/defaulted).
type GRPCProbeRequest struct {
    Target             string        // host, IP, or DNS name
    Port               int           // gRPC port
    Service            string        // health service name; "" = overall server health
    TLS                bool          // true = TLS, false = plaintext h2c
    InsecureSkipVerify bool          // skip cert verification (TLS only)
    ServerName         string        // TLS SNI / cert-name override; "" = derive from target
    Timeout            time.Duration // whole-operation bound (dial + Check RPC)
}

// GRPCResult is the outcome of a gRPC health check.
type GRPCResult struct {
    Serving   bool   // health status == SERVING
    Status    string // "SERVING"/"NOT_SERVING"/"UNKNOWN"/"SERVICE_UNKNOWN"; "" if the RPC never completed
    LatencyMS int64  // Check round-trip in ms; -1 on failure
    Err       string // failure reason (dial/timeout/UNIMPLEMENTED); "" on success
}
```
{% endraw %}

### `LocalProber.ProbeGRPC`

{% raw %}
```go
func (LocalProber) ProbeGRPC(ctx context.Context, req GRPCProbeRequest) GRPCResult {
    ctx, cancel := context.WithTimeout(ctx, req.Timeout)
    defer cancel()

    var creds credentials.TransportCredentials
    if req.TLS {
        creds = credentials.NewTLS(&tls.Config{
            InsecureSkipVerify: req.InsecureSkipVerify,
            ServerName:         req.ServerName, // "" -> derived from dial target
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
    resp, err := grpc_health_v1.NewHealthClient(conn).
        Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: req.Service})
    if err != nil {
        // Includes dial failures (grpc.NewClient is lazy; the RPC forces the connect),
        // deadline, and UNIMPLEMENTED (server has no health service).
        return GRPCResult{LatencyMS: -1, Err: err.Error()}
    }
    status := resp.GetStatus().String() // SERVING / NOT_SERVING / UNKNOWN / SERVICE_UNKNOWN
    return GRPCResult{
        Serving:   resp.GetStatus() == grpc_health_v1.HealthCheckResponse_SERVING,
        Status:    status,
        LatencyMS: time.Since(start).Milliseconds(),
    }
}
```
{% endraw %}

- `grpc.NewClient` is lazy — it does not connect until the first RPC — so the `Check` call is
  what actually forces the dial, and a connection failure surfaces as the RPC error. The
  `context.WithTimeout` therefore bounds dial + RPC together (the single-timeout decision).
- A server without the health service returns gRPC `UNIMPLEMENTED`; that is a finding
  (`Serving=false`, `Status=""`, `Err` set), not a method failure.
- `Status` is the raw enum string; `Serving` is the `== SERVING` verdict, kept separate so a
  flow can tell `NOT_SERVING` (reachable but unhealthy) from an unreachable endpoint.

---

## Method: `probe_grpc`

`internal/methods/probe_grpc.go`, mirroring `probe_http`'s shape and reusing the package
helpers `parsePort` and `parseProbeTimeout`.

**Params**

| param | required | default | meaning |
|-------|----------|---------|---------|
| `target` | yes | — | host, IP, or DNS name |
| `port` | yes | — | gRPC port (1-65535) |
| `service` | no | `""` | health service name; empty = overall server health |
| `tls` | no | `"false"` | `"true"` = TLS, `"false"` = plaintext h2c |
| `insecureSkipVerify` | no | `"false"` | `"true"` to accept self-signed certs (TLS only) |
| `serverName` | no | `""` | TLS SNI / cert-name override; empty = derived from `target` |
| `timeout` | no | `"5s"` | whole-operation timeout (Go duration string; > 0) |

**Outputs** (all scalars always present)

| output | type | meaning |
|--------|------|---------|
| `success` | bool | health status is `SERVING` |
| `status` | string | `SERVING`/`NOT_SERVING`/`UNKNOWN`/`SERVICE_UNKNOWN`; `""` if the RPC never completed |
| `latencyMs` | int | Check round-trip in ms; `-1` on failure |
| `error` | string | failure reason (dial/timeout/UNIMPLEMENTED); `""` on success |

`status` is the gRPC analog of `probe_http`'s `statusCode`: `status != ""` means the endpoint
was reachable and answered the health RPC, while `success` is specifically `SERVING`. So
`NOT_SERVING` reads as "reachable but unhealthy" — parallel to HTTP's reachable-but-wrong-status.

**Run**

{% raw %}
```go
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
```
{% endraw %}

---

## Error-handling semantics

A failed check is a **finding, not a method failure** — consistent with the rest of the probe
family:

- **Dial failure / timeout / connection refused / `UNIMPLEMENTED` (no health service) /
  `NOT_SERVING` / `SERVICE_UNKNOWN`:** the RPC path returns `success=false` with `status` and
  `error` set as applicable, `latencyMs=-1` on transport failure, and a **nil Go error**. The
  step is recorded `completed`.
- **Non-nil Go error** (step recorded `failed`) only for param mistakes: empty `target`, a
  `port` outside 1–65535 or non-numeric, an unparseable `tls`/`insecureSkipVerify` bool, or an
  invalid/`<= 0` `timeout`. Matches the `parsePort` / `parseProbeTimeout` / `probe_http`
  convention.

---

## RBAC

No change. Probes originate network traffic from kato's pod; reachability is governed by
NetworkPolicy, not Kubernetes RBAC — identical to the other probes.

---

## UseCase: `grpc-connectivity-check.yaml`

`examples/usecases/grpc-connectivity-check.yaml`, mirroring the `tcp-` / `http-connectivity-check`
layering. From kato's own pod: DNS (resolve) → TCP (port open?) → **gRPC health** (when the
port is open) → traceroute (when the port is not), separating "service unhealthy" from
"network path broken."

- **Inputs (all required** — kato has no input defaults and each is referenced in a `with`
  value, so an unresolved `$(inputs.X)` would fail the step): `target`, `port`, `service`,
  `tls`.
- **Steps:**
  - `dns` — `probe_dns` `name: $(inputs.target)`, `summaryFilter: [success, addresses, recordCount, latencyMs, error]`.
  - `tcp` — `probe_tcp` `target`/`port`.
  - `grpc` — `when: $(steps.tcp.success)`, `probe_grpc` with `target`/`port`/`service: $(inputs.service)`/`tls: $(inputs.tls)`, `summaryFilter: [success, status, latencyMs, error]`.
  - `traceroute` — `when: $(steps.tcp.success) == false`, `probe_traceroute` `target`, `maxHops: "20"`, `timeout: "1s"`, `summaryFilter` unset so the full `hops` list reaches the LLM.
- **Summary prompt:** compact numbered checklist in the family style, distinguishing:
  1. `dns.success` false → name doesn't resolve (DNS root cause; explains any TCP failure).
  2. `tcp.success` true → port open; then gRPC:
     - `grpc.success` true (`status` SERVING) → healthy.
     - `grpc.success` false with `status` set → reachable but `NOT_SERVING` (or `UNKNOWN`) — app-level; give the status.
     - `grpc.success` false with `status` empty → the RPC never completed under an open port: not a gRPC server, TLS mismatch (plaintext probed against TLS or vice versa), no health service (`UNIMPLEMENTED`), or the service name isn't registered (`NotFound`). Read the `error`.
  3. `tcp.success` false → port not reachable, gRPC skipped; use traceroute (reached target IP = port down/filtered at host; stopped short = path broken).
- **Header comment** with a `curl` example, e.g.
  `{"target":"grpc.example.com","port":"443","service":"","tls":"true"}`.

---

## Implementation notes

- Files:
  - `go.mod` / `go.sum` — promote `google.golang.org/grpc` to a direct dependency (`go mod tidy`).
  - `internal/methods/prober.go` — add `ProbeGRPC` to the interface, `GRPCProbeRequest`,
    `GRPCResult`, and `LocalProber.ProbeGRPC`; new imports `crypto/tls`,
    `google.golang.org/grpc`, `.../credentials`, `.../credentials/insecure`,
    `.../health/grpc_health_v1`.
  - `internal/methods/probe_grpc.go` — the `probeGRPC` method + `init()` registration.
  - `internal/methods/probe_grpc_test.go` — method-level tests (fake prober).
  - `internal/methods/prober_test.go` — extend `fakeProber` with `ProbeGRPC` (add `grpc GRPCResult`
    and `gotGRPC GRPCProbeRequest` fields); add `LocalProber.ProbeGRPC` integration tests.
  - `examples/usecases/grpc-connectivity-check.yaml` — the new UseCase.
  - `docs/METHOD.md` — add the `probe_grpc` index row and a section under the probes; bump the
    method count.
  - `charts/kato/README.md.gotmpl` — "32 → 33 read-only checks"; add `probe_grpc` to the
    Active probes row; add gRPC to the ready-made UseCases sentence. Regenerate `README.md` and
    `charts/kato/README.md` via `make readme`.
- No change to `cmd/kato/main.go`: `LocalProber{}` satisfies the widened interface once
  `ProbeGRPC` is implemented; no new wiring.

---

## Testing strategy

**Method tests** (`probe_grpc_test.go`) reuse the shared `fakeProber` — no real network:

- **SERVING:** `fakeProber` returns `GRPCResult{Serving: true, Status: "SERVING", LatencyMS: 4}`.
  Assert `success=true`, `status="SERVING"`, `error=""`, nil Go error.
- **NOT_SERVING is a finding:** `GRPCResult{Serving: false, Status: "NOT_SERVING", LatencyMS: 2}`.
  Assert nil Go error, `success=false`, `status="NOT_SERVING"`.
- **Transport failure is a finding:** `GRPCResult{LatencyMS: -1, Err: "connection refused"}`.
  Assert nil Go error, `success=false`, `status=""`, `error="connection refused"`, `latencyMs=-1`.
- **Request passthrough:** assert `fakeProber.gotGRPC` carries the parsed `Target`/`Port`/
  `Service`/`TLS`/`InsecureSkipVerify`/`ServerName`/`Timeout` (defaults: `tls=false`,
  `insecureSkipVerify=false`, timeout `5s` when unset).
- **Param errors:** empty `target`; `port` non-numeric / out of range; `tls` and
  `insecureSkipVerify` non-bool; `timeout` bad / `0s` / negative — each returns a non-nil Go error.

**`LocalProber` tests** (`prober_test.go`) exercise the real client against an in-process
health server — a `grpc.Server` with a `health.Server` registered, listening on a loopback
`net.Listen("tcp", "127.0.0.1:0")` (real dialable address; plaintext):

- **SERVING (overall):** `SetServingStatus("", SERVING)` → `Serving=true`, `Status="SERVING"`,
  `LatencyMS >= 0`.
- **NOT_SERVING:** a service set to `NOT_SERVING`, queried by that name → `Serving=false`,
  `Status="NOT_SERVING"`.
- **Unregistered service (NotFound):** query a service name that was never registered → the
  reference health server fails the `Check` RPC with gRPC `NotFound`, so `Serving=false`,
  `Status=""`, `Err` set, `LatencyMS=-1` (the `SERVICE_UNKNOWN` status is Watch-only; `Check`
  returns `NotFound`).
- **Connection failure:** dial a closed/unused loopback port with a short timeout →
  `Serving=false`, `Status=""`, `Err` set, `LatencyMS=-1`.

`fakeProber` gains `grpc GRPCResult` and `gotGRPC GRPCProbeRequest` fields plus the `ProbeGRPC`
method so it continues to satisfy the widened `Prober` interface. (A TLS-path integration test
is a possible extension but not required for v1; plaintext exercises the full request/response
mapping.)

---

## Alternatives considered (rejected)

- **Hand-roll HTTP/2 + protobuf framing** to avoid the grpc-go dependency. Rejected:
  large, fragile, and reimplements grpc-go poorly.
- **Shell out to the `grpc-health-probe` binary.** Rejected: requires bundling a binary in the
  image, breaks the pure-Go in-process `Prober` model, and cannot be reused by the future
  `RemoteProber`.

---

## Non-goals (explicit)

- **The `Watch` streaming health RPC** — v1 is a single unary `Check`; no streaming.
- **mTLS client certificates** — TLS server verification only (`tls` + `insecureSkipVerify` +
  `serverName`); presenting a client cert is addable later.
- **Split connect-timeout / rpc-timeout** — one whole-operation `timeout`, matching the probe family.
- **Reflection-based arbitrary RPC calls** — `probe_grpc` speaks only the standard health service.
- **Batch / multi-service checks per call** — one `service` per call; use `forEach` to fan out.
{% endraw %}
