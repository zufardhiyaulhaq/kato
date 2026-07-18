# Developing kato

This guide covers building, testing, and running kato locally against a
Kubernetes cluster.

## Prerequisites

- **Go 1.25+**
- **kubectl** and a cluster you can reach (a local [kind](https://kind.sigs.k8s.io/)
  cluster is ideal for development)
- **helm** (only for installing via the chart / `helm lint`)
- Optional: **metrics-server** in the cluster, for `check_pod_usage` (live
  CPU/memory). Without it, that method reports `metricsAvailable: false`.
- Optional: an LLM endpoint for summaries — any OpenAI-compatible API
  (OpenAI, [Ollama](https://ollama.com/), vLLM, …). Without one, runs still
  return all collected evidence with an empty summary and a warning.

Run `make help` to see all targets.

## Project layout

```
cmd/kato/             entrypoint + wiring (manager, HTTP server, GC loop)
api/v1alpha1/         CRD types: UseCase, ModelConfig, Run (+ generated deepcopy)
internal/methods/     the read-only check library (one file per method) + registry
internal/engine/      flow execution: $() refs, CEL `when`, validation, step + forEach
internal/summarizer/  evidence builder + OpenAI-compatible client
internal/store/       Run CR persistence + TTL garbage collection
internal/server/      REST API
internal/controller/  reconcilers (Ready conditions), caches, ModelConfig resolution
internal/config/      env-var configuration
config/crd/bases/     generated CRD manifests
charts/kato/          Helm chart (CRDs, Deployment, read-only RBAC)
examples/usecases/    example UseCases + ModelConfig
docs/METHOD.md        per-method input/output reference
```

## Build & test

```bash
make build              # compile to ./bin/kato
make test               # all unit tests
make test-integration   # envtest controller suite (downloads test binaries once)
make lint               # golangci-lint
```

## Code generation

The API types are the source of truth. After editing anything in `api/v1alpha1/`:

```bash
make generate           # regenerate zz_generated.deepcopy.go
make manifests          # regenerate config/crd/bases/*.yaml
cp config/crd/bases/*.yaml charts/kato/crds/   # keep the chart's copy in sync
```

## Run locally against a cluster

kato runs as a controller-runtime manager plus an HTTP server. Run outside the
cluster pointed at your kubeconfig:

1. **Cluster + CRDs.** Use any cluster; install the CRDs:
   ```bash
   kind create cluster            # or use an existing context
   make install-crds              # kubectl apply -f config/crd/bases
   kubectl create namespace kato
   ```
   The CRDs must be installed before kato starts — the manager watches
   `UseCase`/`ModelConfig` and won't become ready otherwise.

2. **(Optional) A ModelConfig for summaries.** Apply one to the cluster. kato
   reads the API-key Secret from `KATO_NAMESPACE`.

   OpenAI:
   ```bash
   kubectl -n kato create secret generic openai-key --from-literal=apiKey=$OPENAI_API_KEY
   kubectl apply -f - <<'EOF'
   apiVersion: kato.zufardhiyaulhaq.com/v1alpha1
   kind: ModelConfig
   metadata: { name: openai }
   spec:
     default: true
     baseURL: https://api.openai.com/v1
     model: gpt-4o-mini
     apiKeySecretRef: { name: openai-key, key: apiKey }
   EOF
   ```

   Local Ollama (no key):
   ```bash
   kubectl apply -f - <<'EOF'
   apiVersion: kato.zufardhiyaulhaq.com/v1alpha1
   kind: ModelConfig
   metadata: { name: ollama }
   spec:
     default: true
     baseURL: http://host.docker.internal:11434/v1   # adjust for your setup
     model: qwen3:14b
   EOF
   ```

   Skip this step entirely to develop without summaries.

3. **Configure and run.**
   ```bash
   cp .env.example .env            # edit KATO_NAMESPACE / KUBECONFIG if needed
   make run                        # loads .env, runs ./cmd/kato
   ```
   kato listens on `:8080` (per `KATO_LISTEN_ADDR`).

4. **Try a UseCase.**
   ```bash
   kubectl apply -f examples/usecases/pod-crashloop.yaml
   curl -s localhost:8080/api/v1/usecases | jq            # 'ready: true' once validated
   curl -s -X POST localhost:8080/api/v1/usecases/pod-crashloop/run \
     -d '{"inputs":{"namespace":"default","pod":"some-pod"}}' | jq
   curl -s localhost:8080/api/v1/methods | jq             # all methods + their outputs
   ```

### Triggering a run with kubectl (no API call)

Besides the REST API, you can trigger a run by creating a `Run` CR directly —
useful for GitOps. The `RunReconciler` executes any externally-created `Run`
(one without the `kato.zufardhiyaulhaq.com/managed-by: api` label the API sets)
exactly once and writes the result to the same `Run`'s status. Give it a fixed
`metadata.name` so it works with `kubectl apply` and you can fetch it by name:

```bash
kubectl apply -f - <<'EOF'
apiVersion: kato.zufardhiyaulhaq.com/v1alpha1
kind: Run
metadata:
  name: pod-crashloop-debug
  namespace: default
spec:
  useCase: pod-crashloop
  inputs:
    namespace: default
    pod: some-pod
EOF

kubectl get run -n default pod-crashloop-debug -o yaml   # per-step outcomes + summary
```

If `spec.useCase` is missing or not Ready, or inputs are invalid, the run ends
`Failed` with the reason in `status.note`. A `Run` is execute-once and immutable:
re-applying the same name is a no-op, so to re-run, `kubectl delete run
pod-crashloop-debug` first (or use a new name, or `generateName` + `kubectl create`
for a fresh run each time). Runs created this way are bounded by
`KATO_RUN_RECONCILE_CONCURRENCY` (separate from the API's `KATO_MAX_CONCURRENT`),
and a run stranded in `Running` by a controller crash is reaped to `Failed` after
`KATO_RUN_MAX_DURATION`. Note that the reaper force-fails *any* run exceeding that
threshold — a healthy long-running one as well as a stranded one — so set it above
your longest expected run.

> kato runs as a single replica (`replicaCount: 1`). The UseCase/ModelConfig
> caches the REST API serves from are populated in-process by their reconcilers,
> and the RunReconciler is the single active executor of external Runs. Scaling
> out needs leader election plus per-controller cache warming; it is not just a
> replica bump.

## How kato handles failures mid-flow

This is a core design property: **a failing step is a finding, not an abort.**
The flow is deterministic and resilient. Specifically:

- **A check fails at runtime** (pod already deleted / `NotFound`, RBAC denied,
  API error, a bad `with` value): the step's outcome is recorded as `failed`
  with the error text, and **the flow continues** to the next step. The error
  becomes evidence the LLM can reason about ("the pod no longer exists").
- **A later step depends on a failed/skipped step** (references it in `when` or
  `with`, or a `forEach` over its list): that step is auto-`skipped` with a
  reason naming the unmet dependency — no cascading errors from unresolved
  references.
- **Bad caller input** (the request body): inputs are validated up front in
  `Execute`. A missing-required or unknown input fails the whole call with
  **HTTP 400** before any step runs — nothing is executed or persisted.
- **A bad `forEach` item / unresolved `$(item.…)`**: that single iteration is
  recorded `failed`; the loop continues with the remaining items. Per-iteration
  failures do not fail the step or the run by themselves.
- **A slow/hung check**: each step and each iteration runs under
  `KATO_STEP_TIMEOUT` (default 30s); a deadline becomes a `failed` finding.
- **The LLM is unreachable**: the run still returns all collected outputs with
  an empty summary and a warning — never a hard failure.

The run's overall `phase` reflects this: `Succeeded` (no failures),
`PartiallySucceeded` (some failed, at least one completed), or `Failed` (every
runnable step failed). Every step's outcome, error, and outputs are persisted on
the `Run` CR, so a partial run is fully auditable:

```bash
kubectl -n kato get runs
kubectl -n kato get run <name> -o yaml   # per-step outcomes, errors, summary
```

### Known gap: panics are not caught per step

Methods are read-only and defensive, so a genuine `panic` inside a method is
unlikely — but the engine does **not** currently `recover()` per step. A panic
would abort that one run (the HTTP layer recovers, returns 500, and the process
keeps serving), and no `Run` CR would be persisted for it. Everything that
returns an `error` (the cases above) is handled gracefully; only an actual panic
is not. Adding per-step/per-iteration `recover()` that converts a panic into a
`failed` finding would close this — a good first contribution.

## Adding a new method

1. Create `internal/methods/<name>.go`. Implement the `Method` interface
   (`Name`, `Description`, `Params`, `OutputFields`, `Run`). Register it in an
   `init()` that appends to `builtinFns` (see any existing method).
2. Outputs are flat, typed (`string`/`int`/`bool`), and always present with a
   defined default — this is what `when` conditions and `$(steps.x.y)`
   references rely on. Use `deps.Kube` for the typed client; handle a nil
   `deps.Metrics` gracefully if you need metrics.
3. To return a list (for `forEach`), also implement `ListOutputs()` (see
   `list_failing_pods.go`).
4. Write a table-driven test using `k8s.io/client-go/kubernetes/fake`.
5. Update `docs/METHOD.md` and adjust `charts/kato/templates/rbac.yaml` if the
   method reads a resource not already granted.

Run `make test && make lint` before sending a change.
