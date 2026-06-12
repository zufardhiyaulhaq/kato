package methods

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkNodeStatus struct{}

func (checkNodeStatus) Name() string        { return "check_node_status" }
func (checkNodeStatus) Description() string { return "Node readiness and pressure conditions" }

func (checkNodeStatus) Params() []Param {
	return []Param{{Name: "name", Required: true, Description: "Node name"}}
}

func (checkNodeStatus) OutputFields() []OutputField {
	return []OutputField{
		{Name: "ready", Type: FieldBool, Description: "Ready condition is True"},
		{Name: "readyReason", Type: FieldString, Description: `Ready condition reason, "" if ready`},
		{Name: "memoryPressure", Type: FieldBool, Description: "MemoryPressure condition is True"},
		{Name: "diskPressure", Type: FieldBool, Description: "DiskPressure condition is True"},
		{Name: "pidPressure", Type: FieldBool, Description: "PIDPressure condition is True"},
		{Name: "unschedulable", Type: FieldBool, Description: "spec.unschedulable (cordoned)"},
	}
}

func (checkNodeStatus) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	node, err := deps.Kube.CoreV1().Nodes().Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get node %s: %w", params["name"], err)
	}
	out := Outputs{
		"ready": false, "readyReason": "",
		"memoryPressure": false, "diskPressure": false, "pidPressure": false,
		"unschedulable": node.Spec.Unschedulable,
	}
	for _, c := range node.Status.Conditions {
		isTrue := c.Status == corev1.ConditionTrue
		switch c.Type {
		case corev1.NodeReady:
			out["ready"] = isTrue
			if !isTrue {
				out["readyReason"] = c.Reason
			}
		case corev1.NodeMemoryPressure:
			out["memoryPressure"] = isTrue
		case corev1.NodeDiskPressure:
			out["diskPressure"] = isTrue
		case corev1.NodePIDPressure:
			out["pidPressure"] = isTrue
		}
	}
	return out, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkNodeStatus{}) }) }
