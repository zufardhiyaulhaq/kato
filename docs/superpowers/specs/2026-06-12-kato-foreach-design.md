# kato — List Output + `forEach` Fan-out Design

**Date:** 2026-06-12
**Module:** `github.com/zufardhiyaulhaq/kato`
**Status:** Approved
**Extends:** `docs/superpowers/specs/2026-06-12-kato-design.md` (v1 linear flow model)

## 1. Problem

kato's flow is strictly linear: each step runs at most once (gated by `when`)
and produces flat scalar outputs. There is no way to discover a *set* of objects
and act on each one. The motivating case: "for the `node-local-dns` DaemonSet,
list the pods that are failing, then fetch logs for each." This needs two new
capabilities:

1. A method that returns a **list** of failing pods for a given workload.
2. A **fan-out** construct so a later step runs once per list item, with a hard
   cap on how many items are processed.

This design adds both while preserving kato's core guarantees: deterministic
execution, AI-as-summarizer-only, and predictable `when` matching.

## 2. Decisions

| Decision | Choice |
|---|---|
| List shape | Structured items — a named list output whose items have typed fields |
| Fan-out scope | `forEach` on a **single** step (runs its method once per item) |
| Cap | `maxItems` default 5, hard ceiling 20, items processed worst-first; truncation recorded |
| New method | `list_failing_pods` — inputs `kind`+`name`+`namespace`, returns failing pods |
| Workload kinds | Deployment, DaemonSet, StatefulSet |
| Failing criteria | Caller-tunable toggles with sensible defaults |
| Matching guarantee | `when`/`$(steps.x.y)` resolve only to **scalars**; lists are consumable **only** by `forEach` |
| Nesting | A `forEach` step's method is a normal method; nested `forEach` is rejected in v1 |

## 3. The `list_failing_pods` method

### Inputs

| Input | Required | Default | Description |
|---|---|---|---|
| `namespace` | yes | — | workload namespace |
| `kind` | yes | — | `Deployment` \| `DaemonSet` \| `StatefulSet` |
| `name` | yes | — | workload name |
| `minRestarts` | no | `0` | only include pods with `restartCount` ≥ this |
| `includeCrashLoop` | no | `true` | count `CrashLoopBackOff` pods |
| `includeImagePull` | no | `true` | count `ImagePullBackOff` / `ErrImagePull` / `CreateContainerError` |
| `includeOOM` | no | `true` | count pods whose last termination was `OOMKilled` or non-zero exit |
| `includeNotReady` | no | `false` | count any not-Ready pod (broadest; off by default) |

`include*` inputs are parsed as bools; absent = the default above. `minRestarts`
is parsed as an int.

### Outputs

Scalar outputs (usable in `when` / `$(...)`):

| Name | Type | Description |
|---|---|---|
| `count` | int | number of failing pods matched |
| `anyFailing` | bool | `count > 0` |

List output `pods` — items sorted **worst-first** (descending `restartCount`):

| Item field | Type | Description |
|---|---|---|
| `namespace` | string | pod namespace |
| `name` | string | pod name |
| `reason` | string | dominant failure reason, e.g. `CrashLoopBackOff`, `OOMKilled` |
| `restartCount` | int | max restartCount across the pod's containers |

### Matching

A pod is "failing" if it matches **any** enabled criterion:
- `includeCrashLoop`: a container waiting with reason `CrashLoopBackOff`.
- `includeImagePull`: a container waiting with reason `ImagePullBackOff`,
  `ErrImagePull`, or `CreateContainerError`.
- `includeOOM`: a container whose last termination reason is `OOMKilled` or
  whose last exit code is non-zero.
- `includeNotReady`: the pod's Ready condition is not True.

After matching, pods below `minRestarts` are dropped.

### Owner resolution

- **DaemonSet / StatefulSet**: list pods in the namespace; keep those with an
  `ownerReference` whose `Kind`+`Name` equal the requested workload.
- **Deployment**: list ReplicaSets in the namespace owned by the deployment
  (by `ownerReference`), collect their names, then keep pods owned by any of
  those ReplicaSets (the two-hop case).

Read-only throughout. Errors (workload not found, RBAC denied) propagate as a
method error → the step is a `failed` finding, consistent with spec §6.3.

## 4. List-output model

### Method contract additions

`internal/methods/method.go` gains:

```go
// ListOutputField declares a list output: a named list whose items are records
// of typed fields. A method may declare zero or more.
type ListOutputField struct {
    Name        string
    ItemFields  []OutputField   // typed fields each item carries
    Description string
}
```

The `Method` interface gains `ListOutputs() []ListOutputField` (methods without
lists return nil — the existing 15 are unchanged via a default).

`Outputs` (`map[string]any`) gains one allowed value shape: a value may be a
list of records, represented as `[]map[string]any`, in addition to
string/int64/bool. A list output's value must be a `[]map[string]any` whose
records match the declared `ItemFields`.

### Guarantee: matching stays scalar-only

`when` conditions and `$(steps.<step>.<field>)` references resolve **only** to
scalar outputs. A list output may be referenced **only** by `forEach`. This
keeps `when` typing and predictability exactly as in v1 — a condition can never
depend on list-shaped data.

## 5. The `forEach` step (CRD)

`api/v1alpha1` `Step` gains two optional fields:

```go
type Step struct {
    Name   string `json:"name"`
    Method string `json:"method"`
    When   string `json:"when,omitempty"`
    With   map[string]string `json:"with,omitempty"`
    SummaryFilter []string `json:"summaryFilter,omitempty"`

    // ForEach references a LIST output of an earlier step, e.g.
    // "$(steps.crashing.pods)". When set, Method runs once per item and
    // $(item.<field>) is available in With (not in When; see §5).
    ForEach string `json:"forEach,omitempty"`
    // MaxItems caps iterations. Default 5; hard ceiling 20.
    MaxItems int `json:"maxItems,omitempty"`
}
```

Example:

```yaml
apiVersion: kato.zufardhiyaulhaq.com/v1alpha1
kind: UseCase
metadata:
  name: daemonset-crashloop
spec:
  description: "List failing pods of a workload and fetch each one's logs"
  inputs:
    - name: namespace
      required: true
    - name: kind
      required: true
    - name: workload
      required: true
  steps:
    - name: crashing
      method: list_failing_pods
      with:
        namespace: $(inputs.namespace)
        kind: $(inputs.kind)
        name: $(inputs.workload)
    - name: logs
      forEach: $(steps.crashing.pods)
      maxItems: 3
      when: $(steps.crashing.anyFailing)
      method: check_pod_logs
      with:
        namespace: $(item.namespace)
        name: $(item.name)
        previous: "true"
      summaryFilter: [logs]
  summary:
    prompt: |
      Some pods of this workload are failing. Using each pod's reason and logs,
      explain the common root cause and suggest a fix.
```

### Reference kinds

- `$(item.<field>)` is a new reference kind, valid **only** inside a `forEach`
  step. `<field>` must be a declared item field of the referenced list.
- `forEach` value must be exactly one `$(steps.<step>.<listOutput>)` reference
  to a list output of an **earlier** step.

### `when` on a `forEach` step

`when`, if present, is a whole-step gate evaluated **once** before iterating,
using scalar references only (as in v1). Per-item filtering is the list method's
job (its criteria inputs). `$(item.*)` is **not** available in a `forEach`
step's `when` in v1 (only in `with`).

## 6. Engine semantics

On reaching a `forEach` step:

1. Evaluate `when` once (scalar refs). False → `skipped`.
2. Resolve the referenced list from prior-step state. If the source step did not
   complete, auto-skip with the existing dependency rule.
3. Determine the limit: `n = min(maxItems_or_default, 20)`. Default 5 when
   `maxItems` is unset. Record a note if the matched list is longer than `n`.
4. Take the first `n` items (the list is already worst-first from the method).
5. For each item: build a scope where `$(item.<field>)` resolves to that item's
   fields; substitute into `with`; validate params; run the method under the
   per-step timeout. Record a per-iteration result (item, outcome, outputs,
   error). A per-iteration method error is a `failed` finding for that
   iteration; the loop continues.
6. Step outcome: `completed` if ≥1 item was processed; `skipped` if `when` was
   false or zero items matched (reason recorded).

Phase computation (spec §6) is unchanged: a `forEach` step contributes one
outcome to the run phase. Internal per-iteration failures do not by themselves
make the run `Failed`.

### Run record

`RunStep` gains:

```go
type RunStepIteration struct {
    Item    map[string]string     `json:"item"`              // the item's fields
    Outcome string                `json:"outcome"`            // completed | failed
    Outputs *apiextensionsv1.JSON `json:"outputs,omitempty"`
    Error   string                `json:"error,omitempty"`
}

// On RunStep:
Iterations []RunStepIteration `json:"iterations,omitempty"`
Note       string             `json:"note,omitempty"`  // e.g. truncation note
```

Truncation note format: `"matched 12, checked 3 (worst-first); 9 not examined"`.

### Evidence to the LLM

A `forEach` step renders as one section per iteration: the item identity plus
that iteration's `summaryFilter`-filtered outputs, followed by the note. The
single end-of-run LLM call still covers the whole run (cost stays predictable;
the ceiling of 20 bounds iteration count).

## 7. Validation (watch-time)

In addition to existing rules:

- A `forEach` value must be a single `$(steps.<step>.<field>)` referencing a
  **list** output of an earlier step → else `Ready=False`.
- `$(item.<field>)` in `with` must name a declared item field of that list →
  else `Ready=False` (message lists valid fields).
- `$(item.*)` used in a step **without** `forEach` → `Ready=False`.
- A `forEach` step must declare `method` and `with` → else `Ready=False`.
- `maxItems` < 0 → `Ready=False`. `maxItems` > 20 is allowed but clamped at run
  time with a note.
- A `forEach` step's `method` must not itself be a fan-out (no nested
  `forEach`) — structurally impossible since `method` names a built-in method,
  but validation asserts the referenced list is produced by a non-`forEach`
  step.
- `forEach` and a scalar-only `method` step remain distinguishable: presence of
  `forEach` makes it a fan-out step.

## 8. Files touched

No new packages; extends existing structure.

| File | Change |
|---|---|
| `api/v1alpha1/usecase_types.go` | `Step.ForEach`, `Step.MaxItems`; `RunStep.Iterations`, `RunStep.Note`; `RunStepIteration` type; regenerate deepcopy + CRDs |
| `internal/methods/method.go` | `ListOutputField`, `ListOutputs()` on the interface, list values in `Outputs`, validation helpers |
| `internal/methods/list_failing_pods.go` (+ test) | new method incl. owner resolution for the three kinds |
| `internal/engine/refs.go` | parse `$(item.<field>)` |
| `internal/engine/validate.go` | `forEach` / list / `item` rules |
| `internal/engine/engine.go` | per-item iteration, cap clamp, truncation note, outcome |
| `internal/summarizer/summarizer.go` | render `forEach` iterations in evidence |
| `internal/server/server.go` | `GET /api/v1/methods` exposes list outputs |
| `charts/kato/templates/rbac.yaml` | add `statefulsets` read |
| `docs/METHOD.md` | document `list_failing_pods` and list outputs |
| `examples/usecases/` | add `daemonset-crashloop.yaml` |

## 9. Testing

- **Unit (methods):** owner resolution for DaemonSet, StatefulSet, and the
  Deployment→ReplicaSet→Pod two-hop, against fake clients; each failing
  criterion (CrashLoop, ImagePull, OOM, NotReady) and `minRestarts`; worst-first
  ordering; `count`/`anyFailing`; the `pods` list shape.
- **Unit (engine):** `$(item.*)` extraction and substitution; `forEach`
  validation (good and each rejection case); iteration execution with cap clamp,
  worst-first selection, zero-items skip, per-iteration failure recorded,
  `when=false` skip.
- **Unit (summarizer):** evidence renders per-iteration outputs + note;
  `summaryFilter` applied per iteration.
- **Integration (envtest):** a UseCase with a `forEach` step reaches
  `Ready=True`; a malformed one (`$(item.*)` without `forEach`) reaches
  `Ready=False`.

## 10. Out of scope (v1 of this feature)

- Nested `forEach` (a fan-out inside a fan-out).
- `forEach` over a block/sub-sequence of steps (only single-step fan-out).
- `$(item.*)` in a `forEach` step's `when`.
- Per-item conditional skipping beyond the list method's own criteria.
