package controller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

// Requires envtest binaries:
//
//	make envtest   (installs setup-envtest and exports KUBEBUILDER_ASSETS)
//
// CI sets KUBEBUILDER_ASSETS; locally run `make test-integration`.
func TestUseCaseReadyCondition(t *testing.T) {
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Skipf("envtest unavailable (set KUBEBUILDER_ASSETS): %v", err)
	}
	defer func() { _ = env.Stop() }()

	c := newClient(t, cfg)
	ucCache := NewUseCaseCache()
	mcCache := NewModelConfigCache()
	rec := &UseCaseReconciler{Client: c, Cache: ucCache, Registry: methods.Builtin(), ModelConfigs: mcCache}

	ctx := context.Background()

	// Apply a VALID use case, reconcile, expect Ready=True.
	valid := &v1alpha1.UseCase{
		ObjectMeta: metav1.ObjectMeta{Name: "valid"},
		Spec: v1alpha1.UseCaseSpec{
			Inputs:  []v1alpha1.InputDecl{{Name: "namespace", Required: true}, {Name: "pod", Required: true}},
			Steps:   []v1alpha1.Step{{Name: "s", Method: "check_pod_status", With: map[string]string{"namespace": "$(inputs.namespace)", "name": "$(inputs.pod)"}}},
			Summary: v1alpha1.SummarySpec{Prompt: "x"},
		},
	}
	if err := c.Create(ctx, valid); err != nil {
		t.Fatal(err)
	}
	reconcileAndCheck(t, ctx, c, rec, "valid", metav1.ConditionTrue)

	// Apply an INVALID use case (unknown method), expect Ready=False.
	invalid := &v1alpha1.UseCase{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid"},
		Spec: v1alpha1.UseCaseSpec{
			Steps:   []v1alpha1.Step{{Name: "s", Method: "check_nonexistent"}},
			Summary: v1alpha1.SummarySpec{Prompt: "x"},
		},
	}
	if err := c.Create(ctx, invalid); err != nil {
		t.Fatal(err)
	}
	reconcileAndCheck(t, ctx, c, rec, "invalid", metav1.ConditionFalse)

	if !ucCache.IsReady("valid") || ucCache.IsReady("invalid") {
		t.Error("cache readiness does not match conditions")
	}
}

func newClient(t *testing.T, cfg *rest.Config) client.Client {
	t.Helper()
	scheme := runtimeScheme(t)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func reconcileAndCheck(t *testing.T, ctx context.Context, c client.Client,
	rec *UseCaseReconciler, name string, want metav1.ConditionStatus) {
	t.Helper()
	if _, err := rec.Reconcile(ctx, reconcileReq(name)); err != nil {
		t.Fatalf("reconcile %s: %v", name, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var got v1alpha1.UseCase
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &got); err != nil {
			t.Fatal(err)
		}
		for _, cond := range got.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == want {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: Ready did not reach %s", name, want)
}
