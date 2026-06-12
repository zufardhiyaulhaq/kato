# kato

Kubernetes troubleshooting via declarative **UseCase** flows with AI summaries.

You define a troubleshooting journey as a CRD — an ordered list of predefined
checks (`check_pod_status`, `check_pod_logs`, `describe_node`, ...). Calling the
use case's API endpoint executes that flow **deterministically**; an LLM is used
only to **summarize** the collected evidence. The AI never decides what to
check and never touches the cluster.

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

## How it works

- **UseCase** (CRD): the flow — inputs, ordered steps, `when` conditions,
  per-step `summaryFilter`, and the summary prompt.
- **ModelConfig** (CRD): an LLM backend; UseCases pick one via
  `summary.modelConfigRef` or fall back to the default.
- **Run** (CRD): the audit record of each execution — inputs, per-step outputs,
  and the summary.

See `docs/superpowers/specs/2026-06-12-kato-design.md` for the full design.

## API

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/usecases` | list use cases |
| `GET /api/v1/usecases/{name}` | one use case's contract |
| `POST /api/v1/usecases/{name}/run` | execute (`{"inputs":{...}}`) |
| `GET /api/v1/methods` | built-in methods + their output fields |
| `GET /api/v1/runs/{name}` | a past run |
