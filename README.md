# kato

Kubernetes troubleshooting via declarative UseCase flows with AI summaries

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.1.0](https://img.shields.io/badge/AppVersion-0.1.0-informational?style=flat-square) [![made with Go](https://img.shields.io/badge/made%20with-Go-brightgreen)](http://golang.org) [![Github main branch build](https://img.shields.io/github/actions/workflow/status/zufardhiyaulhaq/kato/main.yml?branch=main)](https://github.com/zufardhiyaulhaq/kato/actions/workflows/main.yml) [![GitHub issues](https://img.shields.io/github/issues/zufardhiyaulhaq/kato)](https://github.com/zufardhiyaulhaq/kato/issues) [![GitHub pull requests](https://img.shields.io/github/issues-pr/zufardhiyaulhaq/kato)](https://github.com/zufardhiyaulhaq/kato/pulls)

> Turn your team's runbooks into versioned CRDs. kato runs the exact checks **you**
> chose, in the order you chose, and lets an LLM do only the last mile — writing up
> what the evidence means.

## Why kato?

"Ask an AI agent to debug my cluster" is a tempting idea and a risky one: a generic
agent decides on its own what to inspect, the steps differ every time, and giving it
write access to production is a non-starter. kato flips the control.

- **The flow is deterministic, not the model.** *You* author the troubleshooting
  steps as a `UseCase` CRD. Every run executes the same ordered checks. The LLM never
  chooses what to look at and never calls the Kubernetes API.
- **Read-only by construction.** The operator ships with a `get/list/watch`-only
  ClusterRole — no writes, no `exec`, no deletes. The worst it can do is read.
- **Auditable.** Every execution is persisted as a `Run` CRD: the inputs, every
  step's raw output, and the final summary. You can replay exactly what happened.
- **Codify tribal knowledge.** The senior engineer's "first check the events, then
  the previous-container logs if it restarted, then the node" becomes a reusable,
  reviewable, version-controlled object — not a paragraph in a wiki.
- **Bring your own model.** Any OpenAI-compatible endpoint, including a local
  [Ollama](https://ollama.com) model, so cluster data never has to leave your network.
- **Cheap and safe on tokens.** A per-step `summaryFilter` controls exactly which
  fields reach the LLM, so the model sees a curated digest — not your whole cluster.

## How it works

You define a troubleshooting journey as a CRD — an ordered list of predefined checks.
Calling the use case executes that flow **deterministically**; an LLM is used only to
**summarize** the collected evidence.

Three CRDs:

- **`UseCase`** — the flow: inputs, ordered steps, `when` conditions, `forEach`
  fan-out, per-step `summaryFilter`, and the summary prompt.
- **`ModelConfig`** — an LLM backend (OpenAI-compatible). UseCases pick one via
  `summary.modelConfigRef` or fall back to the default.
- **`Run`** — the audit record of each execution: inputs, per-step outputs, and the
  summary. Create one with `kubectl`/GitOps, or via the REST API.

### A real use case

```yaml
apiVersion: kato.zufardhiyaulhaq.com/v1alpha1
kind: UseCase
metadata:
  name: pod-crashloop
spec:
  description: "Diagnose why a pod is crash looping"
  inputs:
    - { name: namespace, required: true }
    - { name: pod, required: true }
  steps:
    - name: status
      method: check_pod_status
      with: { namespace: $(inputs.namespace), name: $(inputs.pod) }
    - name: events
      method: check_events
      with: { namespace: $(inputs.namespace), involvedObject: $(inputs.pod) }
    - name: previous-logs
      method: check_pod_logs
      when: $(steps.status.restartCount) > 0      # only if it actually restarted
      with:
        namespace: $(inputs.namespace)
        name: $(inputs.pod)
        previous: "true"
        tailLines: "100"
    - name: node
      method: describe_node
      when: $(steps.status.nodeName) != ""
      with: { name: $(steps.status.nodeName) }
  summary:
    prompt: |
      You are a Kubernetes SRE. Based on the evidence, explain why this pod is
      crash looping and suggest a fix.
```

Run it:

```console
curl -s -X POST localhost:8080/api/v1/usecases/pod-crashloop/run \
  -d '{"inputs":{"namespace":"payments","pod":"payment-api-xyz"}}' | jq
```

kato runs `check_pod_status` → `check_events` → (if it restarted) `check_pod_logs`
→ (if scheduled) `describe_node`, then hands the filtered evidence to the model for a
plain-language root cause and fix. Same steps, every time.

## Built-in methods

kato ships **25 read-only checks** you compose into flows — pods, workloads,
storage, batch, nodes, networking, and config:

| Area | Methods |
|---|---|
| Pods | `check_pod_status`, `check_pod_logs`, `describe_pod`, `check_pod_resources`, `check_pod_usage` |
| Workloads | `check_deployment_status`, `describe_deployment`, `check_replicaset`, `check_daemonset_status`, `describe_daemonset`, `check_statefulset_status`, `describe_statefulset`, `check_hpa` |
| Storage | `check_pvc` |
| Batch | `check_job`, `check_cronjob` |
| Listing / fan-out | `list_pods`, `list_failing_pods` (drive `forEach` across a workload's pods) |
| Nodes | `check_node_status`, `describe_node` |
| Networking | `check_service_endpoints`, `describe_service`, `check_ingress` |
| Config & events | `check_configmap`, `check_events` |

Full reference and every output field: [`docs/METHOD.md`](https://github.com/zufardhiyaulhaq/kato/blob/main/docs/METHOD.md).
Ready-made UseCases (CrashLoop, pending pods, stuck deployments, unreachable services,
cluster DNS) live under [`examples/`](https://github.com/zufardhiyaulhaq/kato/tree/main/examples).

## API

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/usecases` | list use cases |
| `GET /api/v1/usecases/{name}` | one use case's contract |
| `POST /api/v1/usecases/{name}/run` | execute (`{"inputs":{...}}`) |
| `GET /api/v1/methods` | built-in methods + their output fields |
| `GET /api/v1/runs/{name}` | a past run |

## Quickstart

```bash
helm install kato charts/kato -n kato --create-namespace \
  --set modelConfig.enabled=true \
  --set modelConfig.apiKey=$OPENAI_API_KEY

kubectl apply -f examples/usecases/pod-crashloop.yaml

kubectl -n kato port-forward svc/kato 8080:8080 &
curl -s -X POST localhost:8080/api/v1/usecases/pod-crashloop/run \
  -d '{"inputs":{"namespace":"payments","pod":"payment-api-xyz"}}' | jq
```

No OpenAI account? Point `ModelConfig` at a local Ollama model instead — see
[`examples/modelconfig/`](https://github.com/zufardhiyaulhaq/kato/tree/main/examples/modelconfig).

## Installing

To install the chart with the release name `my-release`:

```console
helm repo add kato https://zufardhiyaulhaq.com/kato/charts/releases/
helm install my-kato kato/kato --values values.yaml
```

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| config.gcInterval | string | `"1h"` |  |
| config.maxConcurrent | int | `10` |  |
| config.runMaxDuration | string | `"1h"` |  |
| config.runReconcileConcurrency | int | `2` |  |
| config.runTTL | string | `"168h"` |  |
| config.stepTimeout | string | `"30s"` |  |
| image.pullPolicy | string | `"IfNotPresent"` |  |
| image.repository | string | `"ghcr.io/zufardhiyaulhaq/kato"` |  |
| image.tag | string | `"0.1.0"` |  |
| modelConfig.apiKey | string | `""` |  |
| modelConfig.baseURL | string | `"https://api.openai.com/v1"` |  |
| modelConfig.default | bool | `true` |  |
| modelConfig.enabled | bool | `false` |  |
| modelConfig.maxTokens | int | `2048` |  |
| modelConfig.model | string | `"gpt-4o-mini"` |  |
| modelConfig.name | string | `"default"` |  |
| modelConfig.temperature | string | `"0"` |  |
| replicaCount | int | `1` |  |
| resources.limits.cpu | string | `"500m"` |  |
| resources.limits.memory | string | `"256Mi"` |  |
| resources.requests.cpu | string | `"50m"` |  |
| resources.requests.memory | string | `"64Mi"` |  |
| service.port | int | `8080` |  |
| service.type | string | `"ClusterIP"` |  |

see example files [here](https://github.com/zufardhiyaulhaq/kato/blob/main/charts/kato/values.yaml)

----------------------------------------------
Autogenerated from chart metadata using [helm-docs v1.14.2](https://github.com/norwoodj/helm-docs/releases/v1.14.2)
