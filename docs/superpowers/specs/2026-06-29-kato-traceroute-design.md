# kato Network Probe Method: `probe_traceroute`

**Status:** Approved (design)

**Goal:** Add a fourth active network-probe method — `probe_traceroute` — so a UseCase can
answer "can we reach this endpoint, and how many network hops away is it?" and, when the
endpoint is *not* reached, see *where* the path stops responding. Built in-process behind the
existing `Prober` seam (so a future `RemoteProber` works unchanged), using an **unprivileged
ICMP datagram socket** so kato gains **no new Kubernetes RBAC and no `CAP_NET_RAW`**.

---

## Background and rationale

kato already has three active probes — `probe_tcp`, `probe_http`, `probe_dns` — all of which
are ordinary pod egress behind the `Prober` interface (`internal/methods/prober.go`), governed
by NetworkPolicy, requiring zero special privileges. `probe_traceroute` is the next member of
that `probe_*` family. It differs from its siblings in two ways:

1. **It reads ICMP replies.** Traceroute works by sending probes with an increasing IP TTL and
   reading the ICMP `time-exceeded` messages that intermediate routers return, plus the final
   `echo-reply` from the destination. Reading those replies needs a socket the other probes
   don't use.
2. **It produces a list.** Where `probe_tcp/http/dns` return a single scalar result, traceroute
   naturally yields one record *per hop*. That maps onto kato's list-output + `forEach` pattern
   (the same shape as `list_nodes`): flat scalar "headline" outputs plus a `hops` list.

### Primary diagnostic intent

This method is scoped to **generic reachability + hop count**: the headline outputs are
`success` (did we reach the destination?) and `hopCount` (how many hops away is it?). The full
per-hop path is *secondary* detail — useful when `success=false` to see where replies stop, but
not the main signal. This keeps the method simple and its evidence token-frugal.

### Why the unprivileged ICMP datagram socket (Approach A)

Reading the returning ICMP requires one of:

- **Unprivileged ICMP datagram socket** (`SOCK_DGRAM` / `IPPROTO_ICMP`, via
  `icmp.ListenPacket("udp4", ...)`) — **chosen**. Needs **no `CAP_NET_RAW`** and no new RBAC,
  matching the "ordinary pod egress" ethos of the existing probes. Its one dependency is the
  node sysctl `net.ipv4.ping_group_range`, which must include kato's pod GID. Many distros
  default this open (`0 2147483647`); where it is locked down, the probe degrades to a
  **finding** (`success=false` + a remediation `error`), never a crash.
- **Raw ICMP socket** (`icmp.ListenPacket("ip4:icmp", ...)`) — works regardless of sysctl but
  requires adding `CAP_NET_RAW` to kato's pod securityContext, a real privilege bump that
  contradicts the deliberate zero-privilege stance of the existing probes. **Non-goal** (see
  Non-goals); addable later if a user hits a locked-down sysctl.
- **UDP/TCP-SYN traceroute** (classic Unix style) — still needs a raw socket to *read* the
  returning ICMP, so it buys no privilege win over the raw-socket option and is more complex
  (firewalls frequently drop UDP traceroute). **Rejected.**

The pure-Go `golang.org/x/net/icmp` + `golang.org/x/net/ipv4` packages are already present in
the module (`golang.org/x/net v0.30.0`, currently indirect) and require no cgo.

### The `Prober` seam (forward-compatibility with centralized kato)

As with the other probes, all probing goes through the `Prober` interface so the future
centralized-multi-cluster vision (probes executing inside a registered remote cluster via a
`RemoteProber`) needs no change to this method or any UseCase. The remote execution vehicle
remains a separate project / separate spec — out of scope here.

---

## Method: `probe_traceroute`

An ICMP traceroute to a target: reports whether the destination was reached, how many hops away
it is, and the per-hop path. IPv4 only in v1.

### Params

| param | required | default | meaning |
|-------|----------|---------|---------|
| `target` | yes | — | host, IP, or DNS name (resolved to its first IPv4 address) |
| `maxHops` | no | `30` | maximum TTL to probe before giving up (1–255) |
| `timeout` | no | `2s` | per-hop reply wait (Go duration string). **Worst case** wall time ≈ `maxHops × probesPerHop × timeout` when the target is unreachable — keep the engine step timeout in mind, or lower `maxHops` |
| `probesPerHop` | no | `1` | probes sent per TTL (1–10); `>1` improves accuracy against transient packet loss |
| `resolveNames` | no | `false` | `"true"` to reverse-DNS each responding hop address (adds latency; off by default) |

### Outputs (scalar, all always present)

| output | type | meaning |
|--------|------|---------|
| `success` | bool | destination reached (an `echo-reply` was received from the target) within `maxHops` |
| `hopCount` | int | number of hops to the destination when reached; `-1` if not reached |
| `respondingHops` | int | count of distinct hops (TTLs) that returned any reply — a "how far does it get" signal |
| `destinationIp` | string | resolved IPv4 of `target`; `""` if DNS resolution failed |
| `latencyMs` | int | RTT to the destination measured on the final (reaching) hop, in ms; `-1` if not reached |
| `error` | string | setup failure reason: DNS resolution failure, or ICMP socket creation failure (includes the `net.ipv4.ping_group_range` remediation hint); `""` when the traceroute ran |

### List output: `hops` (forEach-consumable)

One record per probed TTL, in hop order. **All item fields are statically declared** (the list
carries a fixed schema — no dynamic keys), so `$(item.<field>)` references validate cleanly in a
`forEach` step.

| item field | type | meaning |
|------------|------|---------|
| `hop` | int | TTL / hop number (1-based) |
| `address` | string | responding router IP at this TTL; `""` for a silent (`*`) hop |
| `name` | string | reverse-DNS hostname when `resolveNames` is true and a PTR exists; else `""` |
| `rttMs` | int | RTT for this hop in ms; `-1` if no reply (silent hop) |
| `responded` | bool | a reply was received at this TTL (distinguishes a real `*` timeout hop) |
| `reached` | bool | this hop is the destination (an `echo-reply`, i.e. the final hop) |

The `hops` list is consumable only by a UseCase `forEach` step, never by a `when` condition
(matching stays scalar-only). When `probesPerHop > 1`, a hop is summarized to a single record:
`responded`/`address`/`reached` reflect the first reply received, and `rttMs` is that reply's
RTT (`-1` if no probe at that TTL got any reply).

---

## Error-handling semantics (failure-is-a-finding)

Consistent with the existing probes:

- **Non-nil Go error** (step recorded as `failed`) **only** for param mistakes: missing
  `target`; non-integer or out-of-range `maxHops` (1–255) or `probesPerHop` (1–10); invalid
  `timeout` duration; non-boolean `resolveNames`.
- **`success=false` with `error` populated** (step `completed`): DNS resolution failure (target
  does not resolve), or ICMP socket creation failure (sysctl `net.ipv4.ping_group_range` does
  not cover kato's GID — `error` states exactly what to open). The flow continues so later steps
  can gate on `$(steps.<step>.success)`.
- **`success=false` with `error=""`** (step `completed`): the traceroute ran but the destination
  was not reached within `maxHops` (the path dies mid-route). A valid finding — the `hops` list
  shows *where* replies stopped, and `respondingHops` reports how many hops answered before the
  path went silent.

---

## The `Prober` seam

`Prober` gains one method; the request/result types follow the existing `*ProbeRequest` /
`*Result` convention:

```go
// Prober gains:
ProbeTraceroute(ctx context.Context, req TracerouteRequest) TracerouteResult

type TracerouteRequest struct {
    Target       string        // host, IP, or DNS name
    MaxHops      int           // max TTL (1–255)
    Timeout      time.Duration // per-hop reply wait
    ProbesPerHop int           // probes per TTL (1–10)
    ResolveNames bool          // reverse-DNS each hop
}

type TracerouteResult struct {
    Reached       bool          // echo-reply from the destination
    HopCount      int           // hops to destination when reached; -1 otherwise
    DestinationIP string        // resolved IPv4; "" on DNS failure
    LatencyMS     int64         // RTT to destination on the final hop; -1 if not reached
    Hops          []HopResult   // per-TTL records, in hop order
    Err           string        // setup failure (DNS / socket); "" if the traceroute ran
}

type HopResult struct {
    Hop       int    // TTL (1-based)
    Address   string // responding IP; "" for a silent hop
    Name      string // reverse-DNS hostname; "" if unresolved or resolveNames off
    RTTMS     int64  // RTT in ms; -1 if no reply
    Responded bool   // a reply came back at this TTL
    Reached   bool   // this hop is the destination
}
```

`LocalProber.ProbeTraceroute` (in `internal/methods/prober.go`):

1. Resolve `req.Target` to its first IPv4 address (DNS failure → `TracerouteResult{Err: ...}`,
   `Reached=false`, `HopCount=-1`).
2. Open the unprivileged ICMP datagram socket: `icmp.ListenPacket("udp4", "0.0.0.0")`
   (socket-open failure → `Err` populated with the `ping_group_range` remediation hint).
3. For `ttl` from 1 to `MaxHops`: set the IPv4 TTL via `ipv4.PacketConn.SetTTL`; send up to
   `ProbesPerHop` `icmp.Echo` messages (unique ID/seq); read replies within `Timeout`. An
   `echo-reply` matching our ID/seq from the destination ⇒ this hop is `Reached`. A
   `time-exceeded` ⇒ an intermediate hop; attribute it to this TTL (the message embeds the
   original datagram, confirming it is ours). Record one `HopResult` per TTL. **Stop early** as
   soon as the destination is reached.
4. If `ResolveNames`, reverse-DNS each distinct responding address (best-effort; failures leave
   `Name=""`).

`probe_traceroute.Run` parses/validates params, calls `deps.Prober.ProbeTraceroute`, and maps
the result struct to the flat `Outputs` plus the `hops` list (`[]map[string]any`). `respondingHops`
is computed as the count of `HopResult`s with `Responded=true`.

`cmd/kato` already wires `LocalProber{}` into `Deps` — **no change there**.

---

## RBAC and security

- **No new Kubernetes RBAC.** ICMP egress from kato's pod is governed by NetworkPolicy, not
  RBAC.
- **One deployment dependency:** the node sysctl `net.ipv4.ping_group_range` must include kato's
  pod GID for the unprivileged ICMP datagram socket to open. Documented in `docs/METHOD.md`. When
  it does not, the probe surfaces `success=false` + an `error` naming the sysctl — it degrades to
  a finding, never a crash, and never requires `CAP_NET_RAW`.
- **SSRF surface** is identical to the existing probes: the method sends ICMP to whatever a
  UseCase specifies. UseCases are cluster-scoped, admin-authored, watch-time-validated CRDs, so
  UseCase authorship remains the trust boundary — no new boundary is introduced.

---

## Implementation notes

- Files:
  - `internal/methods/prober.go` — add `ProbeTraceroute` to `Prober`, the `TracerouteRequest` /
    `TracerouteResult` / `HopResult` types, and the `LocalProber` implementation.
  - `internal/methods/prober_test.go` — `LocalProber.ProbeTraceroute` tests (skip-guarded, below).
  - `internal/methods/probe_traceroute.go` — the method (params, outputs, `hops` list, `Run`).
  - `internal/methods/probe_traceroute_test.go` — method tests with a fake `Prober`.
  - `go.mod` — promote `golang.org/x/net` from indirect to a direct dependency.
  - `docs/METHOD.md` — add the `probe_traceroute` section and the `ping_group_range` note.
- Reuse `parseProbeTimeout` for `timeout` (but default `2s`, not the shared `5s` — traceroute
  multiplies the per-hop wait by `maxHops`, so a smaller per-hop default is safer); `maxHops` and
  `probesPerHop` get range-checked integer parsing; `resolveNames` uses the existing
  `parseBoolDefault` helper.
- `hopCount` / `latencyMs` use the established `-1`-means-not-applicable convention
  (`latencyMs:-1` in the other probes).
- The per-hop read respects the request `ctx` so an engine step timeout cancels an in-flight
  traceroute cleanly.

---

## Testing strategy

- **`LocalProber.ProbeTraceroute` (real socket, hermetic — no external network):**
  - Traceroute to `127.0.0.1`: expect `Reached=true`, `HopCount=1`, the single hop's
    `Address=127.0.0.1`, `Reached=true`, `RTTMS >= 0`.
  - The real-socket test is **skip-guarded**: if `icmp.ListenPacket("udp4", ...)` fails on the
    runner (locked-down `ping_group_range`), `t.Skip` with the reason so the suite never goes
    flaky on a restricted CI host.
- **`probe_traceroute` method (fake `Prober`, fully hermetic):**
  - Inject a fake `Prober` returning canned `TracerouteResult`s; assert param parsing and
    defaults (`maxHops=30`, `timeout=2s`, `probesPerHop=1`, `resolveNames=false`), correct mapping
    of the result struct to the scalar outputs and the `hops` list, the `respondingHops` count,
    and the `-1` conventions for the not-reached case.
  - Assert param-error paths (missing `target`; bad `maxHops` / `probesPerHop` / `timeout` /
    `resolveNames`) return a non-nil Go error, and that a probe failure (DNS / socket / not
    reached) returns `success=false` with a nil Go error.
- All tests run under `go test ./internal/methods/` with no cluster and no external network.

---

## Example use cases

1. **Egress reachability + hop count** — pod → an external API (`target: api.example.com`).
   Headline on `success` and `hopCount`; gate later steps on `$(steps.trace.success)`.
2. **Ingress upstream path** — traceroute to the istio-ingressgateway external address. When
   `success=false`, `forEach` over `hops` so the LLM summary sees the last responding hop (where
   the path dies).
3. **Reachability triage with `probe_tcp`** — if a `probe_tcp` to `target:port` fails, a
   `probe_traceroute` (gated by `when`) shows whether the path even gets close to the
   destination, distinguishing "service down" from "network path broken".

---

## Non-goals (explicit)

- **`CAP_NET_RAW` / raw ICMP sockets** (Approach B) and **UDP/TCP-SYN traceroute** (Approach C).
  Addable later behind the same method if a locked-down `ping_group_range` proves common.
- **IPv6 traceroute** (`udp6` / `ipv6.PacketConn`) — addable without changing the output
  contract.
- **Per-hop AS number / geolocation / MTU (path-MTU) discovery**, and **Paris-traceroute**
  multipath enumeration.
- **Load / performance characterization** — this is a one-shot reachability probe, not a
  monitoring tool.
- **Centralized multi-cluster execution** — the `RemoteProber` vehicle. The existing `Prober`
  seam is the only concession made here for it; it remains a separate spec.
