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
