package controller

import (
	"context"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
)

func TestModelConfigReconciler(t *testing.T) {
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
	cache := NewModelConfigCache()
	rec := &ModelConfigReconciler{Client: c, Cache: cache}

	// Create -> reconcile -> cached + Ready=True.
	mc := &v1alpha1.ModelConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default-llm"},
		Spec: v1alpha1.ModelConfigSpec{
			Default: true, BaseURL: "http://llm.local/v1", Model: "gpt-x",
		},
	}
	if err := c.Create(ctx, mc); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Reconcile(ctx, reconcileReq("default-llm")); err != nil {
		t.Fatal(err)
	}
	if !cache.Exists("default-llm") {
		t.Error("model config not added to cache after reconcile")
	}
	var got v1alpha1.ModelConfig
	if err := c.Get(ctx, client.ObjectKey{Name: "default-llm"}, &got); err != nil {
		t.Fatal(err)
	}
	if !hasReadyTrue(got.Status.Conditions) {
		t.Errorf("Ready=True not set; conditions = %+v", got.Status.Conditions)
	}

	// Delete -> reconcile -> removed from cache.
	if err := c.Delete(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Reconcile(ctx, reconcileReq("default-llm")); err != nil {
		t.Fatal(err)
	}
	if cache.Exists("default-llm") {
		t.Error("model config not removed from cache after delete")
	}
}

func hasReadyTrue(conds []metav1.Condition) bool {
	for _, c := range conds {
		if c.Type == "Ready" && c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}
