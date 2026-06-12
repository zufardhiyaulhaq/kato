# kato — Design Specification

**Date:** 2026-06-12
**Module:** `github.com/zufardhiyaulhaq/kato`
**Status:** Approved

## 1. Problem & Positioning

Troubleshooting Kubernetes incidents follows repeatable journeys ("pod is crash
looping → check status, fetch previous logs, check events, inspect the node"),
but existing AI tools either hard-code their checks (K8sGPT) or hand control of
the investigation to an LLM agent (HolmesGPT, kubectl-ai, kagent), making runs
non-reproducible, non-auditable, and unpredictable in cost.

**kato** is a Kubernetes troubleshooting tool where:

1. Users define troubleshooting **use cases** declaratively as CRDs — an ordered
   journey of predefined check steps.
2. Each use case is exposed as an **API endpoint**; calling it executes the flow
   **deterministically**.
3. **AI is used only to summarize** the collected evidence at the end — the LLM
   never chooses what to check and never touches the cluster.

### Landscape gap (researched 2026-06)

| Tool | AI usage | Flow control | CRD-defined flows | API |
|---|---|---|---|---|
| K8sGPT | AI explains fixed analyzer findings | Deterministic | No | gRPC/operator |
| HolmesGPT | Agentic tool-calling | AI-driven | No | CLI/HTTP |
| kubectl-ai | Agentic NL→kubectl | AI-driven | No | CLI/MCP |
| kagent | CRD declares agent (prompt+tools), LLM improvises | AI-driven | Agent, not steps | K8s API |
| KubeDiag | None | Deterministic CRD pipelines | Yes | K8s API (**archived 2024**) |
| Troubleshoot.sh | None | Deterministic collectors | YAML, not live CRDs | CLI |

No tool combines: CRD-defined flows + deterministic execution + API trigger +
AI-as-summarizer-only. kato's differentiators: reproducible auditable runs,
read-only cluster access, single predictable LLM call per run, GitOps-reviewable
flows.

## 2. Decisions Summary

| Decision | Choice |
|---|---|
| Callers | Design for humans (REST), AI agents (MCP), alerts (webhook); **v1 ships REST only**, MCP in v1.1, Alertmanager webhook in v1.2 |
| Flow model | Linear steps + `when` conditions (CEL) + `$(...)` output references |
| Run model | Synchronous API response + every run persisted as a `Run` CR |
| LLM backend | OpenAI-compatible API (covers OpenAI, Ollama, vLLM, Azure, OpenRouter) behind a `Summarizer` interface; models declared via `ModelConfig` CRD, referencable per UseCase |
| v1 methods | ~14 read-only checks: pod/node core, events, workloads, services/networking |
| Architecture | Single Go binary, one Deployment; controller-runtime informer cache (no reconcile loop) |

## 3. Architecture

```
                       ┌─────────────────────────────────────────┐
                       │   kato (single Go binary, 1 Deployment) │
  on-call engineer ───▶│  REST API (v1)        ┌──────────────┐  │
  MCP clients (v1.1) ─▶│  MCP adapter (v1.1) ─▶│  Flow Engine │  │
  Alertmanager (v1.2)─▶│  Webhook (v1.2)       └──┬────────┬──┘  │
                       │  UseCase informer         │        │    │
                       │  (watch CRs → routes)     ▼        ▼    │
                       │                    Method     Summarizer│
                       │                    Library    (OpenAI-  │
                       │                    (~14)      compat)   │
                       └────────┬──────────────┬─────────────────┘
                                ▼              ▼
                         Kubernetes API   Run CR written
                         (read-only)      (audit record)
```

### Packages

| Package | Responsibility | Depends on |
|---|---|---|
| `api/v1alpha1` | CRD types: `UseCase`, `Run`, `ModelConfig` | — |
| `internal/server` | HTTP server; UseCase CRs → routes; input validation | engine |
| `internal/engine` | Flow execution: CEL `when`, `$(...)` substitution, ordered steps, output collection | methods, summarizer |
| `internal/methods` | Built-in check library; static registry; each method: typed params → typed outputs | client-go |
| `internal/summarizer` | One LLM call: step outputs + prompt → summary; `Summarizer` interface, OpenAI-compatible impl | — |
| `internal/store` | Run CR persistence + TTL garbage collection | k8s client |

All front doors call the same `engine.Execute(useCase, inputs)`.

### Security properties

- ServiceAccount RBAC: **read-only** on cluster resources + write only on
  `runs.kato.zufardhiyaulhaq.com`.
- The LLM receives step outputs only (per-step `summaryFilter` filter applies); it has no tools and no cluster access.
- Secrets' data stripped from describe output; env var values redacted by default.
- Local LLM support (Ollama/vLLM) for clusters that cannot send logs externally.

## 4. CRDs

API group: `kato.zufardhiyaulhaq.com`, version `v1alpha1`.

### UseCase (cluster-scoped)

```yaml
apiVersion: kato.zufardhiyaulhaq.com/v1alpha1
kind: UseCase
metadata:
  name: pod-crashloop
spec:
  description: "Diagnose why a pod is crash looping"
  inputs:
    - name: namespace
      type: string
      required: true
    - name: pod
      type: string
      required: true
  steps:
    - name: status
      method: check_pod_status
      with:
        namespace: $(inputs.namespace)
        name: $(inputs.pod)
    - name: events
      method: check_events
      with:
        namespace: $(inputs.namespace)
        involvedObject: $(inputs.pod)
    - name: previous-logs
      method: check_pod_logs
      when: $(steps.status.restartCount) > 0
      with:
        namespace: $(inputs.namespace)
        name: $(inputs.pod)
        previous: true
        tailLines: 100
    - name: node
      method: describe_node
      when: $(steps.status.nodeName) != ""
      with:
        name: $(steps.status.nodeName)
  summary:
    modelConfigRef: ollama-local   # optional; omitted → default ModelConfig
    prompt: |
      You are a Kubernetes SRE. Based on the evidence collected,
      explain why this pod is crash looping and suggest a fix.
status:
  conditions:
    - type: Ready
      status: "True"
```

- Input `type` supports `string` only in v1 (booleans/numbers arrive as
  strings and are converted by methods as needed).
- `when` expressions are **CEL** (`cel-go`) — same language as Kubernetes
  ValidatingAdmissionPolicy: familiar, sandboxed, battle-tested.
- `$(...)` in `with` values is string interpolation against a structured
  context: `inputs.*` and `steps.<name>.<field>` (documented output fields of
  earlier steps).
- **Validation at watch time (typed):** on UseCase create/update, kato
  validates method names and `with` params against each method's declared
  param schema, and **compiles every `when` expression with cel-go against a
  typed environment** built from `inputs.*` plus the declared output fields of
  *prior* steps. Unknown fields (`steps.status.restartCnt`), type errors
  (`phase > 3` where `phase` is a string), and forward/unknown step references
  all yield `Ready=False` with a message naming the offending expression and
  listing the valid fields. The same check applies to `$(...)` substitutions.
  Not-Ready use cases return a clear API error (`422`) instead of failing
  mid-flow.
- Cluster-scoped: journeys are not namespace-bound; inputs carry the namespace.

### ModelConfig (cluster-scoped)

Declares an available LLM so different UseCases can use different models
(e.g., local Ollama for routine checks, a stronger hosted model for complex
diagnosis, or pinning sensitive use cases to an in-cluster LLM).

```yaml
apiVersion: kato.zufardhiyaulhaq.com/v1alpha1
kind: ModelConfig
metadata:
  name: ollama-local
spec:
  default: true                  # exactly one ModelConfig should set this
  baseURL: http://ollama.llm.svc:11434/v1
  model: qwen3:14b
  apiKeySecretRef:               # optional (local LLMs often need none)
    name: ollama-key             # Secret in kato's namespace
    key: apiKey
  maxTokens: 2048
  temperature: 0
status:
  conditions:
    - type: Ready                # spec validates; optional connectivity probe
      status: "True"
```

Resolution rules:

- UseCase `summary.modelConfigRef` omitted → the `default: true` ModelConfig.
- No default and no ref → UseCase stays Ready; runs return step outputs with
  `summary: null` and a warning (same as LLM-unavailable behavior).
- `modelConfigRef` naming a missing ModelConfig → UseCase `Ready=False` at watch time.
- The ModelConfig is resolved at **run** time, so editing a ModelConfig takes
  effect immediately without touching UseCases.
- If multiple ModelConfigs set `default: true`, kato picks the
  lexicographically-first by name and reports a warning condition on the
  others.

### Run (namespaced, in kato's namespace)

```yaml
apiVersion: kato.zufardhiyaulhaq.com/v1alpha1
kind: Run
metadata:
  generateName: pod-crashloop-
  labels:
    kato.zufardhiyaulhaq.com/usecase: pod-crashloop
spec:
  useCase: pod-crashloop
  inputs:
    namespace: payments
    pod: payment-api-7d9f8b-xk2lp
status:
  phase: Succeeded            # Succeeded | PartiallySucceeded | Failed
  startedAt: "..."
  completedAt: "..."
  steps:
    - name: status
      outcome: completed       # completed | skipped | failed
      outputs: { ... }         # all declared outputs of the step, size-capped
  summary: "..."
```

- Written by kato after every execution (audit record); queryable via labels.
- `status` also records which `ModelConfig` produced the summary.
- TTL-based garbage collection (configurable, default 7 days).
- Per-step size cap (configurable, default 64KiB per step) for etcd object
  limits and LLM context budget. Truncation applies only to large string
  outputs (e.g. `logs`, head+tail kept); scalar outputs are never truncated.

## 5. REST API (v1)

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/usecases` | List use cases (name, description, inputs, Ready) |
| `GET /api/v1/methods` | List built-in methods: params + declared output fields |
| `GET /api/v1/usecases/{name}` | One use case's contract |
| `POST /api/v1/usecases/{name}/run` | Execute synchronously; body `{"inputs": {...}}` |
| `GET /api/v1/runs?usecase={name}` | List past runs |
| `GET /api/v1/runs/{name}` | One run: step outputs + summary |
| `GET /healthz`, `GET /readyz` | Probes |

- `POST .../run` response mirrors Run status (step outcomes, outputs, summary,
  Run CR name). `?includeOutputs=false` for compact responses.
- Errors: `404` unknown use case; `422` use case not Ready (with validation
  message); `400` invalid inputs; `429` over max-concurrent-runs (default 10);
  `500` only for kato bugs.
- v1 auth: none built-in; rely on NetworkPolicy/ingress (documented). Token
  auth is a fast follow.

## 6. Engine Semantics

1. Validate inputs against the UseCase contract → `400` on mismatch.
2. Steps run strictly in order. Per step: evaluate `when` (absent = true);
   false → `skipped`. Substitute `$(...)`; invoke method with per-step timeout
   (default 30s); record the step's outputs.
3. **Step failure ≠ flow failure.** Errors (RBAC denied, object gone, timeout)
   are recorded in the step's outputs (`error` field) and the flow continues — a failed check is itself a
   finding. Exception: steps referencing outputs of failed/skipped steps are
   auto-skipped with a recorded reason.
4. Summarization: one LLM call with all steps' outputs (after `summaryFilter`
   filtering) + use-case prompt, under an overall token budget (large string
   outputs truncated proportionally if needed).
5. Run phase: `Succeeded` (all steps ran, summary produced),
   `PartiallySucceeded` (some steps failed/skipped, summary produced),
   `Failed` (engine-level failure).
6. **LLM unavailable → still return all step outputs** with `summary: null`
   and a warning. Deterministic value does not depend on AI availability.

## 7. Method Library (v1, all read-only)

### Method contract: outputs only

Every method declares a static, typed contract in the registry: **params**
(typed inputs: name, type, required) and **outputs** — one flat set of typed,
guaranteed-present, documented fields. There is no separate evidence/details
payload: **outputs are simultaneously what `when` conditions match, what
`$(...)` references read, what the LLM receives, and what the Run records.**
One structure, byte-identical everywhere.

Example — `check_pod_status` outputs:

| Field | Type | Notes |
|---|---|---|
| `phase` | string | `Pending\|Running\|Succeeded\|Failed\|Unknown` |
| `ready` | bool | all containers ready |
| `restartCount` | int | rolled-up max across containers, 0 if none |
| `nodeName` | string | `""` if unscheduled |
| `waitingReason` | string | e.g. `CrashLoopBackOff`, `""` if none |
| `waitingMessage` | string | human-readable waiting message, `""` if none |
| `lastTerminationReason` | string | e.g. `OOMKilled`, `""` if none |
| `lastTerminationExitCode` | int | -1 if no prior termination |
| `qosClass` | string | `Guaranteed\|Burstable\|BestEffort` |

Free-text data is just a string output: `check_pod_logs` declares
`logs` (string). Outputs are built explicitly in the method's Go code —
**never a raw API dump**: noise such as IPs, image/container IDs,
`managedFields`, probe timestamps is never declared as an output; Secret data
and env var values are stripped/redacted.

**Why outputs are flat scalars (not nested):**

1. *Guaranteed presence.* Kubernetes objects have optional nested branches
   (`state.waiting` only while waiting, no `containerStatuses` before
   scheduling). Nested references would error on healthy objects; flat fields
   always exist with defined defaults (`waitingReason: ""`), so conditions
   never break.
2. *No array ambiguity.* A pod has N containers; the method answers "which
   one?" once in its contract (`restartCount` = max across containers)
   instead of every UseCase author answering differently.
3. *Simple references, docs, and validation.* `$(steps.status.restartCount)`
   reads at a glance; method docs are a flat name/type/meaning table; typed
   CEL validation stays trivial.

Trade-off: no per-container matching in conditions. Rich detail still reaches
the AI via string outputs (`waitingMessage`, `logs`); if finer matching is
ever needed, the fix is declaring more outputs, not nesting.

### Filtering what the AI sees: `summaryFilter`

Each UseCase step may declare which outputs are sent to the LLM:

```yaml
steps:
  - name: status
    method: check_pod_status
    summaryFilter: [phase, restartCount, waitingReason, lastTerminationReason]
    # omitted → all of the method's outputs go to the AI
    # []      → nothing from this step goes to the AI (still recorded in Run)
```

- Validated at watch time: a field not declared by the method →
  `Ready=False` naming the bad field and listing valid ones.
- The filter affects only the LLM. Conditions and `$(...)` references can use
  any declared output, and the Run records all outputs regardless.

Static registry (`map[string]Method`); adding a method = implementing a small
interface (also the future plugin extension point). `GET /api/v1/methods`
exposes every method's params and output fields with descriptions, so
use-case authors can discover what is matchable.

- **Pod/Node:** `check_pod_status` (phase, restartCount, waiting reasons,
  nodeName, conditions), `check_pod_logs` (container/previous/tailLines),
  `describe_pod` (secrets stripped), `check_node_status` (conditions,
  pressure), `describe_node` (capacity, allocatable, taints)
- **Events:** `check_events` (by involvedObject or namespace, warnings first)
- **Workloads:** `check_deployment_status`, `describe_deployment`,
  `check_replicaset`
- **Networking:** `check_service_endpoints` (selector matches? ready/notReady),
  `describe_service`, `check_ingress` (rules, backend existence, LB status)

## 8. Summarizer

- `Summarizer` interface; one OpenAI-compatible implementation in v1.
- Configured declaratively via `ModelConfig` CRs (see §4) — `baseURL`, `model`,
  `apiKeySecretRef`, `maxTokens`, `temperature` (default 0). No ConfigMap-based
  LLM settings; the CRD is the single source of LLM config. Per-run resolution:
  UseCase `summary.modelConfigRef`, else the default ModelConfig.
- System prompt: evidence-based diagnosis, cite supporting step evidence per
  claim, say "inconclusive" rather than guess. UseCase `summary.prompt`
  appended as case-specific instruction.

## 9. Repository Layout

```
github.com/zufardhiyaulhaq/kato
├── cmd/kato/                  # main: flags, config, wiring
├── api/v1alpha1/              # UseCase, Run types
├── internal/server/           # HTTP handlers, routing
├── internal/engine/           # executor, CEL eval, substitution
├── internal/methods/          # one file per method + registry
├── internal/summarizer/       # interface + openai client
├── internal/store/            # Run persistence + TTL GC
├── config/crd/                # generated CRD manifests
├── charts/kato/               # Helm chart
├── examples/usecases/         # pod-crashloop, pod-pending,
│                              # deployment-stuck, service-unreachable
└── docs/
```

## 10. Testing

TDD throughout.

- **Unit:** engine (CEL, substitution, skip/failure semantics — table-driven);
  each method against client-go fake clients; summarizer against a stub HTTP
  server.
- **Integration:** `envtest` — CRD watch → route registration → execution →
  Run persistence.
- **E2E:** one happy path on `kind` in CI.
- **CI:** GitHub Actions — lint (golangci-lint), test, build, image publish.

## 11. Deployment

Helm chart: CRDs, Deployment (1 replica), read-only ClusterRole + Run-write
Role (plus Secret read in kato's own namespace for `apiKeySecretRef`),
ConfigMap (timeouts, Run TTL, max concurrency, output size caps — LLM settings
live in `ModelConfig` CRs instead). Chart values can optionally create an
initial `ModelConfig` (+ its Secret) and the four example UseCases.

## 12. Roadmap (post-v1)

- **v1.1:** MCP server adapter (use cases exposed as MCP tools).
- **v1.2:** Alertmanager webhook receiver (alert label → use case mapping,
  summary pushed to sinks).
- **Later:** token auth, plugin/external methods, additional LLM providers,
  web UI over the runs history.
