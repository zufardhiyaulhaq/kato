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

	"github.com/gopaytech/kato/api/v1alpha1"
	"github.com/gopaytech/kato/internal/engine"
	"github.com/gopaytech/kato/internal/store"
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
	concurrency := r.Concurrency
	if concurrency < 1 {
		concurrency = 1 // 0 would let controller-runtime silently default; be explicit
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Run{}, builder.WithPredicates(notAPIManaged)).
		WithOptions(controller.Options{MaxConcurrentReconciles: concurrency}).
		Complete(r)
}
