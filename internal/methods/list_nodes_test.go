package methods

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// nodeWith builds a Node with a Ready condition and optional mutations.
func nodeWith(name string, ready bool, opts ...func(*corev1.Node)) *corev1.Node {
	cond, reason := corev1.ConditionTrue, ""
	if !ready {
		cond, reason = corev1.ConditionFalse, "KubeletNotReady"
	}
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: cond, Reason: reason},
		}},
	}
	for _, o := range opts {
		o(n)
	}
	return n
}

func withMemoryPressure(n *corev1.Node) {
	n.Status.Conditions = append(n.Status.Conditions,
		corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue})
}

func withCordon(n *corev1.Node) { n.Spec.Unschedulable = true }

func withLabels(l map[string]string) func(*corev1.Node) {
	return func(n *corev1.Node) { n.Labels = l }
}

func runListNodes(t *testing.T, params map[string]string, objs ...*corev1.Node) Outputs {
	t.Helper()
	client := fake.NewSimpleClientset()
	for _, o := range objs {
		if _, err := client.CoreV1().Nodes().Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	m, ok := Builtin().Get("list_nodes")
	if !ok {
		t.Fatal("list_nodes not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client}, params)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out
}

func nodeItems(t *testing.T, out Outputs) []map[string]any {
	t.Helper()
	items, ok := out["nodes"].([]map[string]any)
	if !ok {
		t.Fatalf("nodes list output missing/wrong type: %#v", out["nodes"])
	}
	return items
}

func TestListNodesBucketsByStatus(t *testing.T) {
	out := runListNodes(t, map[string]string{},
		nodeWith("ok-1", true),
		nodeWith("ok-2", true),
		nodeWith("down-1", false),
		nodeWith("mem-1", true, withMemoryPressure),
		nodeWith("cordon-1", true, withCordon),
	)
	want := Outputs{
		"total": int64(5), "ready": int64(4), "notReady": int64(1),
		"memoryPressure": int64(1), "diskPressure": int64(0), "pidPressure": int64(0),
		"unschedulable": int64(1), "anyUnhealthy": true,
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %#v, want %#v", k, out[k], v)
		}
	}
}

func TestListNodesListsOnlyUnhealthyByDefault(t *testing.T) {
	out := runListNodes(t, map[string]string{},
		nodeWith("ok-1", true),
		nodeWith("down-1", false),
		nodeWith("mem-1", true, withMemoryPressure),
	)
	items := nodeItems(t, out)
	if len(items) != 2 {
		t.Fatalf("listed %d nodes, want 2 (unhealthy only): %#v", len(items), items)
	}
	names := map[string]map[string]any{}
	for _, it := range items {
		names[it["name"].(string)] = it
	}
	if _, ok := names["ok-1"]; ok {
		t.Error("healthy node ok-1 should not be listed by default")
	}
	if names["down-1"]["ready"] != false || names["down-1"]["reason"] != "KubeletNotReady" {
		t.Errorf("down-1 item = %#v", names["down-1"])
	}
	if names["mem-1"]["ready"] != true {
		t.Errorf("mem-1 ready = %#v, want true", names["mem-1"]["ready"])
	}
}

func TestListNodesIncludeHealthy(t *testing.T) {
	out := runListNodes(t, map[string]string{"includeHealthy": "true"},
		nodeWith("ok-1", true),
		nodeWith("down-1", false),
	)
	if got := len(nodeItems(t, out)); got != 2 {
		t.Fatalf("listed %d nodes, want 2 (all)", got)
	}
}

func TestListNodesLabelSelector(t *testing.T) {
	out := runListNodes(t, map[string]string{"labelSelector": "pool=gpu", "includeHealthy": "true"},
		nodeWith("gpu-1", true, withLabels(map[string]string{"pool": "gpu"})),
		nodeWith("cpu-1", true, withLabels(map[string]string{"pool": "cpu"})),
	)
	if out["total"] != int64(1) {
		t.Fatalf("total = %#v, want 1 (selector pool=gpu)", out["total"])
	}
	items := nodeItems(t, out)
	if len(items) != 1 || items[0]["name"] != "gpu-1" {
		t.Errorf("items = %#v, want only gpu-1", items)
	}
}

func TestListNodesMaxItemsTruncatesWorstFirst(t *testing.T) {
	out := runListNodes(t, map[string]string{"maxListItems": "1"},
		nodeWith("mem-1", true, withMemoryPressure),
		nodeWith("down-1", false),
	)
	if out["listTruncated"] != true {
		t.Errorf("listTruncated = %#v, want true", out["listTruncated"])
	}
	items := nodeItems(t, out)
	if len(items) != 1 {
		t.Fatalf("listed %d, want 1 (capped)", len(items))
	}
	// NotReady sorts before pressured.
	if items[0]["name"] != "down-1" {
		t.Errorf("worst-first item = %#v, want down-1 (NotReady before pressured)", items[0]["name"])
	}
	// Scalars still reflect the true totals, not the cap.
	if out["notReady"] != int64(1) || out["memoryPressure"] != int64(1) {
		t.Errorf("counts not reflecting full fleet: %#v", out)
	}
}

func TestListNodesParamErrors(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("list_nodes")
	for _, p := range []map[string]string{
		{"maxListItems": "-1"},
		{"maxListItems": "abc"},
		{"includeHealthy": "notabool"},
		{"labelSelector": "!!!bad"},
	} {
		if _, err := m.Run(context.Background(), Deps{Kube: client}, p); err == nil {
			t.Errorf("params %v: expected error, got nil", p)
		}
	}
}
