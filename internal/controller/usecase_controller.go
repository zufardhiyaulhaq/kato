package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gopaytech/kato/api/v1alpha1"
	"github.com/gopaytech/kato/internal/engine"
	"github.com/gopaytech/kato/internal/methods"
)

// UseCaseReconciler validates each UseCase (spec §4) and maintains the cache +
// Ready condition. It performs NO cluster mutation beyond status.
type UseCaseReconciler struct {
	client.Client
	Cache        *UseCaseCache
	Registry     *methods.Registry
	ModelConfigs *ModelConfigCache
}

func (r *UseCaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var uc v1alpha1.UseCase
	if err := r.Get(ctx, req.NamespacedName, &uc); err != nil {
		if errors.IsNotFound(err) {
			r.Cache.Delete(req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	problems := engine.ValidateUseCase(&uc, r.Registry, r.ModelConfigs.Exists)
	ready := len(problems) == 0
	r.Cache.Set(&uc, ready)

	cond := metav1.Condition{
		Type: "Ready", ObservedGeneration: uc.Generation,
		LastTransitionTime: metav1.Now(),
	}
	if ready {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, "Validated", "use case is valid"
	} else {
		cond.Status, cond.Reason = metav1.ConditionFalse, "ValidationFailed"
		cond.Message = joinProblems(problems)
	}
	changed := setCondition(&uc.Status.Conditions, cond)
	if changed {
		if err := r.Status().Update(ctx, &uc); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *UseCaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.UseCase{}).Complete(r)
}
