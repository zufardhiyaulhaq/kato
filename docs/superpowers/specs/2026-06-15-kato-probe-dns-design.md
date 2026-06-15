# kato DNS Probe Method: `probe_dns`

**Status:** Approved (design)

**Goal:** Add a `probe_dns` method that actively resolves a hostname and reports whether
it resolved, the addresses returned, and the query latency. It belongs to the `probe_*`
family (active network traffic from kato's pod), filling the one gap the `cluster-dns`
UseCase cannot close today: every component is inspected declaratively, but no step ever
performs an actual DNS lookup.

---

## Background and rationale

kato's methods fall into two families:

- **`check_*` / `describe_*` / `list_*`** — passive reads of the Kubernetes API through
  kato's authenticated client (`deps.Kube`).
- **`probe_tcp` / `probe_http`** — active, unauthenticated network traffic originated to an
  arbitrary target, routed through the `Prober` seam (`deps.Prober`).

`probe_dns` is the third member of the **probe** family. It originates a DNS query rather
than a TCP/HTTP connection, so it goes through `deps.Prober` exactly like the others. The
30-step `cluster-dns` UseCase inspects CoreDNS, node-local-dns, the Corefile, the HPA, and
the kube-dns Service — but never resolves a name. `probe_dns` is the active end-to-end check
that confirms resolution actually works, and (via the optional `server` param) which resolver
layer is responsible when it doesn't.

### Scope decisions (from design dialogue)

- **A/AAAA only, resolves-or-not.** The method resolves a hostname to IP addresses via
  `LookupHost` (which returns both A and AAAA records as strings) and asserts only that at
  least one address came back. No SRV/CNAME/TXT/PTR, no expected-address assertion in v1 —
  these are addable later without changing the shape.
- **System resolver by default, optional explicit server.** With no `server`, the query uses
  the pod's configured resolver (`/etc/resolv.conf`) — what real workloads experience. With
  `server` set (e.g. the node-local-dns link-local IP `169.254.20.10`, or the kube-dns
  clusterIP), the query is forced to that resolver, isolating which layer is broken.
- **stdlib `net.Resolver`.** No new dependency; consistent with `LocalProber`'s
  dependency-free style.
- **Single name per call.** To resolve several names, fan out with `forEach`.

### Forward-compatibility

A future `RemoteProber` (noted in `prober.go`) will run probes inside a registered remote
cluster. Because `probe_dns` resolves through `deps.Prober`, it follows that change for free,
the same as `probe_tcp`/`probe_http`. Nothing multi-cluster is built here.

---

## `Prober` interface extension

`internal/methods/prober.go` grows one method and two types. `LocalProber` (the only
production implementation) and the test `fakeProber` both implement it.

{% raw %}
```go
type Prober interface {
    ProbeTCP(ctx context.Context, target string, port int, timeout time.Duration) TCPResult
    ProbeHTTP(ctx context.Context, req HTTPProbeRequest) HTTPResult
    ProbeDNS(ctx context.Context, req DNSProbeRequest) DNSResult // new
}

// DNSProbeRequest is a fully-resolved DNS probe (params already parsed/defaulted).
type DNSProbeRequest struct {
    Name    string        // hostname to resolve
    Server  string        // optional resolver IP; "" = pod's /etc/resolv.conf
    Port    int           // resolver port (default 53; only used when Server set)
    Timeout time.Duration
}

// DNSResult is the outcome of a DNS resolution probe.
type DNSResult struct {
    Resolved  bool     // got >= 1 address
    Addresses []string // resolved A/AAAA IPs, sorted
    LatencyMS int64    // query time in ms; -1 on failure
    Err       string   // failure reason (NXDOMAIN/timeout/unreachable); "" on success
}
```
{% endraw %}

### `LocalProber.ProbeDNS`

{% raw %}
```go
func (LocalProber) ProbeDNS(ctx context.Context, req DNSProbeRequest) DNSResult {
    resolver := net.DefaultResolver
    if req.Server != "" {
        d := net.Dialer{Timeout: req.Timeout}
        server := net.JoinHostPort(req.Server, strconv.Itoa(req.Port))
        resolver = &net.Resolver{
            PreferGo: true,
            // Force every query to the specified server over the requested
            // network (udp, then tcp on truncation), ignoring resolv.conf.
            Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
                return d.DialContext(ctx, network, server)
            },
        }
    }
    ctx, cancel := context.WithTimeout(ctx, req.Timeout)
    defer cancel()
    start := time.Now()
    addrs, err := resolver.LookupHost(ctx, req.Name)
    if err != nil {
        return DNSResult{Resolved: false, LatencyMS: -1, Err: err.Error()}
    }
    sort.Strings(addrs)
    return DNSResult{Resolved: true, Addresses: addrs, LatencyMS: time.Since(start).Milliseconds()}
}
```
{% endraw %}

- `LookupHost` returns both A and AAAA addresses as strings — exactly "A/AAAA, resolves-or-not".
- A name that resolves to zero records returns an error (NXDOMAIN / "no such host"), so the
  empty-result case is the failure path.
- Addresses are sorted so the `addresses` output is stable across calls.
- The custom-server branch sets `PreferGo: true` so Go's resolver (not cgo) honors the
  `Dial`, guaranteeing the query reaches the requested server.

---

## Method: `probe_dns`

`internal/methods/probe_dns.go`, mirroring `probe_tcp`'s shape and reusing the package
helpers `parsePort` and `parseProbeTimeout`.

**Params**

| param | required | default | meaning |
|-------|----------|---------|---------|
| `name` | yes | — | hostname to resolve (e.g. `kubernetes.default.svc.cluster.local`) |
| `server` | no | `""` | DNS server IP to query directly; empty = pod's configured resolver |
| `port` | no | `53` | DNS server port; only used when `server` is set |
| `timeout` | no | `5s` | query timeout (Go duration string; > 0), applied via `context.WithTimeout` |

**Outputs** (all scalars always present)

| output | type | meaning |
|--------|------|---------|
| `success` | bool | resolution returned at least one address |
| `addresses` | string | comma-separated resolved IPs (A/AAAA), sorted; `""` if none |
| `recordCount` | int | number of addresses resolved |
| `latencyMs` | int | query time in ms; `-1` on failure |
| `error` | string | failure reason (NXDOMAIN/timeout/unreachable); `""` on success |

There is no separate `resolved` output: given resolves-or-not, it would be identical to
`success`. `success` is the name for cross-probe consistency, so a flow gates later steps on
`$(steps.dns.success)` the same way it does for `probe_tcp`/`probe_http`.

**Run**

{% raw %}
```go
func (probeDNS) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
    if params["name"] == "" {
        return nil, fmt.Errorf("param name: required")
    }
    port := 53
    if v := params["port"]; v != "" {
        p, err := parsePort(v) // validates 1-65535
        if err != nil {
            return nil, err
        }
        port = p
    }
    timeout, err := parseProbeTimeout(params["timeout"]) // unset -> 5s, else > 0
    if err != nil {
        return nil, err
    }
    res := deps.Prober.ProbeDNS(ctx, DNSProbeRequest{
        Name: params["name"], Server: params["server"], Port: port, Timeout: timeout,
    })
    return Outputs{
        "success":     res.Resolved,
        "addresses":   strings.Join(res.Addresses, ", "),
        "recordCount": int64(len(res.Addresses)),
        "latencyMs":   res.LatencyMS,
        "error":       res.Err,
    }, nil
}
```
{% endraw %}

---

## Error-handling semantics

A failed lookup is a **finding, not a method failure** — consistent with `probe_tcp`:

- **NXDOMAIN / timeout / server unreachable:** `success=false`, `recordCount=0`,
  `latencyMs=-1`, `error` populated, **nil Go error**. The step is recorded `completed`.
- **Non-nil Go error** (step recorded `failed`) only for param mistakes: empty `name`, a
  `port` outside 1–65535 or non-numeric, or an invalid/`<= 0` `timeout`. Matches the
  `parsePort` / `parseProbeTimeout` convention.

---

## RBAC

No change. Probes originate network traffic from kato's pod; reachability is governed by
NetworkPolicy, not Kubernetes RBAC — identical to `probe_tcp`/`probe_http`.

---

## Implementation notes

- Files:
  - `internal/methods/prober.go` — add `ProbeDNS` to the interface, `DNSProbeRequest`,
    `DNSResult`, and `LocalProber.ProbeDNS`.
  - `internal/methods/probe_dns.go` — the `probeDNS` method + `init()` registration.
  - `internal/methods/probe_dns_test.go` — method-level tests (fake prober).
  - `internal/methods/prober_test.go` — extend `fakeProber` with `ProbeDNS`; add
    `LocalProber.ProbeDNS` tests.
  - `docs/METHOD.md` — add the `probe_dns` index row and a section under the probes.
- No change to `cmd/kato/main.go`: `LocalProber{}` already satisfies the widened interface
  once `ProbeDNS` is implemented; no new dependency, no new wiring.

---

## Testing strategy

**Method tests** (`probe_dns_test.go`) reuse the shared `fakeProber` — no real network:

- **Success:** `fakeProber` returns `DNSResult{Resolved: true, Addresses: []string{"10.0.0.5", "10.0.0.6"}, LatencyMS: 3}`.
  Assert `success=true`, `addresses="10.0.0.5, 10.0.0.6"`, `recordCount=2`, `error=""`, nil Go error.
- **Failure is a finding:** `fakeProber` returns `Resolved: false, LatencyMS: -1, Err: "no such host"`.
  Assert no Go error, `success=false`, `recordCount=0`, `error="no such host"`.
- **Request passthrough:** assert `fakeProber.gotDNS` carries the parsed `Name`/`Server`/`Port`/`Timeout`
  (default port `53` when unset; the given port otherwise).
- **Param errors:** empty `name`; `port` non-numeric / out of range; `timeout` bad / `0s` /
  negative — each returns a non-nil Go error.

**`LocalProber` tests** (`prober_test.go`) exercise the real resolver:

- **Resolves:** `LookupHost(ctx, "localhost")` returns ≥1 of `127.0.0.1`/`::1` → `Resolved=true`,
  `LatencyMS >= 0`.
- **NXDOMAIN:** a name in the RFC-guaranteed-nonexistent `.invalid` TLD (e.g.
  `nonexistent.invalid`) → `Resolved=false`, `Err` set, `LatencyMS=-1`.
- **Custom server path:** point `Server` at `127.0.0.1` and a closed/unused port with a short
  timeout; assert the failure surfaces (exercises the custom-resolver `Dial` branch without a
  live DNS server).

`fakeProber` gains `dns DNSResult` and `gotDNS DNSProbeRequest` fields plus the `ProbeDNS`
method so it continues to satisfy the widened `Prober` interface.

---

## Non-goals (explicit)

- **SRV / CNAME / TXT / PTR record types** — A/AAAA only in v1; a `type` param is addable later.
- **`expectAddress` assertion** (resolves to a specific IP, e.g. verifying kube-dns clusterIP) —
  not in v1; `success` is resolves-or-not.
- **Batch resolution** of multiple names per call — use `forEach`.
- **Returning rcodes / raw DNS metadata** — only `success`/`addresses`/`recordCount` plus
  latency and error.
- **A separate `resolved` output** — redundant with `success` under resolves-or-not.
