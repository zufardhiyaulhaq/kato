# kato API Server Health Method: `check_apiserver`

**Status:** Approved (design)

**Goal:** Add a `check_apiserver` method that reports the health of the Kubernetes API
server kato is connected to, using kato's own authenticated REST client against the
apiserver's `/livez` or `/healthz` verbose health endpoints. It surfaces a pass/fail
signal plus the **names of the failing health checks** — distro-agnostic, with no
hardcoded component list — so a UseCase can gate on "is the control plane degraded?" and
hand the failing-check names to the LLM as evidence.

---

## Background and rationale

kato's existing methods fall into two families:

- **`check_*` / `describe_*` / `list_*`** — passive reads of the Kubernetes API through
  kato's configured client (`deps.Kube`).
- **`probe_tcp` / `probe_http`** — active, *unauthenticated* network traffic originated
  to an arbitrary target.

`check_apiserver` belongs to the **first** family. It is deliberately *not* a `probe_`
method: it does not take a `target`/`port`, and it does not originate raw traffic. It
reuses kato's authenticated client to read the apiserver's own health endpoints.

### Why a new method (and not `probe_http`)

kato is always already connected to one API server — if it weren't, the Run could not
execute. And `probe_http` can already confirm "is `https://api:6443/livez` returning 200
over the wire". So a new method only earns its place by doing what `probe_http` cannot:

1. **Authenticated.** It uses kato's ServiceAccount via `deps.Kube`, so it works on
   clusters where the health endpoints (or their verbose form) require authentication,
   without the UseCase author wiring a target/port/TLS. The health paths are
   *non-resource URLs*, so kato's ClusterRole must additionally grant `get` on them (see
   RBAC below) — this does not rely on the cluster-default `system:public-info-viewer`
   binding.
2. **Component-level parsing.** The `?verbose` form lists each individual health check as
   `[+]name ok` / `[-]name failed`. `check_apiserver` parses that and reports *which*
   checks failed — signal a bare HTTP 200/500 from `probe_http` cannot give.

### `/livez` and `/healthz` — what they report

The verbose wire format is identical across all distros (EKS, GKE, AKS, kubeadm) because
it is served by kube-apiserver itself:

```
[+]ping ok
[+]log ok
[+]etcd ok
[+]poststarthook/start-kube-apiserver-admission-initializer ok
...
livez check passed
```

- `/livez` — apiserver liveness (ping, log, etcd connectivity, basic poststarthooks).
- `/healthz` — the legacy combined endpoint; still present and widely available on every
  distro.

The **set** of check names is *not* stable across clusters: it varies with feature gates,
admission plugins, registered `APIService` aggregations, and poststarthooks. Therefore the
method must **not** hardcode per-component outputs (`etcdHealthy`, `schedulerHealthy`, …).
It reports a pass/fail bool, a failed-check count, and the failing check **names** as a
list — whatever they happen to be named on that cluster. (`/readyz` is intentionally out
of scope for v1; it is trivially addable later as a third allowed `endpoint` value.)

### Forward-compatibility with centralized kato

The future centralized-multi-cluster vision (a `KubernetesCluster` registration CRD) makes
`deps.Kube` a per-target-cluster client. Because `check_apiserver` reads health through
`deps.Kube`, it follows that change for free — checking a remote cluster's API server needs
**no** change to this method and **no** new seam (unlike the `Prober`). Nothing about
multi-cluster is built here; this is just noting the method does not block it.

---

## Method: `check_apiserver`

**Params**

| param | required | default | meaning |
|-------|----------|---------|---------|
| `endpoint` | no | `livez` | which health path to read: `livez` \| `healthz` |
| `timeout` | no | `5s` | request timeout (Go duration string; > 0), applied via `context.WithTimeout` |

No `target`/`port`: the method always reads the API server kato is configured against.
One endpoint per step (selectable). To check both `livez` and `healthz`, use two steps —
each is then independently gateable. (Rejected: checking both in one call, which would
produce awkward doubled outputs.)

**Outputs** (all scalars always present)

| output | type | meaning |
|--------|------|---------|
| `healthy` | bool | the endpoint reported healthy (HTTP 200 / summary line passed) |
| `statusCode` | int | HTTP status code; `0` on transport failure (apiserver unreachable) |
| `failedCount` | int | number of `[-]…failed` checks parsed from the verbose body |
| `error` | string | transport-failure reason (apiserver unreachable / timeout); `""` otherwise |

**List output**

| list | item fields | meaning |
|------|-------------|---------|
| `failedChecks` | `name` (string) | names of the individual `[-]…failed` health checks, as named on that cluster |

This mirrors `list_failing_pods` (a count scalar + a list output). `healthy` gates `when:`
conditions and `$(steps.x.healthy)` references; `failedCount` gates thresholds; the
`failedChecks` list flows into the Run CR and LLM evidence as part of the step's recorded
outputs when the step leaves `summaryFilter` unset (a non-nil `summaryFilter` allowlists
**scalar** outputs only — a list output's name cannot be added to it), or is iterated by a
`forEach`. No `maxListItems` param is provided: the failing-check set is naturally small
(bounded by the cluster's registered health checks), so the list needs no cap.

---

## Error-handling semantics

A degraded or unreachable API server is a **finding, not a method failure**:

- **HTTP 500** (apiserver up but unhealthy): the verbose body contains `[-]…failed` lines.
  The method returns `healthy=false`, `failedCount>0`, the `failedChecks` list populated,
  and a **nil Go error**. The step is recorded `completed`.
- **Transport failure** (statusCode `0`: kato genuinely cannot reach its own apiserver —
  connection refused, timeout, DNS): the method returns `healthy=false`, `statusCode=0`,
  `error` populated, and a **nil Go error**. This mirrors how `probe_tcp` treats
  connection-refused as a finding.
- **Non-nil Go error** (step recorded `failed`) only for param mistakes: an `endpoint`
  value other than `livez`/`healthz`, or an invalid/`<= 0` `timeout`. Matches the
  `parseMaxLineLength` / `parsePort` convention (invalid params are errors; expected
  outcomes are outputs).

---

## How it calls the API server

{% raw %}
```go
ctx, cancel := context.WithTimeout(ctx, timeout)
defer cancel()

result := deps.Kube.Discovery().RESTClient().
    Get().AbsPath("/" + endpoint).Param("verbose", "true").Do(ctx)

var code int
result.StatusCode(&code)
body, _ := result.Raw() // body is populated on 200 AND on 500; ignore err (status code is authoritative)

if code == 0 {
    // transport failure: apiserver unreachable
    return Outputs{
        "healthy": false, "statusCode": int64(0), "failedCount": int64(0),
        "error": result.Error().Error(),
        "failedChecks": []map[string]any{},
    }, nil
}
// HTTP response received: parse the verbose body
failed := parseFailedChecks(body) // collect names from lines starting with "[-]"
return Outputs{
    "healthy":     code == 200,
    "statusCode":  int64(code),
    "failedCount": int64(len(failed)),
    "error":       "",
    "failedChecks": failed, // []map[string]any{{"name": "etcd"}, ...}
}, nil
```
{% endraw %}

- `result.Raw()` returns the response body on both 200 and 500; the per-line `[-]`/`[+]`
  parse plus the status code carry all needed signal, so its returned error is ignored.
- `result.StatusCode(&code)` yields `0` when no HTTP response was received (transport
  failure); `result.Error()` then carries the connection/timeout reason.
- `parseFailedChecks` splits the body into lines, selects lines beginning with `[-]`, and
  extracts the check name (the text between `[-]` and the first space; e.g. `[-]etcd failed:
  reason` → `etcd`).

---

## Implementation notes

- Files:
  - `internal/methods/check_apiserver.go` — the method, `parseEndpoint`, `parseFailedChecks`.
  - `internal/methods/check_apiserver_test.go` — method tests.
  - `docs/METHOD.md` — add the `check_apiserver` index row and section.
  - `charts/kato/templates/rbac.yaml` — add a `nonResourceURLs: ["/livez", "/healthz"]`
    `get` rule to kato's ClusterRole (the health paths are non-resource URLs not covered by
    the existing resource grants).
- No change to `internal/methods/method.go` (`Deps.Kube` already exists) and no change to
  `cmd/kato/main.go` (no new dependency).
- `timeout` reuses the package-internal `parseProbeTimeout` helper (unset → `5s`, else a Go
  duration `> 0`); it is generic despite the name. DRY over duplicating the parse.
- `endpoint` is validated by `parseEndpoint`: empty → `livez`; `livez`/`healthz` accepted;
  anything else is a param error.
- `failedChecks` is a `ListProducer` list output (item field `name`), consumable by `forEach`.
  It is intentionally **not** capped (no `maxListItems` param, no `capItems` call): the
  failing-check set is naturally small, bounded by the cluster's registered health checks.

---

## Testing strategy

The fake clientset's discovery client cannot serve arbitrary `AbsPath` calls, so tests use a
real `httptest` server and a real clientset pointed at it — exercising the actual HTTP path
and body parsing, with no cluster:

{% raw %}
```go
srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    // route on r.URL.Path ("/livez" | "/healthz"); write 200+passed or 500+failed body
}))
client, _ := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
out, err := m.Run(ctx, Deps{Kube: client}, params)
```
{% endraw %}

Cases:
- **Healthy:** handler returns `200` + a verbose body of all `[+]` lines ending
  `livez check passed`. Assert `healthy=true`, `statusCode=200`, `failedCount=0`,
  `failedChecks` empty, nil Go error.
- **Degraded:** handler returns `500` + a body with two `[-]name failed` lines (e.g.
  `[-]etcd failed: ...`, `[-]poststarthook/x failed`) ending `livez check failed`. Assert
  `healthy=false`, `statusCode=500`, `failedCount=2`, `failedChecks` names parsed correctly,
  nil Go error.
- **`endpoint=healthz`:** assert the request hit `/healthz` (handler records `r.URL.Path`).
- **Transport failure:** point the clientset at a closed server (call `srv.Close()` first, or
  use an unused `127.0.0.1:0`-derived URL). Assert `healthy=false`, `statusCode=0`, `error`
  populated, nil Go error.
- **Param errors:** `endpoint=readyz` (and any other non-allowed value) and `timeout=0s` /
  `timeout=bad` each return a non-nil Go error.

All tests run under `go test ./internal/methods/` with no external network or cluster.

---

## Non-goals (explicit)

- **`/readyz`** as an endpoint value in v1 (trivially addable later as a third allowed value).
- **Per-component typed outputs** (`etcdHealthy`, …) — the check-name set is cluster-dependent;
  failing names are reported as a list instead.
- **Probing a *remote* cluster's API server** — arrives for free when the centralized
  multi-cluster `KubernetesCluster` client lands; not built here.
- **Returning the raw verbose body** as an output — only the parsed failing-check names.
- **Custom health subpaths** (e.g. `/readyz/etcd`) and non-GET methods.
