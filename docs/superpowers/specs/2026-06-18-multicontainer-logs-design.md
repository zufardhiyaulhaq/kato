# Multi-Container Pod Logs Design
{% raw %}
**Date:** 2026-06-18
**Status:** Approved

## Problem

`check_pod_logs` fails on multi-container pods when no container is specified. The
`pod-crashloop` example UseCase has a `previous-logs` step that calls `check_pod_logs`
with `previous: "true"` and no `container`:

```yaml
- name: previous-logs
  method: check_pod_logs
  when: $(steps.status.restartCount) > 0
  with:
    namespace: $(inputs.namespace)
    name: $(inputs.pod)
    previous: "true"
    tailLines: "20"
    maxLineLength: "200"
  summaryFilter: [logs]
```

On a pod with more than one container (e.g. an app container plus a sidecar), the
Kubernetes logs subresource cannot pick a container for you and rejects the request.
The observed failure:

```
get logs <ns>/<pod>: the server rejected our request for an unknown reason (get pods <pod>)
outcome: failed
```

`check_pod_logs` already accepts an optional `container` param (pod_logs.go), but the
UseCase never sets it, so the request goes out with an empty container on a
multi-container pod and is rejected.

## Goal

Make pod-log collection work on multi-container pods without forcing every UseCase
author to name a container, while still allowing per-container targeting and explicit
per-container looping.

## Decisions

1. **Do both** a method-level auto-loop and forEach enablement (user: "why not both").
2. **`container` stays an input.** Set → fetch that one container. Empty → loop all
   containers (user: "container as input right? so if container is empty will loop everything").
3. **List outputs on both** `check_pod_status` and `describe_pod` (user: "both").
4. **All containers**, not only restarted ones (user: "All containers").
5. **No engine changes.** The engine already supports `forEach` over a list output and
   `$(item.<field>)` substitution; we only add capabilities methods already model.

## Architecture

Two independent, complementary capabilities. Neither touches the engine.

1. **`check_pod_logs` auto-loop** — the actual bug fix. When `container` is empty, the
   method `Get`s the pod, enumerates its containers, and fetches logs per container,
   concatenating them with per-container headers.
2. **Container list outputs** — `check_pod_status` and `describe_pod` each expose a
   container list via the existing `ListProducer` interface, so any UseCase can
   `forEach: $(steps.X.<list>)` and pass `container: $(item.name)` for per-container
   `summaryFilter` control.

The crashloop UseCase is fixed purely by capability (1); its YAML does not change.

## Component 1 — `check_pod_logs` auto-loop

**File:** `internal/methods/pod_logs.go`

- **`container` set** → unchanged: a single `GetLogs(name, opts).DoRaw(ctx)`, hard error
  on failure. This preserves today's behavior and today's tests.
- **`container` empty** → `Pods(ns).Get(ctx, name, …)`, enumerate `Spec.Containers` and
  `Spec.InitContainers` (init containers crashloop too), and for each container fetch
  logs with the same options (`previous`, `tailLines`) but that container's name set.
  Aggregate with per-container headers:

  ```
  === container: app ===
  <logs>

  === container: istio-proxy ===
  (no previous logs: previous terminated container not found)
  ```

- **Error handling:** per-container failures become inline notes (shown in place of that
  container's logs); the step as a whole still succeeds. Rationale: a healthy sidecar
  with no previous instance must not fail the whole diagnosis. The method hard-fails only
  when the pod `Get` itself fails (e.g. pod not found).
- **Truncation:** `maxLineLength` (`ClampLineLength`) is applied per container; the final
  aggregated string is clamped with `Truncate(…, defaultLogBytes)` exactly as today.
- **Output field unchanged:** still a single `logs` string. `Params()` is unchanged (the
  `container` param already exists and its description already says "empty = first
  container" — update that description to "empty = all containers").

**Edge cases:**
- Pod with a single container + empty `container` → loop of one; output is the single
  container's logs under one header. (Acceptable; consistent. A future refinement could
  drop the header when there is exactly one container, but it is not required.)
- All containers fail (e.g. `previous: true` on a pod where nothing has a previous
  instance) → the step still returns the aggregated notes rather than hard-failing; "no
  previous logs anywhere" is itself diagnostic signal for the summary.
- Ephemeral containers are out of scope (rare for this use case).

## Component 2 — `check_pod_status` container list output

**File:** `internal/methods/pod_status.go`

Implement `ListProducer.ListOutputs()` returning one list whose items describe each
container. Candidate item fields: `name`, `restartCount`, `ready`. The list name must
not collide with any existing scalar output of `check_pod_status` (the engine stores
scalar and list outputs in the same `Outputs` map, keyed by field name). The exact list
name is confirmed against the method's current outputs during planning; if `containers`
is free it is used, otherwise a distinct name is chosen.

`Run` populates the list from `pod.Status.ContainerStatuses` (name, restartCount, ready).

## Component 3 — `describe_pod` container list output

**File:** `internal/methods/pod_describe.go`

`describe_pod` already emits scalar `containers` (comma-joined string) and `images`.
Keep those. Add `ListProducer.ListOutputs()` returning a list under a **non-colliding**
name — `containerList` — with items `{name, image}`, populated from `pod.Spec.Containers`.

## Data flow (forEach usage, illustrative)

A UseCase that wants explicit per-container control can do:

```yaml
- name: status
  method: check_pod_status
  with: { namespace: $(inputs.namespace), name: $(inputs.pod) }
- name: logs
  forEach: $(steps.status.containers)
  method: check_pod_logs
  with:
    namespace: $(inputs.namespace)
    name: $(inputs.pod)
    container: $(item.name)
    previous: "true"
  summaryFilter: [logs]
```

This is optional. The crashloop UseCase keeps its single `previous-logs` step and relies
on Component 1's auto-loop.

## Testing

**`check_pod_logs`:**
- `container` set → single container, unchanged (existing test stays green).
- `container` empty + multi-container pod → aggregated output contains a header for each
  container.
- `container` empty + one container with no previous instance → that container shows an
  inline note, the other container's logs are present, step succeeds.
- Pod `Get` fails → hard error.
- Init containers are included in the loop.

  Note: client-go's fake clientset returns a fixed body from `GetLogs(...).DoRaw` and does
  not vary by container, so asserting per-container differentiation follows whatever fake
  `pod_logs_test.go` already uses (likely an httptest-backed REST client); the exact
  mechanism is confirmed during planning against the existing test file.

**`check_pod_status`:** `ListOutputs()` shape; a `Run` over a fake multi-container pod
produces the populated container list (name, restartCount, ready).

**`describe_pod`:** `ListOutputs()` shape; a `Run` produces `containerList` with
`{name, image}` per `Spec.Containers`; existing scalar `containers`/`images` outputs
unchanged.

## Out of scope / YAGNI

- No engine changes.
- No new methods.
- No change to `pod-crashloop.yaml` logic (it starts working via Component 1).
- No restarted-only filtering (user chose all containers).
- No ephemeral-container support.

## Constraint

Do not commit. All changes stay in the working tree (standing user instruction).
{% endraw %}
