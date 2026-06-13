# RunReconciler Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **STANDING USER INSTRUCTION — DO NOT COMMIT.** This project leaves all changes in the working tree. Skip every `git commit`/`git add` step. There are no commit steps in this plan; do not add any.

**Goal:** Make creating a `Run` CR (kubectl/GitOps) trigger flow execution, with results written back to the same `Run`'s status — without changing the existing synchronous REST path.

**Architecture:** A new `RunReconciler` watches `Run`s cluster-wide, skips API-written audit `Run`s via a `managed-by: api` label predicate, and executes each external `Run` exactly once using a phase state machine (`"" → Running → terminal`) anchored by an optimistic-concurrency claim. It reuses the same `engine.Execute` and `UseCaseCache` the server holds. A staleness sweep in the existing GC loop reaps `Run`s stuck in `Running` after a controller crash.

**Tech Stack:** Go, controller-runtime v0.20.4, envtest, Helm. Design spec: `docs/superpowers/specs/2026-06-12-kato-run-reconciler-design.md`.

---

## File Structure

- `api/v1alpha1/run_types.go` — add `Running` to the `Phase` enum, add `RunStatus.Note`, add `ManagedByLabel`/`ManagedByAPI` constants.
- `internal/engine/engine.go` — add `PhaseRunning` constant.
- `internal/config/config.go` — add `RunReconcileConcurrency`, `RunMaxDuration`.
- `internal/store/store.go` — set `managed-by` label in `SaveRun`; extract pure `BuildRunStatus`; add `ReapStuckRuns`.
- `internal/controller/run_controller.go` — **new**: `ExecuteFunc`, `RunReconciler`, `Reconcile`, `SetupWithManager`.
- `internal/controller/run_controller_test.go` — **new**: envtest reconciler suite.
- `cmd/kato/main.go` — wire `RunReconciler`; extend `gcRunnable` to reap.
- `charts/kato/templates/rbac.yaml` — grant cluster-wide `runs` get/list/watch + `runs/status` update/patch.
- `.env.example`, `DEVELOPMENT.md` — document the two new env vars and the kubectl-driven run flow.

---

## Task 1: Run API types — Running phase, Note, label constants

**Files:**
- Modify: `api/v1alpha1/run_types.go`
- Regenerate: `config/crd/bases/kato.zufardhiyaulhaq.com_runs.yaml`, copy to `charts/kato/crds/`

- [ ] **Step 1: Add `Running` to the Phase enum and a `Note` field**

In `api/v1alpha1/run_types.go`, change the `RunStatus.Phase` marker and field block from:

```go
type RunStatus struct {
	// +kubebuilder:validation:Enum=Succeeded;PartiallySucceeded;Failed
	Phase       string       `json:"phase,omitempty"`
	StartedAt   *metav1.Time `json:"startedAt,omitempty"`
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	Steps       []RunStep    `json:"steps,omitempty"`
	Summary     string       `json:"summary,omitempty"`
```

to:

```go
type RunStatus struct {
	// +kubebuilder:validation:Enum=Running;Succeeded;PartiallySucceeded;Failed
	Phase       string       `json:"phase,omitempty"`
	StartedAt   *metav1.Time `json:"startedAt,omitempty"`
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	Steps       []RunStep    `json:"steps,omitempty"`
	Summary     string       `json:"summary,omitempty"`
	// Note records a reconciler-level message: a validation failure reason for an
	// externally-created Run, or the reap note when a stuck Running Run is failed.
	Note string `json:"note,omitempty"`
```

(Leave the rest of `RunStatus` — `Warning`, `ModelConfig` — unchanged.)

- [ ] **Step 2: Add the managed-by label constants**

At the top of `api/v1alpha1/run_types.go`, immediately after the `import (...)` block, add:

```go
// ManagedByLabel marks a Run created by the REST API as an audit record. The
// RunReconciler skips Runs carrying it (value ManagedByAPI) so API-written Runs
// are never re-executed; externally-created Runs (kubectl/GitOps) omit it.
const (
	ManagedByLabel = "kato.zufardhiyaulhaq.com/managed-by"
	ManagedByAPI   = "api"
)
```

- [ ] **Step 3: Verify it builds**

Run: `go build ./api/...`
Expected: no output (success).

- [ ] **Step 4: Regenerate CRD manifests and sync the chart copy**

Run:
```bash
make manifests
cp config/crd/bases/kato.zufardhiyaulhaq.com_runs.yaml charts/kato/crds/
```
Expected: `config/crd/bases/kato.zufardhiyaulhaq.com_runs.yaml` now lists `Running` in the `phase` enum and a `note` property under `status`. Confirm with:
```bash
grep -A4 'phase:' config/crd/bases/kato.zufardhiyaulhaq.com_runs.yaml | grep -i running
grep -n 'note:' config/crd/bases/kato.zufardhiyaulhaq.com_runs.yaml
```
Expected: both greps match.

---

## Task 2: engine PhaseRunning constant

**Files:**
- Modify: `internal/engine/engine.go:17-19`

- [ ] **Step 1: Add the constant**

In `internal/engine/engine.go`, change the phase const block from:

```go
	PhaseSucceeded          = "Succeeded"
	PhasePartiallySucceeded = "PartiallySucceeded"
	PhaseFailed             = "Failed"
```

to:

```go
	PhaseRunning            = "Running"
	PhaseSucceeded          = "Succeeded"
	PhasePartiallySucceeded = "PartiallySucceeded"
	PhaseFailed             = "Failed"
```

`PhaseRunning` is set by the reconciler's claim and the reaper; `phaseOf` (which only ever returns the three terminal phases) is unchanged.

- [ ] **Step 2: Verify it builds**

Run: `go build ./internal/engine/...`
Expected: no output (success).

---

## Task 3: config — RunReconcileConcurrency and RunMaxDuration

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"testing"
	"time"
)

func TestLoadRunReconcilerDefaults(t *testing.T) {
	t.Setenv("KATO_RUN_RECONCILE_CONCURRENCY", "")
	t.Setenv("KATO_RUN_MAX_DURATION", "")
	cfg := Load()
	if cfg.RunReconcileConcurrency != 2 {
		t.Errorf("RunReconcileConcurrency default = %d, want 2", cfg.RunReconcileConcurrency)
	}
	if cfg.RunMaxDuration != time.Hour {
		t.Errorf("RunMaxDuration default = %s, want 1h", cfg.RunMaxDuration)
	}
}

func TestLoadRunReconcilerOverrides(t *testing.T) {
	t.Setenv("KATO_RUN_RECONCILE_CONCURRENCY", "5")
	t.Setenv("KATO_RUN_MAX_DURATION", "30m")
	cfg := Load()
	if cfg.RunReconcileConcurrency != 5 {
		t.Errorf("RunReconcileConcurrency = %d, want 5", cfg.RunReconcileConcurrency)
	}
	if cfg.RunMaxDuration != 30*time.Minute {
		t.Errorf("RunMaxDuration = %s, want 30m", cfg.RunMaxDuration)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config/... -run TestLoadRunReconciler -v`
Expected: FAIL — `cfg.RunReconcileConcurrency` / `cfg.RunMaxDuration` undefined (compile error).

- [ ] **Step 3: Add the fields and loading**

In `internal/config/config.go`, add the two fields to the `Config` struct (after `GCInterval`):

```go
type Config struct {
	Namespace     string        // kato's own namespace (for Runs + Secrets)
	ListenAddr    string        // e.g. ":8080"
	StepTimeout   time.Duration
	RunTTL        time.Duration
	MaxConcurrent int
	GCInterval    time.Duration
	// RunReconcileConcurrency bounds concurrent execution of externally-created
	// Runs (MaxConcurrentReconciles); separate from the API's MaxConcurrent.
	RunReconcileConcurrency int
	// RunMaxDuration is the staleness threshold after which a Run stuck in
	// Running (controller crashed mid-run) is reaped to Failed.
	RunMaxDuration time.Duration
}
```

and the two lines to `Load()` (after `GCInterval`):

```go
func Load() Config {
	return Config{
		Namespace:               getEnv("KATO_NAMESPACE", "kato"),
		ListenAddr:              getEnv("KATO_LISTEN_ADDR", ":8080"),
		StepTimeout:             getDuration("KATO_STEP_TIMEOUT", 30*time.Second),
		RunTTL:                  getDuration("KATO_RUN_TTL", 7*24*time.Hour),
		MaxConcurrent:           getInt("KATO_MAX_CONCURRENT", 10),
		GCInterval:              getDuration("KATO_GC_INTERVAL", time.Hour),
		RunReconcileConcurrency: getInt("KATO_RUN_RECONCILE_CONCURRENCY", 2),
		RunMaxDuration:          getDuration("KATO_RUN_MAX_DURATION", time.Hour),
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/config/... -run TestLoadRunReconciler -v`
Expected: PASS (both subtests).

---

## Task 4: store — managed-by label, BuildRunStatus, ReapStuckRuns

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go` (append)

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/store_test.go`:

```go
func ptrTime(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}

func TestBuildRunStatusMapsResult(t *testing.T) {
	res := engine.Result{
		Phase:       engine.PhaseSucceeded,
		Summary:     "ok",
		ModelConfig: "openai",
		Steps: []engine.StepResult{
			{Name: "s", Outcome: engine.OutcomeCompleted, Outputs: map[string]any{"phase": "Running"}},
		},
	}
	start := time.Unix(100, 0)
	end := time.Unix(200, 0)
	st, err := BuildRunStatus(res, start, end)
	if err != nil {
		t.Fatalf("BuildRunStatus: %v", err)
	}
	if st.Phase != engine.PhaseSucceeded || st.Summary != "ok" || st.ModelConfig != "openai" {
		t.Errorf("status = %+v", st)
	}
	if len(st.Steps) != 1 || st.Steps[0].Outputs == nil {
		t.Errorf("steps = %+v", st.Steps)
	}
	if st.StartedAt == nil || !st.StartedAt.Time.Equal(start) {
		t.Errorf("startedAt = %v, want %v", st.StartedAt, start)
	}
}

func TestSaveRunSetsManagedByLabel(t *testing.T) {
	c := newFakeClient(t)
	s := &Store{Client: c, Namespace: "kato"}
	run, err := s.SaveRun(context.Background(), "pod-crashloop", nil,
		engine.Result{Phase: engine.PhaseSucceeded}, time.Unix(1, 0), time.Unix(2, 0))
	if err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	if run.Labels[v1alpha1.ManagedByLabel] != v1alpha1.ManagedByAPI {
		t.Errorf("managed-by label = %q, want %q", run.Labels[v1alpha1.ManagedByLabel], v1alpha1.ManagedByAPI)
	}
}

func TestReapStuckRunsFailsOnlyStale(t *testing.T) {
	now := time.Unix(10000, 0)
	stuck := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "stuck", Namespace: "default"},
		Status:     v1alpha1.RunStatus{Phase: engine.PhaseRunning, StartedAt: ptrTime(now.Add(-2 * time.Hour))},
	}
	fresh := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "fresh", Namespace: "default"},
		Status:     v1alpha1.RunStatus{Phase: engine.PhaseRunning, StartedAt: ptrTime(now.Add(-time.Minute))},
	}
	done := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "done", Namespace: "kato"},
		Status:     v1alpha1.RunStatus{Phase: engine.PhaseSucceeded, StartedAt: ptrTime(now.Add(-3 * time.Hour))},
	}
	c := newFakeClient(t, stuck, fresh, done)
	s := &Store{Client: c, Namespace: "kato", TTL: time.Hour}

	n, err := s.ReapStuckRuns(context.Background(), now, time.Hour)
	if err != nil {
		t.Fatalf("ReapStuckRuns: %v", err)
	}
	if n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	var got v1alpha1.Run
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "stuck"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != engine.PhaseFailed {
		t.Errorf("stuck phase = %q, want Failed", got.Status.Phase)
	}
	if got.Status.Note == "" {
		t.Error("stuck Note is empty")
	}
	var stillFresh v1alpha1.Run
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "fresh"}, &stillFresh); err != nil {
		t.Fatal(err)
	}
	if stillFresh.Status.Phase != engine.PhaseRunning {
		t.Errorf("fresh phase = %q, want Running (untouched)", stillFresh.Status.Phase)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/... -run 'BuildRunStatus|ManagedBy|ReapStuck' -v`
Expected: FAIL — `BuildRunStatus` and `ReapStuckRuns` undefined; `ManagedByLabel` reference is the only compiling part.

- [ ] **Step 3: Add the label constants to the existing usecaseLabel block**

In `internal/store/store.go`, the label is referenced via the exported `v1alpha1` constants, so no new local const is needed. Change the `SaveRun` `Labels` map from:

```go
			Labels:       map[string]string{usecaseLabel: useCase},
```

to:

```go
			Labels:       map[string]string{usecaseLabel: useCase, v1alpha1.ManagedByLabel: v1alpha1.ManagedByAPI},
```

- [ ] **Step 4: Extract BuildRunStatus and use it in SaveRun**

In `internal/store/store.go`, replace the body of `SaveRun` *after* the `Create` call (everything from `steps := make(...)` through the final `return run, nil`) with a call to a new pure function. The resulting `SaveRun` tail becomes:

```go
	if err := s.Client.Create(ctx, run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}

	status, err := BuildRunStatus(res, startedAt, completedAt)
	if err != nil {
		return nil, err
	}
	run.Status = status
	if err := s.Client.Status().Update(ctx, run); err != nil {
		return nil, fmt.Errorf("update run status: %w", err)
	}
	return run, nil
}
```

Then add the new function below `SaveRun`:

```go
// BuildRunStatus maps an engine.Result to a RunStatus (steps, iterations,
// summary, timing). Pure: the REST path (SaveRun) and the RunReconciler share it
// so externally-triggered runs record identical status to API ones.
func BuildRunStatus(res engine.Result, startedAt, completedAt time.Time) (v1alpha1.RunStatus, error) {
	steps := make([]v1alpha1.RunStep, 0, len(res.Steps))
	for _, sr := range res.Steps {
		rs := v1alpha1.RunStep{Name: sr.Name, Outcome: sr.Outcome, Reason: sr.Reason, Error: sr.Error, Note: sr.Note}
		if len(sr.Outputs) > 0 {
			raw, err := json.Marshal(sr.Outputs)
			if err != nil {
				return v1alpha1.RunStatus{}, fmt.Errorf("marshal outputs for step %s: %w", sr.Name, err)
			}
			rs.Outputs = &apiextensionsv1.JSON{Raw: raw}
		}
		for _, it := range sr.Iterations {
			ri := v1alpha1.RunStepIteration{Item: it.Item, Outcome: it.Outcome, Error: it.Error}
			if len(it.Outputs) > 0 {
				raw, err := json.Marshal(it.Outputs)
				if err != nil {
					return v1alpha1.RunStatus{}, fmt.Errorf("marshal iteration outputs for step %s: %w", sr.Name, err)
				}
				ri.Outputs = &apiextensionsv1.JSON{Raw: raw}
			}
			rs.Iterations = append(rs.Iterations, ri)
		}
		steps = append(steps, rs)
	}
	started := metav1.NewTime(startedAt)
	completed := metav1.NewTime(completedAt)
	return v1alpha1.RunStatus{
		Phase:       res.Phase,
		StartedAt:   &started,
		CompletedAt: &completed,
		Steps:       steps,
		Summary:     res.Summary,
		Warning:     res.Warning,
		ModelConfig: res.ModelConfig,
	}, nil
}
```

- [ ] **Step 5: Add ReapStuckRuns**

Below `GarbageCollect` in `internal/store/store.go`, add:

```go
// ReapStuckRuns force-fails Runs stuck in Running longer than maxDuration — the
// signature of a controller that crashed after claiming a Run but before writing
// its terminal phase. It scans cluster-wide (external Runs may live in any
// namespace) and flips status only; it never deletes. Returns the count reaped.
func (s *Store) ReapStuckRuns(ctx context.Context, now time.Time, maxDuration time.Duration) (int, error) {
	var list v1alpha1.RunList
	if err := s.Client.List(ctx, &list); err != nil {
		return 0, fmt.Errorf("list runs: %w", err)
	}
	reaped := 0
	for i := range list.Items {
		run := &list.Items[i]
		if run.Status.Phase != engine.PhaseRunning || run.Status.StartedAt == nil {
			continue
		}
		if now.Sub(run.Status.StartedAt.Time) <= maxDuration {
			continue
		}
		completed := metav1.NewTime(now)
		run.Status.Phase = engine.PhaseFailed
		run.Status.CompletedAt = &completed
		run.Status.Note = fmt.Sprintf("run exceeded max duration (%s); controller likely restarted mid-run", maxDuration)
		if err := s.Client.Status().Update(ctx, run); err != nil {
			if errors.IsConflict(err) {
				continue // another writer won the race; the next sweep retries
			}
			return reaped, fmt.Errorf("reap run %s: %w", run.Name, err)
		}
		reaped++
	}
	return reaped, nil
}
```

(`errors` — `k8s.io/apimachinery/pkg/api/errors` — and `engine` are already imported by `store.go`.)

- [ ] **Step 6: Run the store tests to verify they pass**

Run: `go test ./internal/store/... -count=1 -v`
Expected: PASS — the new tests plus all existing `SaveRun`/`GetRun`/`ListRuns`/`GarbageCollect` tests (the `BuildRunStatus` refactor must not change `SaveRun` behavior).

---

## Task 5: RunReconciler

**Files:**
- Create: `internal/controller/run_controller.go`
- Create: `internal/controller/run_controller_test.go`

- [ ] **Step 1: Write the reconciler**

Create `internal/controller/run_controller.go`:

```go
package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/engine"
	"github.com/zufardhiyaulhaq/kato/internal/store"
)

// ExecuteFunc runs a flow (engine.Engine.Execute satisfies it). Mirrors the
// server's ExecuteFunc so the reconciler shares the same engine.
type ExecuteFunc func(ctx context.Context, uc *v1alpha1.UseCase, inputs map[string]string) (engine.Result, error)

// RunReconciler executes externally-created Run CRs (kubectl/GitOps). API-written
// audit Runs (label ManagedByLabel=ManagedByAPI) are filtered out by the predicate
// in SetupWithManager and ignored here, so they are never re-executed. A Run is
// executed exactly once: only an unphased Run is actionable, and the Running claim
// is an optimistic-concurrency Status().Update that serializes competing reconciles.
type RunReconciler struct {
	client.Client
	UseCases    *UseCaseCache
	Execute     ExecuteFunc
	Now         func() time.Time // injectable clock; defaults to time.Now
	Concurrency int              // MaxConcurrentReconciles
}

func (r *RunReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *RunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var run v1alpha1.Run
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Defense in depth: the predicate already excludes API-managed Runs.
	if run.Labels[v1alpha1.ManagedByLabel] == v1alpha1.ManagedByAPI {
		return ctrl.Result{}, nil
	}
	// Execute-once: any non-empty phase (Running or terminal) is a no-op.
	if run.Status.Phase != "" {
		return ctrl.Result{}, nil
	}

	now := r.now()

	// Gate on the UseCase contract before claiming, mirroring the REST /run path.
	uc, ok := r.UseCases.GetUseCase(run.Spec.UseCase)
	if !ok {
		return ctrl.Result{}, r.fail(ctx, &run, fmt.Sprintf("useCase %q not found", run.Spec.UseCase), now)
	}
	if !r.UseCases.IsReady(run.Spec.UseCase) {
		return ctrl.Result{}, r.fail(ctx, &run, fmt.Sprintf("useCase %q is not Ready", run.Spec.UseCase), now)
	}

	// Claim: persist Running before executing so a crash leaves a reapable Run and
	// a concurrent reconcile observes the claim. A stale-cache double-claim loses
	// the optimistic-concurrency race (409), requeues, and re-reads Running.
	started := metav1.NewTime(now)
	run.Status.Phase = engine.PhaseRunning
	run.Status.StartedAt = &started
	if err := r.Status().Update(ctx, &run); err != nil {
		return ctrl.Result{}, err
	}

	res, execErr := r.Execute(ctx, uc, run.Spec.Inputs)
	completed := metav1.NewTime(r.now())
	if execErr != nil {
		// Execute only errors on invalid caller inputs (engine.InputError); on the
		// REST path that is HTTP 400 with nothing persisted, but the Run already
		// exists, so record the reason on its status.
		var ie *engine.InputError
		note := execErr.Error()
		if !errors.As(execErr, &ie) {
			note = fmt.Sprintf("execution error: %s", execErr.Error())
		}
		run.Status.Phase = engine.PhaseFailed
		run.Status.CompletedAt = &completed
		run.Status.Note = note
		return ctrl.Result{}, r.Status().Update(ctx, &run)
	}

	status, err := store.BuildRunStatus(res, started.Time, completed.Time)
	if err != nil {
		return ctrl.Result{}, err
	}
	run.Status = status
	return ctrl.Result{}, r.Status().Update(ctx, &run)
}

// fail writes a terminal Failed status with a human-readable note (validation
// failure before any step ran).
func (r *RunReconciler) fail(ctx context.Context, run *v1alpha1.Run, note string, now time.Time) error {
	t := metav1.NewTime(now)
	run.Status.Phase = engine.PhaseFailed
	run.Status.StartedAt = &t
	run.Status.CompletedAt = &t
	run.Status.Note = note
	return r.Status().Update(ctx, run)
}

func (r *RunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	notAPIManaged := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetLabels()[v1alpha1.ManagedByLabel] != v1alpha1.ManagedByAPI
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Run{}, builder.WithPredicates(notAPIManaged)).
		WithOptions(controller.Options{MaxConcurrentReconciles: r.Concurrency}).
		Complete(r)
}
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./internal/controller/...`
Expected: no output (success).

- [ ] **Step 3: Write the envtest reconciler test**

Create `internal/controller/run_controller_test.go`:

```go
package controller

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/engine"
)

func runReq(ns, name string) ctrl.Request {
	return ctrl.Request{NamespacedName: client.ObjectKey{Namespace: ns, Name: name}}
}

func getRun(t *testing.T, ctx context.Context, c client.Client, ns, name string) *v1alpha1.Run {
	t.Helper()
	var run v1alpha1.Run
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &run); err != nil {
		t.Fatal(err)
	}
	return &run
}

func TestRunReconciler(t *testing.T) {
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Skipf("envtest unavailable (set KUBEBUILDER_ASSETS): %v", err)
	}
	defer env.Stop()

	c := newClient(t, cfg)
	ctx := context.Background()
	ucCache := NewUseCaseCache()
	ucCache.Set(&v1alpha1.UseCase{ObjectMeta: metav1.ObjectMeta{Name: "uc-ready"}}, true)
	ucCache.Set(&v1alpha1.UseCase{ObjectMeta: metav1.ObjectMeta{Name: "uc-bad"}}, false)

	fixedNow := time.Now()
	executed := 0
	rec := &RunReconciler{
		Client:   c,
		UseCases: ucCache,
		Now:      func() time.Time { return fixedNow },
		Execute: func(_ context.Context, _ *v1alpha1.UseCase, _ map[string]string) (engine.Result, error) {
			executed++
			return engine.Result{
				Phase:   engine.PhaseSucceeded,
				Summary: "all good",
				Steps:   []engine.StepResult{{Name: "s", Outcome: engine.OutcomeCompleted}},
			}, nil
		},
	}

	mkRun := func(name string, labels map[string]string, useCase string) {
		run := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: labels},
			Spec:       v1alpha1.RunSpec{UseCase: useCase},
		}
		if err := c.Create(ctx, run); err != nil {
			t.Fatal(err)
		}
	}

	// External Run executes once -> Succeeded + summary on status.
	mkRun("ext-1", nil, "uc-ready")
	if _, err := rec.Reconcile(ctx, runReq("default", "ext-1")); err != nil {
		t.Fatal(err)
	}
	got := getRun(t, ctx, c, "default", "ext-1")
	if got.Status.Phase != engine.PhaseSucceeded || got.Status.Summary != "all good" {
		t.Fatalf("ext-1 status = %+v", got.Status)
	}
	if executed != 1 {
		t.Fatalf("executed = %d, want 1", executed)
	}
	// Re-reconcile is a no-op (already terminal).
	if _, err := rec.Reconcile(ctx, runReq("default", "ext-1")); err != nil {
		t.Fatal(err)
	}
	if executed != 1 {
		t.Fatalf("re-executed: executed = %d", executed)
	}

	// API-managed Run is ignored (never executed).
	mkRun("api-1", map[string]string{v1alpha1.ManagedByLabel: v1alpha1.ManagedByAPI}, "uc-ready")
	if _, err := rec.Reconcile(ctx, runReq("default", "api-1")); err != nil {
		t.Fatal(err)
	}
	if getRun(t, ctx, c, "default", "api-1").Status.Phase != "" {
		t.Fatal("api-1 was executed")
	}
	if executed != 1 {
		t.Fatalf("api-1 triggered execution: executed = %d", executed)
	}

	// Missing UseCase -> Failed + note, no execution.
	mkRun("miss-1", nil, "nope")
	if _, err := rec.Reconcile(ctx, runReq("default", "miss-1")); err != nil {
		t.Fatal(err)
	}
	miss := getRun(t, ctx, c, "default", "miss-1")
	if miss.Status.Phase != engine.PhaseFailed || !strings.Contains(miss.Status.Note, "not found") {
		t.Fatalf("miss-1 status = %+v", miss.Status)
	}

	// Not-Ready UseCase -> Failed + note.
	mkRun("notready-1", nil, "uc-bad")
	if _, err := rec.Reconcile(ctx, runReq("default", "notready-1")); err != nil {
		t.Fatal(err)
	}
	nr := getRun(t, ctx, c, "default", "notready-1")
	if nr.Status.Phase != engine.PhaseFailed || !strings.Contains(nr.Status.Note, "not Ready") {
		t.Fatalf("notready-1 status = %+v", nr.Status)
	}

	// Invalid inputs (Execute returns *engine.InputError) -> Failed + note.
	recBad := &RunReconciler{
		Client:   c,
		UseCases: ucCache,
		Now:      func() time.Time { return fixedNow },
		Execute: func(_ context.Context, _ *v1alpha1.UseCase, _ map[string]string) (engine.Result, error) {
			return engine.Result{}, &engine.InputError{Msg: `missing required input "namespace"`}
		},
	}
	mkRun("badinput-1", nil, "uc-ready")
	if _, err := recBad.Reconcile(ctx, runReq("default", "badinput-1")); err != nil {
		t.Fatal(err)
	}
	bi := getRun(t, ctx, c, "default", "badinput-1")
	if bi.Status.Phase != engine.PhaseFailed || !strings.Contains(bi.Status.Note, "missing required input") {
		t.Fatalf("badinput-1 status = %+v", bi.Status)
	}

	// Already-Running Run is a no-op (in flight).
	mkRun("running-1", nil, "uc-ready")
	r1 := getRun(t, ctx, c, "default", "running-1")
	r1.Status.Phase = engine.PhaseRunning
	if err := c.Status().Update(ctx, r1); err != nil {
		t.Fatal(err)
	}
	before := executed
	if _, err := rec.Reconcile(ctx, runReq("default", "running-1")); err != nil {
		t.Fatal(err)
	}
	if executed != before {
		t.Fatalf("running-1 was executed: executed = %d", executed)
	}
	if getRun(t, ctx, c, "default", "running-1").Status.Phase != engine.PhaseRunning {
		t.Fatal("running-1 phase changed")
	}
}
```

- [ ] **Step 4: Run the reconciler test to verify it passes**

Run: `make test-integration` (sets `KUBEBUILDER_ASSETS`), or directly:
```bash
KUBEBUILDER_ASSETS="$(bin/setup-envtest use 1.32.0 -p path)" go test ./internal/controller/... -run TestRunReconciler -count=1 -v
```
Expected: PASS. (If envtest binaries are unavailable the test self-skips; ensure `make test-integration` runs it for real.)

---

## Task 6: Wire RunReconciler and the reaper into main.go

**Files:**
- Modify: `cmd/kato/main.go`

- [ ] **Step 1: Register the RunReconciler**

In `cmd/kato/main.go`, after the `eng := &engine.Engine{...}` construction (around line 107) and before `st := &store.Store{...}`, add:

```go
	if err := (&controller.RunReconciler{
		Client:      mgr.GetClient(),
		UseCases:    ucCache,
		Execute:     eng.Execute,
		Concurrency: cfg.RunReconcileConcurrency,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup run controller: %w", err)
	}
```

The reconciler uses the cached `mgr.GetClient()` (so claims get optimistic concurrency and the cluster-wide Run watch is informer-backed); this requires the cluster-wide `runs` RBAC added in Task 7.

- [ ] **Step 2: Extend gcRunnable to reap stuck runs**

In `cmd/kato/main.go`, change the `gcRunnable` struct from:

```go
type gcRunnable struct {
	store    *store.Store
	interval time.Duration
	log      interface{ Info(string, ...any) }
}
```

to:

```go
type gcRunnable struct {
	store       *store.Store
	interval    time.Duration
	maxDuration time.Duration
	log         interface{ Info(string, ...any) }
}
```

and change its `Start` ticker body from:

```go
		case <-t.C:
			n, err := g.store.GarbageCollect(ctx, time.Now())
			if err != nil {
				g.log.Info("run GC error", "err", err.Error())
			} else if n > 0 {
				g.log.Info("garbage-collected runs", "count", n)
			}
		}
```

to:

```go
		case <-t.C:
			n, err := g.store.GarbageCollect(ctx, time.Now())
			if err != nil {
				g.log.Info("run GC error", "err", err.Error())
			} else if n > 0 {
				g.log.Info("garbage-collected runs", "count", n)
			}
			reaped, err := g.store.ReapStuckRuns(ctx, time.Now(), g.maxDuration)
			if err != nil {
				g.log.Info("run reap error", "err", err.Error())
			} else if reaped > 0 {
				g.log.Info("reaped stuck runs", "count", reaped)
			}
		}
```

- [ ] **Step 3: Pass maxDuration when constructing gcRunnable**

In `cmd/kato/main.go`, change the `mgr.Add(gcRunnable{...})` line from:

```go
	if err := mgr.Add(gcRunnable{st, cfg.GCInterval, log}); err != nil {
		return err
	}
```

to:

```go
	if err := mgr.Add(gcRunnable{store: st, interval: cfg.GCInterval, maxDuration: cfg.RunMaxDuration, log: log}); err != nil {
		return err
	}
```

- [ ] **Step 4: Verify the whole module builds**

Run: `go build ./...`
Expected: no output (success).

---

## Task 7: RBAC — cluster-wide runs for the reconciler

**Files:**
- Modify: `charts/kato/templates/rbac.yaml`

- [ ] **Step 1: Grant cluster-wide runs access in the ClusterRole**

In `charts/kato/templates/rbac.yaml`, inside the `ClusterRole` named `{{ include "kato.name" . }}-reader`, after the existing `usecases/status, modelconfigs/status` rule (the block ending at the `---` before the ClusterRoleBinding), add two rules:

```yaml
  - apiGroups: ["kato.zufardhiyaulhaq.com"]
    resources: [runs]
    verbs: [get, list, watch]
  - apiGroups: ["kato.zufardhiyaulhaq.com"]
    resources: [runs/status]
    verbs: [update, patch]
```

This lets the RunReconciler watch externally-created `Run`s in any namespace and write their status (including the cluster-wide reap). The namespaced `Role` (`-runs`) is unchanged: it still grants `create`/`delete` on `runs` in kato's namespace for the REST audit-write path and TTL garbage-collection, which stay namespaced.

- [ ] **Step 2: Lint the chart**

Run: `helm lint charts/kato`
Expected: `1 chart(s) linted, 0 chart(s) failed`.

- [ ] **Step 3: Confirm the rendered RBAC includes runs**

Run: `helm template charts/kato | grep -A2 'resources: \[runs\]'`
Expected: shows the `runs` rule with `get, list, watch`.

---

## Task 8: Document the new env vars and kubectl-driven runs

**Files:**
- Modify: `.env.example`
- Modify: `DEVELOPMENT.md`

- [ ] **Step 1: Add the two env vars to .env.example**

In `.env.example`, after the `KATO_GC_INTERVAL` block and before the `KUBECONFIG` block, add:

```bash
# Max concurrent execution of externally-created (kubectl/GitOps) Run CRs. This
# is separate from KATO_MAX_CONCURRENT, which bounds the REST API path.
KATO_RUN_RECONCILE_CONCURRENCY=2

# A Run stuck in phase=Running longer than this (controller crashed mid-run) is
# reaped to phase=Failed by the GC sweep. Go duration syntax.
KATO_RUN_MAX_DURATION=1h
```

- [ ] **Step 2: Document kubectl-driven runs in DEVELOPMENT.md**

In `DEVELOPMENT.md`, after the `## Run locally against a cluster` section's step 4 ("Try a UseCase"), add a new subsection:

````markdown
### Triggering a run with kubectl (no API call)

Besides the REST API, you can trigger a run by creating a `Run` CR directly —
useful for GitOps. The `RunReconciler` executes any externally-created `Run`
(one without the `kato.zufardhiyaulhaq.com/managed-by: api` label the API sets)
exactly once and writes the result to the same `Run`'s status:

```bash
kubectl apply -f - <<'EOF'
apiVersion: kato.zufardhiyaulhaq.com/v1alpha1
kind: Run
metadata:
  generateName: pod-crashloop-
  namespace: default
spec:
  useCase: pod-crashloop
  inputs:
    namespace: default
    pod: some-pod
EOF

kubectl get run -n default                 # PHASE: Running -> Succeeded/Failed
kubectl get run -n default <name> -o yaml  # per-step outcomes + summary
```

If `spec.useCase` is missing or not Ready, or inputs are invalid, the run ends
`Failed` with the reason in `status.note`. A `Run` is execute-once and immutable —
to re-run, create a new one. Runs created this way are bounded by
`KATO_RUN_RECONCILE_CONCURRENCY` (separate from the API's `KATO_MAX_CONCURRENT`),
and a run stranded in `Running` by a controller crash is reaped to `Failed` after
`KATO_RUN_MAX_DURATION`.
````

- [ ] **Step 3: Final full build and test sweep**

Run:
```bash
go build ./...
go test ./... -count=1
make test-integration
helm lint charts/kato
```
Expected: build clean; all unit tests pass; envtest suite (including `TestRunReconciler`) passes; chart lints clean.

---

## Self-Review Notes

- **Spec coverage:** §1 label separation → Tasks 1,4,5,7. §2 phase state machine → Tasks 1,2,5. §3 validation gating → Task 5. §4 reuse engine → Tasks 5,6. §5 concurrency → Tasks 3,5,6. §6 reaping → Tasks 3,4,6. §7 namespacing/RBAC → Task 7. Data-model changes → Tasks 1,2,3. Testing → Tasks 3,4,5,8.
- **Type consistency:** `ManagedByLabel`/`ManagedByAPI` defined in `api/v1alpha1` (Task 1) and consumed by store (Task 4) and controller (Task 5). `BuildRunStatus(engine.Result, time.Time, time.Time) (v1alpha1.RunStatus, error)` defined in Task 4, consumed in Task 5. `PhaseRunning` defined in Task 2, used in Tasks 4,5. `ExecuteFunc` in Task 5 matches `eng.Execute` wired in Task 6. `gcRunnable.maxDuration` added in Task 6 and fed `cfg.RunMaxDuration` from Task 3.
- **No placeholders:** every code step shows complete code; every run step states the expected result.
