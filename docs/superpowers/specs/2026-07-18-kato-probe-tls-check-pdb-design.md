# kato Methods: `probe_tls` and `check_pdb`
{% raw %}
**Status:** Approved (design)

**Goal:** Add two methods. `probe_tls` actively performs a TLS handshake against a target,
reports whether the certificate chain is valid, and — critically — surfaces certificate
facts (expiry, issuer, SANs) *even when the chain is invalid*, closing the cert-expiry
outage class that `probe_http` cannot see. `check_pdb` reads a PodDisruptionBudget by name
and reports whether it currently permits any voluntary disruption — the standard root cause
of "the node drain hangs forever."

A third candidate, `probe_udp`, was considered and **explicitly rejected** in the design
dialogue: UDP has no handshake, so silence is indistinguishable from a firewall drop, and a
`success` verdict built on "not proven closed" cannot be made trustworthy. No UDP probe.

---

## Method 1: `probe_tls`

### Background and rationale

The sixth member of the active-probe family (`probe_tcp`, `probe_http`, `probe_dns`,
`probe_traceroute`, `probe_grpc` in progress). `probe_http` with `scheme: https` proves an
HTTPS endpoint answers, but it either fails opaquely on a bad certificate or (with
`insecureSkipVerify`) ignores the certificate entirely — it never *reports on* the
certificate. Certificate expiry is a recurring, fully-preventable outage class; `probe_tls`
makes it observable and gateable from a UseCase.

### The capture-then-verify mechanism

The central design decision: the prober **always handshakes with verification disabled**
(`tls.Config{InsecureSkipVerify: true}`) so the peer certificate is captured even when it is
expired, self-signed, or name-mismatched — then runs **manual verification**
(`leaf.Verify` with the presented intermediates, against the system root pool, with the
hostname check against `serverName` or `target`). Facts and verdict are therefore
independent:

| situation | handshakeComplete | facts | verified |
|---|---|---|---|
| not TLS / refused / timeout | `false` | absent (defaults) | `false` |
| handshake OK, chain bad | `true` | present | `false` (+ `verifyError`) |
| handshake OK, chain good | `true` | present | `true` |

### Verdict semantics (design-dialogue decisions)

- **`success = handshakeComplete && verified`** (default). "TLS is healthy here" in one
  bool, matching `probe_http`'s reached-AND-matched convention.
- **`insecureSkipVerify: "true"` relaxes the verdict to `success = handshakeComplete &&
  !expired`.** Rationale: in-cluster services commonly use an internal CA that can never
  verify against system roots; without this, `success` would be permanently false for them.
  Chain verification is excluded from the *verdict* only — `verified` and `verifyError` are
  still reported as facts. Expiry still fails the verdict because expiry monitoring is this
  probe's primary job.
- **No expiry-threshold param.** `daysUntilExpiry` is an int output; flows gate with CEL:
  `when: $(steps.tls.daysUntilExpiry) < 30`. A `warnDays` param would duplicate what `when`
  already does.
- **`daysUntilExpiry` is negative when expired** (floor of time-until-`notAfter` in days).
  It is meaningful only when `handshakeComplete` is `true`; its always-present default is
  `0`, so flows should gate expiry logic on `handshakeComplete`.

### `Prober` interface extension

`internal/methods/prober.go` grows one method and two types. `LocalProber` and the test
`fakeProber` both implement it.

```go
type Prober interface {
    // ... existing five ...
    ProbeTLS(ctx context.Context, req TLSProbeRequest) TLSResult
}

// TLSProbeRequest is a fully-resolved TLS probe (params already parsed/defaulted).
type TLSProbeRequest struct {
    Target     string        // host, IP, or DNS name
    Port       int           // TLS port
    ServerName string        // SNI + hostname to verify; "" = Target
    Timeout    time.Duration // whole-operation bound (dial + handshake)
}

// TLSResult is the outcome of a TLS handshake probe. Verdict composition
// (success from verified/expired per insecureSkipVerify) happens in the method,
// not here — the prober reports raw facts.
type TLSResult struct {
    HandshakeComplete bool
    Verified          bool   // chain + hostname verify against roots
    VerifyError       string // why verification failed; "" when Verified
    Expired           bool
    DaysUntilExpiry   int64  // negative if expired; 0 when no cert
    NotAfter          string // RFC3339; "" when no cert
    Issuer            string // leaf issuer CN
    Subject           string // leaf subject CN
    DNSNames          string // comma-separated leaf SANs
    TLSVersion        string // e.g. "TLS1.3"
    LatencyMS         int64  // dial+handshake in ms; -1 on failure
    Err               string // transport/handshake failure; "" otherwise
}
```

`LocalProber` gains an optional field:

```go
type LocalProber struct {
    // RootCAs overrides the root pool used by ProbeTLS's manual verification.
    // nil = system roots. Set only by tests (verification against a test CA
    // without touching system trust). Production wiring passes the zero value.
    RootCAs *x509.CertPool
}
```

This is the test seam for the verification path; `cmd/kato/main.go` is unchanged
(`methods.LocalProber{}` zero value still compiles and behaves identically).

**`LocalProber.ProbeTLS` implementation sketch:**

1. `tls.Dialer` with `Config{InsecureSkipVerify: true, ServerName: sni}` where `sni` is
   `ServerName` or `Target` (for an IP target with no override, `ServerName` stays empty —
   Go omits SNI for IPs); bound by `context.WithTimeout(req.Timeout)`.
2. Dial/handshake error → `TLSResult{LatencyMS: -1, Err: ...}` (`HandshakeComplete: false`).
3. On success: take `ConnectionState()`. Leaf = `PeerCertificates[0]`; facts from it
   (`NotAfter` → RFC3339 + `DaysUntilExpiry` floor + `Expired`, issuer/subject CN, SANs
   joined, `tls.VersionName(state.Version)`), `LatencyMS` from dial+handshake wall time.
4. Manual verify: `x509.VerifyOptions{Roots: p.RootCAs (nil = system), DNSName: verifyName,
   Intermediates: pool(PeerCertificates[1:])}` → `Verified` / `VerifyError`. `verifyName` is
   `ServerName` or `Target` (hostname check runs against IP targets too — Go's `Verify`
   handles IP SANs).
5. Close the connection; return.

### Method: `probe_tls`

`internal/methods/probe_tls.go`, mirroring `probe_http`'s shape; reuses `parsePort` and
`parseProbeTimeout`.

**Params**

| param | required | default | meaning |
|---|---|---|---|
| `target` | yes | — | host, IP, or DNS name |
| `port` | yes | — | TLS port (1–65535) |
| `serverName` | no | `""` | SNI + hostname to verify; empty = derived from `target` |
| `insecureSkipVerify` | no | `"false"` | `"true"` = exclude chain/name verification from `success` (expiry still counts) |
| `timeout` | no | `"5s"` | whole-operation timeout (Go duration; > 0) |

**Outputs** (all scalars, always present)

| output | type | default | meaning |
|---|---|---|---|
| `success` | bool | `false` | `handshakeComplete && verified`; with `insecureSkipVerify`: `handshakeComplete && !expired` |
| `handshakeComplete` | bool | `false` | a TLS handshake completed (cert facts are meaningful) |
| `verified` | bool | `false` | chain + hostname verified against roots |
| `expired` | bool | `false` | leaf cert past `notAfter` |
| `daysUntilExpiry` | int | `0` | days until leaf `notAfter` (floor); negative if expired; gate on `handshakeComplete` |
| `notAfter` | string | `""` | leaf expiry, RFC3339 |
| `issuer` | string | `""` | leaf issuer CN |
| `subject` | string | `""` | leaf subject CN |
| `dnsNames` | string | `""` | comma-separated leaf SANs |
| `tlsVersion` | string | `""` | negotiated version, e.g. `TLS1.3` |
| `verifyError` | string | `""` | why chain verification failed |
| `latencyMs` | int | `-1` | dial + handshake in ms |
| `error` | string | `""` | transport/handshake failure reason |

**Run:** parse params (`target` required; `parsePort`; `parseProbeTimeout`;
`strconv.ParseBool` for `insecureSkipVerify`), call `deps.Prober.ProbeTLS`, compose
`success` from the result per the verdict rule, map fields 1:1 otherwise.

**Error semantics:** a failed handshake, invalid chain, or expired cert is a **finding**
(`success: false`, step `completed`), never a method error. Non-nil Go error (step
`failed`) only for param mistakes: empty `target`, bad `port`, unparseable
`insecureSkipVerify`, invalid/`<= 0` `timeout` — the `probe_http` convention exactly.

**RBAC:** none. Network reachability governed by NetworkPolicy, like all probes.

---

## Method 2: `check_pdb`

### Background and rationale

A PodDisruptionBudget with `status.disruptionsAllowed: 0` silently blocks every voluntary
eviction — `kubectl drain`, node-pool upgrades, the descheduler — and nothing in kato's
current library can see it. The semantics trap (documented in METHOD.md): a PDB gates
**evictions**, not rolling updates, so `blocked: true` explains "the drain hangs," not "the
rollout hangs."

### Design decisions (from dialogue)

- **By-name lookup** (`namespace` + `name`), mirroring `check_hpa`/`check_pvc`. A
  selector/workload-based discovery mode was considered and rejected for v1: no existing
  method takes that shape, and runbooks know their PDB names. A missing PDB is
  `exists: false` — a finding, not an error ("no PDB protects this workload" is itself the
  answer to "why did the drain evict everything?").
- `minAvailable`/`maxUnavailable` are **string** outputs because the API type is
  IntOrString (`"2"` or `"50%"`); rendered via `.String()`, `""` when unset.

### Method: `check_pdb`

`internal/methods/pdb.go` (resource-named file convention: `hpa.go`, `pvc.go`, …). Client
call: `deps.Kube.PolicyV1().PodDisruptionBudgets(namespace).Get(ctx, name, ...)`;
`IsNotFound` → the `exists: false` outputs; any other API error → method error (step
`failed`, a finding for the flow).

**Params:** `namespace` (required), `name` (required).

**Outputs** (all scalars, always present)

| output | type | default | meaning |
|---|---|---|---|
| `exists` | bool | `false` | PDB exists |
| `minAvailable` | string | `""` | `spec.minAvailable` (int-or-percent, e.g. `"2"`, `"50%"`) |
| `maxUnavailable` | string | `""` | `spec.maxUnavailable` (int-or-percent) |
| `selector` | string | `""` | `spec.selector` matchLabels, rendered `k=v, k=v` (via `renderKVMap`) |
| `expectedPods` | int | `0` | `status.expectedPods` |
| `currentHealthy` | int | `0` | `status.currentHealthy` |
| `desiredHealthy` | int | `0` | `status.desiredHealthy` |
| `disruptionsAllowed` | int | `0` | `status.disruptionsAllowed` |
| `blocked` | bool | `false` | `exists && disruptionsAllowed == 0` — no voluntary disruption possible right now |
| `conditionReason` | string | `""` | reason of the `DisruptionAllowed` condition when False (e.g. `InsufficientPods`) |

**RBAC:** add one rule to `charts/kato/templates/rbac.yaml`: apiGroup `policy`, resource
`poddisruptionbudgets`, verbs `get,list,watch`.

---

## Example UseCases

- **`examples/usecases/tls-certificate-check.yaml`** — probe-family layering, inputs
  `target` + `port` (both required):
  - `dns` — `probe_dns` on `$(inputs.target)`.
  - `tcp` — `probe_tcp`.
  - `tls` — `probe_tls`, `when: $(steps.tcp.success)`.
  - Summary prompt distinguishes: name doesn't resolve → DNS root cause; port closed →
    reachability; handshake fails under an open port → not a TLS endpoint; chain invalid →
    read `verifyError`; valid but `daysUntilExpiry` low → renewal warning with the number.
  - Header comment with a `curl` example (`{"target":"example.com","port":"443"}`).
- **`examples/usecases/deployment-disruption-check.yaml`** — inputs `namespace`,
  `deployment`, `pdb` (all required):
  - `deployment` — `check_deployment_status`.
  - `pdb` — `check_pdb`.
  - Summary prompt: `blocked` + `conditionReason: InsufficientPods` → "the PDB permits zero
    evictions because only currentHealthy of desiredHealthy pods are healthy; fix the
    unready pods before draining"; `exists: false` → "no PDB — drains will evict freely";
    healthy deployment + `blocked` → over-tight budget (e.g. `maxUnavailable: 0`).

---

## Docs, counts, chart

- `docs/METHOD.md` — index rows for both; `probe_tls` section under **Probes** (after
  `probe_traceroute`); `check_pdb` section under **Workloads** (after `check_hpa`),
  including the evictions-not-rollouts note.
- `charts/kato/README.md.gotmpl` — "32 → **34** read-only checks"; add `check_pdb` to the
  Workloads row and `probe_tls` to the Active probes row. Regenerate `README.md` and
  `charts/kato/README.md` via `make readme`. (`probe_grpc` remains uncounted until it is
  actually registered.)
- `charts/kato/templates/rbac.yaml` — the `policy`/`poddisruptionbudgets` rule.

No changes to the engine, server, CRDs, or `cmd/kato/main.go` — both methods are pure
registry additions (`init()` + `builtinFns`).

---

## Testing strategy

**`probe_tls_test.go`** (method-level, shared `fakeProber` — no real network):
- Success mapping: `TLSResult{HandshakeComplete: true, Verified: true, DaysUntilExpiry: 90, ...}`
  → `success=true`, all facts mapped 1:1, nil Go error.
- Verify failure is a finding: `Verified: false, VerifyError: "x509: ..."` → nil Go error,
  `success=false`, facts still present.
- `insecureSkipVerify` relaxation: same unverified result → `success=true`; but
  `Expired: true` → `success=false` even with skip-verify.
- Expired: `Expired: true, DaysUntilExpiry: -12` → `success=false`, negative days surfaced.
- Transport failure: `TLSResult{LatencyMS: -1, Err: "connection refused"}` → nil Go error,
  `success=false`, `handshakeComplete=false`.
- Request passthrough: `fakeProber.gotTLS` carries parsed `Target`/`Port`/`ServerName`/
  `Timeout` (default `5s`).
- Param errors: empty `target`; bad `port`; non-bool `insecureSkipVerify`; bad/`0s`/negative
  `timeout` — each a non-nil Go error.

**`prober_test.go`** (`LocalProber.ProbeTLS` against in-process `tls.Listen` servers with
certs generated in the test):
- Valid chain: cert for `127.0.0.1` signed by a test CA; `LocalProber{RootCAs: testPool}` →
  `HandshakeComplete=true, Verified=true`, sane `DaysUntilExpiry`/`NotAfter`/`TLSVersion`.
- Self-signed: → `HandshakeComplete=true, Verified=false`, `VerifyError` set, facts present.
- Expired cert (NotAfter in the past): → `Expired=true`, negative `DaysUntilExpiry`;
  handshake still completes (verification is disabled at handshake time).
- Not a TLS endpoint: plain TCP listener → `HandshakeComplete=false`, `Err` set,
  `LatencyMS=-1`.
- Closed port → `Err` set.
- `fakeProber` gains `tls TLSResult` / `gotTLS TLSProbeRequest` fields + `ProbeTLS` so it
  keeps satisfying the widened interface.

**`pdb_test.go`** (table-driven, `k8s.io/client-go/kubernetes/fake`):
- Exists with `disruptionsAllowed: 2` → `exists=true, blocked=false`, counts mapped.
- Blocked: `disruptionsAllowed: 0` + `DisruptionAllowed=False/InsufficientPods` condition →
  `blocked=true`, `conditionReason="InsufficientPods"`.
- Percent budget: `minAvailable: "50%"` renders as `"50%"`.
- Int budget: `maxUnavailable: 1` renders as `"1"`.
- Missing PDB → `exists=false`, all defaults, nil Go error.

---

## Alternatives considered (rejected)

- **`probe_udp`** — dropped entirely (see Goal): no handshake means silence is
  indistinguishable from a firewall drop; every candidate `success` semantic is either
  untrustworthy (lenient) or a false-negative machine (strict).
- **Extending `probe_http` with cert outputs** instead of a new probe — rejected: conflates
  two contracts (HTTP assertion vs certificate inspection), forces `https` scheme coupling,
  and `probe_tls` also covers non-HTTP TLS (databases, SMTP-over-TLS ports, gRPC).
- **`warnDays` threshold param on `probe_tls`** — rejected: CEL `when` on the
  `daysUntilExpiry` int already expresses any threshold.
- **Selector/workload-based `check_pdb` discovery** — rejected for v1 (see Method 2).

## Non-goals (explicit)

- **mTLS client certificates** (`probe_tls` presents no client cert; addable later).
- **StartTLS negotiation** (SMTP/LDAP-style upgrade flows).
- **Custom CA via param/Secret** — the `RootCAs` field is a test seam, not a user feature;
  in-cluster CAs are handled via `insecureSkipVerify` verdict relaxation.
- **Chain-wide expiry reporting** — facts come from the leaf; an expiring intermediate
  surfaces through `verified: false` once it lapses.
- **PDB creation/mutation advice** — `check_pdb` reads; the LLM summary interprets.
{% endraw %}
