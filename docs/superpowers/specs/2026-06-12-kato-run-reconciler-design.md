# kato RunReconciler Design

**Status:** Approved
**Date:** 2026-06-12

## Problem

Today a flow runs only when the REST API receives `POST /api/v1/usecases/{name}/run`.
The server executes synchronously and writes a `Run` CR as an audit record *after*
the flow finishes. There is no way to trigger a run by creating a `Run` CR with
`kubectl` or GitOps — the `Run` CR is an output, never an input.

We want a `Run` CR to also be a **trigger**: `kubectl apply` a `Run`, kato executes
the flow, and the result lands on the same `Run`'s `status` (`kubectl get run … -o yaml`).

## Goals

- Creating a `Run` CR externally (kubectl/GitOps) executes the referenced UseCase.
- The existing synchronous REST path is unchanged (still fast, still writes an audit `Run`).
- No double execution: API-written audit `Run`s are never re-executed by the reconciler.
- Idempotent: a `Run` executes exactly once even under repeated reconciles/restarts.
- Bad `Run`s (missing/not-Ready UseCase, invalid inputs) fail cleanly with a clear status.
- A controller crash mid-run does not strand a `Run` forever.

## Non-goals

- Re-running a `Run` (spec edits, re-trigger annotations). A `Run` is execute-once and
  immutable; to re-run, create a new `Run`.
- Changing the REST API surface (no new async endpoint).
- Cluster-wide deletion of user-created `Run`s (TTL GC stays namespaced — see below).

## Design

### 1. Additive reconciler, label-based separation

A new `RunReconciler` watches `Run` resources. Its event predicate **skips any Run
labeled `kato.zufardhiyaulhaq.com/managed-by: api`**. The REST path's `SaveRun` sets
that label atomically at `Create` time, so API audit `Run`s are never reconciled —
only externally-created `Run`s are. The two paths never both execute the same `Run`.

### 2. Phase state machine (execute-once + Running guard)

`RunStatus.Phase` gains a `Running` value. On reconcile of an external `Run`:

```
phase == ""        -> validate; if invalid: phase=Failed (+ note), done.
                      else: claim (phase=Running, startedAt=now), Execute,
                      write terminal phase + steps + summary.
phase == Running   -> no-op (in flight, or crashed -> reaped by the GC sweep).
phase terminal     -> no-op (Succeeded | PartiallySucceeded | Failed).
```

The claim is the idempotency anchor. controller-runtime serializes reconciles per
object key, and the claim is a `Status().Update` carrying the object's
`resourceVersion`; a stale-cache double-claim loses the optimistic-concurrency race
(HTTP 409), requeues, re-reads `Running`, and no-ops. Persisting `Running` *before*
executing is what lets the reaper (below) recover a crash.

### 3. Validation gating (before claim)

While `phase == ""`, the reconciler gates on the same contract the REST `/run` path
uses, turning every failure into terminal `phase=Failed` with `status.note`:

- UseCase missing (not in `UseCaseCache`) → note `useCase "X" not found`.
- UseCase not Ready (`UseCaseCache.IsReady` false) → note `useCase "X" is not Ready`.
- Invalid inputs → after claim, `engine.Execute` returns a typed `*engine.InputError`;
  the reconciler writes `phase=Failed` with that message as the note. (On the API path
  this is HTTP 400 with nothing persisted; here the `Run` already exists, so the error
  is recorded on its status.)

A malformed `Run` thus reaches a terminal phase in one reconcile and is then inert.

### 4. Reuse the engine — identical results

The reconciler is handed `engine.Engine.Execute` (the same function the server holds,
via an `ExecuteFunc` type) and the shared `UseCaseCache`. An externally-triggered run
produces byte-identical results to an API one; only the destination differs (the
`Run`'s status vs. an HTTP response). The reconciler writes status with its own cached
client (`mgr.GetClient()`) so claims use optimistic concurrency.

### 5. Concurrency

The REST path's `MaxConcurrent` semaphore (HTTP 429 beyond N) is unchanged and **not**
shared. Reconciler-triggered runs are bounded the idiomatic way — `MaxConcurrentReconciles`
on the `RunReconciler`, via `KATO_RUN_RECONCILE_CONCURRENCY` (default 2). The two paths
have different backpressure semantics (the API rejects; the reconciler queues), so
coupling them to one semaphore would let one starve the other.

### 6. Reaping stuck-Running runs

If the controller crashes between claim and the terminal write, a `Run` is stranded in
`Running` (an already-phased `Run` is a reconcile no-op). The existing GC loop
(`KATO_GC_INTERVAL`) is extended to also reap: any `Run` with `phase==Running` whose
`status.startedAt` is older than `KATO_RUN_MAX_DURATION` (default 1h) is force-marked
`phase=Failed` with note `run exceeded max duration (…); controller likely restarted
mid-run`. The threshold is a flat config value (predictable) set comfortably above
`StepTimeout × worst-case step count`. A normally-completing run writes its terminal
phase well within the window.

### 7. Namespacing & RBAC

External `Run`s may be applied in any namespace, so the reconciler watches `Run`s
cluster-wide and writes `runs/status` cluster-wide. The chart's read-only `ClusterRole`
gains `runs` + `runs/status` (`get;list;watch` and `update;patch`). Stuck-run **reaping**
(a status flip, not a delete) is therefore also cluster-wide.

TTL **garbage-collection (delete)** stays **namespaced to kato**: it reaps the API's own
audit trail, which only accumulates in kato's namespace. kato does not delete `Run`s it
did not create — externally-created `Run`s in other namespaces are the user's to manage
(GitOps/kubectl). No cluster-wide `delete` permission is granted.

## Data model changes

- `RunStatus.Phase` enum: add `Running`.
- `RunStatus.Note string`: reconciler-level message (validation failure reason / reap note).
- Label constant `kato.zufardhiyaulhaq.com/managed-by` with value `api`, set by `SaveRun`.
- `engine.PhaseRunning = "Running"`.
- Config: `RunReconcileConcurrency` (`KATO_RUN_RECONCILE_CONCURRENCY`, default 2),
  `RunMaxDuration` (`KATO_RUN_MAX_DURATION`, default 1h).

## Testing

Envtest integration suite (matches `internal/controller`), reconciler injected with a
stub `ExecuteFunc`:

- External `Run`, empty phase → executes once; terminal phase + summary + steps on status.
- API-labeled `Run` → predicate skips it; never executed.
- Missing UseCase → `Failed` + note, no execution.
- Not-Ready UseCase → `Failed` + note.
- Invalid inputs (stub returns `*engine.InputError`) → `Failed` + note.
- Already-terminal / already-Running `Run` → no-op.
- Reap: `Running` `Run` with old `startedAt` → swept to `Failed` with the duration note.

Unit tests: `store.BuildRunStatus` mapping; `store.ReapStuckRuns` (fake client).
```
