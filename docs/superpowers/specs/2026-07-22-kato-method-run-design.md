{% raw %}
# kato Direct Method Run — Design

**Status:** Approved (design)

**Goal:** Add `POST /api/v1/methods/{name}/run` — execute a single built-in method
directly with caller-supplied params and return its outputs. Stateless: no Run CRD,
no LLM summary, nothing persisted. This gives API consumers (notably the kato-bot
MCP/REST proxy) an exploratory "poke at the cluster" primitive alongside the
authored UseCase flows.

## Problem

kato's methods (35 read-only checks) are only reachable through a UseCase: an
authored, validated flow that runs steps and produces an LLM summary. There is no
way to invoke one method ad hoc. `GET /api/v1/methods` lists methods and their
declared params/outputs, but nothing can execute one. Agent-style consumers (MCP
clients) want exactly that: discover methods, then call one directly and read raw
outputs — no UseCase authoring, no LLM latency.

## Non-goals

- **Persistence.** No Run CRD is created; direct method calls are interactive and
  leave no audit record (kato logs the call). The UseCase flow remains the audited
  path.
- **LLM involvement.** The response is raw outputs only.
- **CEL / `forEach` / multi-step semantics.** Those are flow features; this runs
  exactly one method once.
- **Auth.** Unchanged (none); network reach is the boundary.
- **Typed params.** Params are strings on the wire, like UseCase inputs.
- **Output filtering.** The response always carries all of the method's outputs.
  `summaryFilter` is a UseCase-step feature (it selects LLM evidence); with no
  LLM here there is nothing to filter for, and callers can ignore fields. A
  request-side filter can be added later if payload size ever becomes a problem.

## API contract

### Request

```
POST /api/v1/methods/{name}/run
Content-Type: application/json

{"params": {"namespace": "payments", "pod": "api-0"}}
```

- `{name}` in the path, mirroring `POST /api/v1/usecases/{name}/run`.
- `params` is `map[string]string`. Body optional (missing = empty params).

### Response — 200 (method executed)

A method-level failure (pod not found, probe refused, connection reset) is a
legitimate troubleshooting *finding*, not a caller error — it returns 200 with
`outcome: "failed"`, exactly like a failed step inside a UseCase run.

```json
{"outcome": "completed", "outputs": {"phase": "Running", "restarts": 3, "pods": ["a", "b"]}}
{"outcome": "failed", "error": "pod \"api-0\" not found", "outputs": {}}
```

- `outcome`: `completed` | `failed`.
- `outputs`: the method's outputs — scalars plus list outputs as arrays (same
  content as a `StepResult.Outputs`).
- `error`: present only when `outcome` is `failed`.

The response is a **new view type with lowercase json tags** (`outcome`,
`outputs`, `error`) — do not reuse `engine.StepResult`, whose capitalized keys
are a documented wart we should not propagate to a new endpoint.

### Error statuses (caller mistakes only)

| Status | When | Body |
|---|---|---|
| 400 | invalid JSON; missing required param; unknown param key | `{"error": "..."}` |
| 404 | unknown method name | `{"error": "unknown method \"x\""}` |
| 429 | method-run limiter full | `{"error": "too many concurrent method runs"}` |
| 500 | internal failure | `{"error": "..."}` |

Param validation runs **before** execution, against the method's declared
`Params` (each has `Name`/`Required`): every `Required` param must be present;
any caller key not declared is rejected. This reuses the existing
`methods.ValidateParams` — the same presence semantics as UseCase input
validation.

## Concurrency — dedicated limiter

Direct method runs get their **own** semaphore, independent of
`KATO_MAX_CONCURRENT` (which stays UseCase-only):

- New env `KATO_METHOD_MAX_CONCURRENT`, default `10`.
- Non-blocking acquire; full → `429`.

Rationale: method calls are cheap single checks bounded by the step timeout
(~30s), while UseCase runs are minutes-long LLM flows. A shared cap would let an
agent probing with method calls starve real troubleshooting runs, and vice versa.

## Execution semantics

- Timeout: the existing `KATO_STEP_TIMEOUT` (default 30s) bounds the method call,
  same as a step inside a flow.
- Deps: the same `methods.Deps` (Kube client, Metrics, Prober) the engine already
  holds — the handler resolves the method from `methods.Builtin()`'s registry and
  invokes it directly. No engine flow machinery involved.
- Read-only guarantees unchanged: methods only `get/list/watch` (or probe), so
  this endpoint adds no write capability.

## Architecture

A thin handler in `internal/server` beside `runUseCase`:

1. Resolve `{name}` in the method registry → 404 if absent.
2. Decode body; validate params against the method's declarations → 400.
3. Acquire the method-run semaphore (non-blocking) → 429.
4. Execute the method with `KATO_STEP_TIMEOUT` context and the server's deps.
5. Map the method result to the response view: error from the method →
   `outcome: failed` + `error`; success → `outcome: completed` + outputs
   (scalars + lists merged into one `outputs` object, as in step results).

Config: `KATO_METHOD_MAX_CONCURRENT` added to `internal/config` with the other
knobs.

## Docs

- `openapi.yaml`: new path + `MethodRunRequest` / `MethodRunResponse` schemas.
- `docs/METHOD.md`: short "running a method directly" section noting statelessness
  and the param rules.
- README env-var table: `KATO_METHOD_MAX_CONCURRENT`.

## Testing strategy

Handler-level (`internal/server`), table tests, fake deps — no live cluster:

- **success** — known method, valid params → 200, `outcome: completed`, outputs
  present (scalar + list).
- **method failure is 200** — method returns an error → 200, `outcome: failed`,
  `error` set, no HTTP error.
- **unknown method** → 404.
- **missing required param / unknown param / invalid JSON** → 400 with message;
  method never executed.
- **limiter full** → 429 (fill the semaphore, assert the next call rejects).
- **timeout** — a method exceeding the step timeout surfaces as
  `outcome: failed` with a deadline error.
- Config: `KATO_METHOD_MAX_CONCURRENT` parses, defaults to 10. (Non-positive
  values are accepted and yield a zero-capacity limiter — every call 429s —
  mirroring `KATO_MAX_CONCURRENT`'s existing behavior; a floor/warning is a
  possible future refinement, deliberately not added for consistency.)

## Consumer note

kato-bot's MCP/REST proxy (see kato-bot's
`2026-07-22-kato-bot-mcp-rest-proxy-design.md`) is the first consumer: its
`run_method` MCP tool and `POST /api/v1/clusters/{cluster}/methods/{name}/run`
proxy route both forward to this endpoint. This spec ships first.
{% endraw %}
