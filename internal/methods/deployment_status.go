package methods

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkDeploymentStatus struct{}

func (checkDeploymentStatus) Name() string { return "check_deployment_status" }
func (checkDeploymentStatus) Description() string {
	return "Deployment replica counts and rollout conditions"
}

func (checkDeploymentStatus) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Deployment namespace"},
		{Name: "name", Required: true, Description: "Deployment name"},
	}
}

func (checkDeploymentStatus) OutputFields() []OutputField {
	return []OutputField{
		{Name: "desiredReplicas", Type: FieldInt, Description: "spec.replicas"},
		{Name: "readyReplicas", Type: FieldInt, Description: "status.readyReplicas"},
		{Name: "updatedReplicas", Type: FieldInt, Description: "status.updatedReplicas"},
		{Name: "available", Type: FieldBool, Description: "Available condition is True"},
		{Name: "progressing", Type: FieldBool, Description: "Progressing condition is True"},
		{Name: "progressingReason", Type: FieldString, Description: `e.g. ProgressDeadlineExceeded, "" if progressing`},
	}
}

func (checkDeploymentStatus) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	d, err := deps.Kube.AppsV1().Deployments(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get deployment %s/%s: %w", params["namespace"], params["name"], err)
	}
	desired := int64(1)
	if d.Spec.Replicas != nil {
		desired = int64(*d.Spec.Replicas)
	}
	out := Outputs{
		"desiredReplicas": desired,
		"readyReplicas":   int64(d.Status.ReadyReplicas),
		"updatedReplicas": int64(d.Status.UpdatedReplicas),
		"available":       false, "progressing": false, "progressingReason": "",
	}
	for _, c := range d.Status.Conditions {
		isTrue := c.Status == corev1.ConditionTrue
		switch c.Type {
		case appsv1.DeploymentAvailable:
			out["available"] = isTrue
		case appsv1.DeploymentProgressing:
			out["progressing"] = isTrue
			if !isTrue {
				out["progressingReason"] = c.Reason
			}
		}
	}
	return out, nil
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkDeploymentStatus{}) })
}
