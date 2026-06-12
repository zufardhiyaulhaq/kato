package methods

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkReplicaSet struct{}

func (checkReplicaSet) Name() string        { return "check_replicaset" }
func (checkReplicaSet) Description() string { return "State of the ReplicaSets owned by a deployment" }

func (checkReplicaSet) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Namespace"},
		{Name: "deployment", Required: true, Description: "Owning deployment name"},
	}
}

func (checkReplicaSet) OutputFields() []OutputField {
	return []OutputField{
		{Name: "replicaFailure", Type: FieldBool, Description: "any owned RS has ReplicaFailure=True"},
		{Name: "failureReason", Type: FieldString, Description: `e.g. FailedCreate, "" if none`},
		{Name: "failureMessage", Type: FieldString, Description: `failure message, "" if none`},
		{Name: "activeReplicaSets", Type: FieldInt, Description: "owned RS with desired replicas > 0"},
	}
}

func (checkReplicaSet) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	list, err := deps.Kube.AppsV1().ReplicaSets(params["namespace"]).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list replicasets in %s: %w", params["namespace"], err)
	}
	out := Outputs{
		"replicaFailure": false, "failureReason": "", "failureMessage": "",
		"activeReplicaSets": int64(0),
	}
	for _, rs := range list.Items {
		owned := false
		for _, ref := range rs.OwnerReferences {
			if ref.Kind == "Deployment" && ref.Name == params["deployment"] {
				owned = true
			}
		}
		if !owned {
			continue
		}
		if rs.Spec.Replicas != nil && *rs.Spec.Replicas > 0 {
			out["activeReplicaSets"] = out["activeReplicaSets"].(int64) + 1
		}
		for _, c := range rs.Status.Conditions {
			if c.Type == appsv1.ReplicaSetReplicaFailure && c.Status == corev1.ConditionTrue {
				out["replicaFailure"] = true
				out["failureReason"], out["failureMessage"] = c.Reason, c.Message
			}
		}
	}
	return out, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkReplicaSet{}) }) }
