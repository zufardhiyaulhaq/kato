package methods

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func pressuredNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-7"},
		Spec: corev1.NodeSpec{
			Unschedulable: true,
			Taints:        []corev1.Taint{{Key: "node.kubernetes.io/memory-pressure", Effect: corev1.TaintEffectNoSchedule}},
		},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady"},
			{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
		}},
	}
}

func TestCheckNodeStatus(t *testing.T) {
	client := fake.NewSimpleClientset(pressuredNode())
	m, ok := Builtin().Get("check_node_status")
	if !ok {
		t.Fatal("check_node_status not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client}, map[string]string{"name": "node-7"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := Outputs{
		"ready": false, "readyReason": "KubeletNotReady",
		"memoryPressure": true, "diskPressure": false, "pidPressure": false,
		"unschedulable": true,
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %#v, want %#v", k, out[k], v)
		}
	}
}

func TestDescribeNode(t *testing.T) {
	client := fake.NewSimpleClientset(pressuredNode())
	m, ok := Builtin().Get("describe_node")
	if !ok {
		t.Fatal("describe_node not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client}, map[string]string{"name": "node-7"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out["taints"].(string), "memory-pressure") {
		t.Errorf("taints = %q", out["taints"])
	}
	if _, ok := out["manifest"].(string); !ok {
		t.Error("manifest output missing")
	}
}
