# kato Network Probe Methods: `probe_tcp` + `probe_http`

**Status:** Approved (design)

**Goal:** Add two active network-probe methods — `probe_tcp` (TCP connect / "telnet" check)
and `probe_http` (HTTP(S) request with status/body assertions) — so a UseCase can verify
black-box that a service, database, or host is reachable and serving (e.g. "is the DB
accepting connections on 5432?", "does the health endpoint return 200?"). Built in-process
behind a `Prober` seam so a remote-execution backend can be added later for the centralized
multi-cluster vision without changing the methods or any UseCase.

---

## Background and rationale

kato's existing methods (`check_*`, `describe_*`, `list_*`) all **read the Kubernetes API**
— they are passive. These two new methods are the first that **actively originate network
traffic** to a target. That distinction is reflected in a new verb prefix: `probe_`.

### Why in-process, not k6, not the API proxy

Three approaches were considered and rejected for this goal:

- **Kubernetes API server service-proxy** (`/api/v1/namespaces/{ns}/services/{name}:{port}/proxy/...`):
  HTTP-only and **name-only**. Cannot do a raw TCP connect (the DB-port case) and cannot
  target an arbitrary IP or an off-cluster endpoint. Too narrow.
- **k6 (Grafana)**: a *load-testing* tool, not a readiness checker. It does not solve the
  real problem (where the probe executes); it would still need a Job/agent to run inside a
  remote cluster. Core k6 has **no native raw TCP** (needs a custom `xk6` build), and its
  VU/threshold machinery is pure overhead for a one-shot readiness check. Out of scope unless
  load testing becomes a goal (it is not).
- **In-process `net/http` + `net.DialTimeout`** (chosen): for the current deployment model
  (kato runs *inside* the target cluster) this reaches everything required — ClusterIPs, pod
  IPs, service DNS, and external endpoints — subject only to NetworkPolicy, with **zero new
  Kubernetes RBAC** (it is ordinary pod egress).

### The `Prober` seam (forward-compatibility with centralized kato)

There is a future vision of a centralized kato managing multiple clusters via a
`KubernetesCluster` registration CRD. In that world a probe must execute *inside* the
(remote) target cluster, because central kato cannot route to a remote cluster's internal
network. The **probe semantics are identical** regardless of where the probe runs; only the
execution vehicle changes. To keep the methods forward-compatible, all probing goes through a
`Prober` interface. Today the only implementation is `LocalProber` (in-process). When
centralization lands, a `RemoteProber` (agent or Job backend) implements the same interface,
and these methods plus every UseCase using them stay byte-identical.

The centralized `KubernetesCluster` CRD and the remote execution vehicle are explicitly a
**separate project / separate spec** — out of scope here.

---

## Method 1: `probe_tcp`

A TCP connect check: does `target:port` accept a connection within the timeout?

**Params**

| param | required | default | meaning |
|-------|----------|---------|---------|
| `target` | yes | — | host, IP, or DNS name (e.g. `postgres.data.svc.cluster.local`, `10.0.0.5`, `db.example.com`) |
| `port` | yes | — | TCP port (integer string; 1–65535) |
| `timeout` | no | `5s` | connect timeout (Go duration string); bounded by the engine step timeout |

**Outputs** (all always present)

| output | type | meaning |
|--------|------|---------|
| `success` | bool | TCP connection established within the timeout |
| `latencyMs` | int | time to establish the connection in milliseconds; `-1` on failure |
| `error` | string | failure reason (connection refused, timeout, DNS resolution failure); `""` on success |

---

## Method 2: `probe_http`

An HTTP(S) request with status and optional body assertions.

**Params**

| param | required | default | meaning |
|-------|----------|---------|---------|
| `target` | yes | — | host, IP, or DNS name |
| `port` | yes | — | port (integer string; 1–65535) |
| `scheme` | no | `http` | `http` \| `https` |
| `path` | no | `/` | request path (a leading `/` is added if missing) |
| `expectStatus` | no | `200` | expected HTTP status code (integer string) |
| `expectBodyContains` | no | `""` | substring the response body must contain (matched within the first 64 KiB of the response; see Implementation notes); empty = no body assertion |
| `insecureSkipVerify` | no | `false` | accept self-signed / internal TLS certs (https only) |
| `timeout` | no | `5s` | total request timeout (Go duration string) |

The request method is **GET** (not configurable in v1). Custom request headers are not
supported in v1 (see Non-goals).

**Outputs** (all always present)

| output | type | meaning |
|--------|------|---------|
| `success` | bool | `statusMatched` **and** `bodyMatched` (i.e. the probe passed) |
| `statusCode` | int | HTTP status code; `0` if no response was received (transport failure) |
| `statusMatched` | bool | `statusCode == expectStatus` |
| `bodyMatched` | bool | response body contains `expectBodyContains` (true when `expectBodyContains` is empty) |
| `latencyMs` | int | full request round-trip in milliseconds; `-1` on failure |
| `error` | string | failure reason (DNS failure, connection refused, timeout, TLS error); `""` on success |

**The response body is never an output** — only `statusCode` / `statusMatched` /
`bodyMatched`. This keeps response payloads (which may contain secrets) out of the Run CR and
the LLM evidence. The body is read only to evaluate `bodyMatched`, then discarded; the read
is size-bounded (see Implementation notes).

---

## Error-handling semantics

A network-level failure is a **finding, not a method failure**:

- Connection refused, timeout, DNS resolution failure, TLS verification failure, or a
  non-matching status/body → the method returns `success=false` with `error` populated and a
  **nil Go error**. The step is recorded as `completed`, and the flow continues so later steps
  can gate on `$(steps.<step>.success)`. This mirrors how `list_failing_pods` treats "nothing
  matched" as a result rather than an error.
- The method returns a **non-nil Go error** (recorded as a `failed` step) only for param
  mistakes: missing/invalid `port`, invalid `timeout` duration, an unknown `scheme`, or an
  invalid `expectStatus`. This matches the `parseMaxLineLength`/`parseFailCriteria` convention
  (invalid params are errors; expected outcomes are outputs).

---

## The `Prober` seam

A single interface with one method per protocol, injected via `methods.Deps` (the same
dependency-injection pattern as `Deps.Kube` / `Deps.Metrics`):

```go
// Prober performs active network probes. LocalProber is the in-process default;
// a future RemoteProber will run probes inside a registered remote cluster.
type Prober interface {
    ProbeTCP(ctx context.Context, target string, port int, timeout time.Duration) TCPResult
    ProbeHTTP(ctx context.Context, req HTTPProbeRequest) HTTPResult
}

type TCPResult struct {
    Success   bool
    LatencyMS int64
    Err       string
}

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

type HTTPResult struct {
    StatusCode    int
    StatusMatched bool
    BodyMatched   bool
    LatencyMS     int64
    Err           string
}
```

- `methods.Deps` gains a `Prober Prober` field.
- `LocalProber` (in `internal/methods/prober.go`) implements both methods using
  `net.DialTimeout("tcp", net.JoinHostPort(target, port), timeout)` and an `http.Client` with
  the configured timeout and (for https) a `tls.Config{InsecureSkipVerify: ...}`.
- `cmd/kato` wires `LocalProber{}` into the `Deps` it builds.
- `probe_tcp.Run` / `probe_http.Run` parse and validate params, call the matching `Prober`
  method, and map the result struct to the flat `Outputs`.

---

## RBAC and security

- **No new Kubernetes RBAC.** TCP/HTTP egress from kato's pod is governed by NetworkPolicy,
  not RBAC. Document that the probe targets must be reachable from kato's pod (a restrictive
  NetworkPolicy can block them); a blocked probe surfaces as `success=false` with a timeout
  `error`.
- **SSRF surface:** these methods connect to whatever a UseCase specifies. UseCases are
  cluster-scoped, admin-authored, watch-time-validated CRDs, so UseCase authorship is already
  the trust boundary — no new boundary is introduced. Noted for operators who delegate UseCase
  creation.

---

## Implementation notes

- Files:
  - `internal/methods/prober.go` — `Prober` interface, `LocalProber`, request/result types.
  - `internal/methods/prober_test.go` — `LocalProber` tests.
  - `internal/methods/probe_tcp.go` + `internal/methods/probe_http.go` — the two methods.
  - `internal/methods/probe_tcp_test.go` + `internal/methods/probe_http_test.go` — method tests.
  - `internal/methods/method.go` — add `Prober` to `Deps`.
  - `cmd/kato/main.go` — wire `LocalProber` into `Deps`.
  - `docs/METHOD.md` — add `probe_tcp` and `probe_http` sections.
- `LocalProber.ProbeHTTP` reads at most a bounded prefix of the response body (e.g. 64 KiB,
  reusing the existing `defaultLogBytes` bound) when evaluating `expectBodyContains`, so a
  large/streaming body cannot exhaust memory. The body is never retained past the match check.
- `latencyMs` is measured around the connect (TCP) or the full `Do` round-trip (HTTP).
- `port` is validated to the range 1–65535; `timeout` is parsed with `time.ParseDuration`;
  `scheme` must be `http` or `https`; `expectStatus` must parse as an integer.

---

## Testing strategy

- **`LocalProber` (real I/O, no network dependency on external hosts):**
  - HTTP: stand up `httptest.NewServer` / `httptest.NewTLSServer`; assert status match,
    status mismatch, body-contains match/miss, timeout (slow handler), and (TLS)
    `insecureSkipVerify` true vs false against the test server's self-signed cert.
  - TCP: `net.Listen("tcp", "127.0.0.1:0")` for the connect-success case; a closed/unused
    port for connection-refused; assert `success`, `latencyMs >= 0`, and `error` content.
- **`probe_tcp` / `probe_http` methods (fake `Prober`, no real network):**
  - Inject a fake `Prober` returning canned results; assert param parsing, defaults
    (`timeout=5s`, `scheme=http`, `path=/`, `expectStatus=200`), correct mapping of result
    structs to outputs, and the `success` composition (`statusMatched && bodyMatched`).
  - Assert the param-error paths (missing/invalid `port`, bad `timeout`, unknown `scheme`,
    non-integer `expectStatus`) return a non-nil Go error, and that a probe failure returns
    `success=false` with a nil Go error.
- All tests run under `go test ./internal/methods/` with no external network or cluster.

---

## Non-goals (explicit)

- **Load / performance testing** (RPS, latency percentiles, virtual users, thresholds) and
  any k6 integration.
- **Centralized multi-cluster execution** — the `KubernetesCluster` CRD and the remote probe
  vehicle (persistent agent vs Job-per-probe). Separate spec. The `Prober` seam is the only
  concession made here for it.
- **Custom HTTP request headers** and **non-GET methods** (addable later without changing the
  output contract).
- **Raw arbitrary-IP probing via the API proxy**, **gRPC/DNS/TLS-specific probes** (future
  `probe_*` methods if needed).
- Returning the raw response body as an output.
