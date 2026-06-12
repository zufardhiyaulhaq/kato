package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
)

// ModelConfigReconciler keeps the ModelConfig cache in sync and reports a
// Ready condition (config is structurally validated by the CRD schema).
type ModelConfigReconciler struct {
	client.Client
	Cache *ModelConfigCache
}

func (r *ModelConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var mc v1alpha1.ModelConfig
	if err := r.Get(ctx, req.NamespacedName, &mc); err != nil {
		if errors.IsNotFound(err) {
			r.Cache.Delete(req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	r.Cache.Set(&mc)

	cond := metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, Reason: "Loaded",
		Message: "model config loaded", ObservedGeneration: mc.Generation,
		LastTransitionTime: metav1.Now(),
	}
	if setCondition(&mc.Status.Conditions, cond) {
		if err := r.Status().Update(ctx, &mc); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *ModelConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.ModelConfig{}).Complete(r)
}
