package methods

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkStatefulSetStatus struct{}

func (checkStatefulSetStatus) Name() string { return "check_statefulset_status" }
func (checkStatefulSetStatus) Description() string {
	return "StatefulSet replica counts and rollout state"
}

func (checkStatefulSetStatus) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "StatefulSet namespace"},
		{Name: "name", Required: true, Description: "StatefulSet name"},
	}
}

func (checkStatefulSetStatus) OutputFields() []OutputField {
	return []OutputField{
		{Name: "desiredReplicas", Type: FieldInt, Description: "spec.replicas (1 if unset)"},
		{Name: "readyReplicas", Type: FieldInt, Description: "status.readyReplicas"},
		{Name: "currentReplicas", Type: FieldInt, Description: "status.currentReplicas"},
		{Name: "updatedReplicas", Type: FieldInt, Description: "status.updatedReplicas"},
		{Name: "availableReplicas", Type: FieldInt, Description: "status.availableReplicas"},
		{Name: "updateRevisionPending", Type: FieldBool, Description: "currentRevision != updateRevision (rollout in flight)"},
	}
}

func (checkStatefulSetStatus) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	s, err := deps.Kube.AppsV1().StatefulSets(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get statefulset %s/%s: %w", params["namespace"], params["name"], err)
	}
	desired := int64(1)
	if s.Spec.Replicas != nil {
		desired = int64(*s.Spec.Replicas)
	}
	return Outputs{
		"desiredReplicas":       desired,
		"readyReplicas":         int64(s.Status.ReadyReplicas),
		"currentReplicas":       int64(s.Status.CurrentReplicas),
		"updatedReplicas":       int64(s.Status.UpdatedReplicas),
		"availableReplicas":     int64(s.Status.AvailableReplicas),
		"updateRevisionPending": s.Status.UpdateRevision != "" && s.Status.CurrentRevision != s.Status.UpdateRevision,
	}, nil
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkStatefulSetStatus{}) })
}
