package methods

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// nodePod builds a pod scheduled on a node with a Ready condition + restart count.
func nodePod(ns, name, node string, ready bool, restarts int32) *corev1.Pod {
	cond := corev1.ConditionTrue
	if !ready {
		cond = corev1.ConditionFalse
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: cond}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "c", RestartCount: restarts, Ready: ready}},
		},
	}
}

func runListNodePods(t *testing.T, params map[string]string, objs ...*corev1.Pod) Outputs {
	t.Helper()
	client := fake.NewSimpleClientset()
	for _, o := range objs {
		if _, err := client.CoreV1().Pods(o.Namespace).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	m, ok := Builtin().Get("list_node_pods")
	if !ok {
		t.Fatal("list_node_pods not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client}, params)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out
}

func nodePodItems(t *testing.T, out Outputs) []map[string]any {
	t.Helper()
	items, ok := out["pods"].([]map[string]any)
	if !ok {
		t.Fatalf("pods list output missing/wrong type: %#v", out["pods"])
	}
	return items
}

func TestListNodePodsFiltersByNode(t *testing.T) {
	out := runListNodePods(t, map[string]string{"node": "node-1"},
		nodePod("kube-system", "coredns-a", "node-1", true, 0),
		nodePod("kube-system", "coredns-b", "node-2", true, 0),
	)
	if out["count"] != int64(1) {
		t.Fatalf("count = %#v, want 1 (only node-1)", out["count"])
	}
	items := nodePodItems(t, out)
	if len(items) != 1 || items[0]["name"] != "coredns-a" {
		t.Errorf("items = %#v, want only coredns-a", items)
	}
}

func TestListNodePodsNamePatternRegex(t *testing.T) {
	out := runListNodePods(t, map[string]string{"node": "node-1", "namePattern": "coredns|terway"},
		nodePod("kube-system", "coredns-7d8f", "node-1", true, 0),
		nodePod("kube-system", "terway-eniip-x", "node-1", true, 0),
		nodePod("default", "my-app-123", "node-1", true, 0),
	)
	if out["count"] != int64(2) {
		t.Fatalf("count = %#v, want 2 (coredns|terway)", out["count"])
	}
	for _, it := range nodePodItems(t, out) {
		if it["name"] == "my-app-123" {
			t.Errorf("my-app should not match coredns|terway")
		}
	}
}

func TestListNodePodsNamespaceFilter(t *testing.T) {
	out := runListNodePods(t, map[string]string{"node": "node-1", "namespace": "kube-system"},
		nodePod("kube-system", "coredns-a", "node-1", true, 0),
		nodePod("default", "app-a", "node-1", true, 0),
	)
	if out["count"] != int64(1) {
		t.Fatalf("count = %#v, want 1 (namespace kube-system)", out["count"])
	}
}

func TestListNodePodsNotReadyFirstAndCounts(t *testing.T) {
	out := runListNodePods(t, map[string]string{"node": "node-1"},
		nodePod("kube-system", "ready-1", "node-1", true, 0),
		nodePod("kube-system", "broken-1", "node-1", false, 7),
	)
	if out["count"] != int64(2) || out["notReadyCount"] != int64(1) {
		t.Errorf("counts: count=%#v notReadyCount=%#v", out["count"], out["notReadyCount"])
	}
	items := nodePodItems(t, out)
	if items[0]["name"] != "broken-1" {
		t.Errorf("worst-first: items[0] = %#v, want broken-1 (not-ready first)", items[0]["name"])
	}
	if items[0]["restartCount"] != int64(7) || items[0]["ready"] != false {
		t.Errorf("broken-1 item = %#v", items[0])
	}
}

func TestListNodePodsMaxItemsTruncates(t *testing.T) {
	out := runListNodePods(t, map[string]string{"node": "node-1", "maxListItems": "1"},
		nodePod("kube-system", "a", "node-1", true, 0),
		nodePod("kube-system", "b", "node-1", true, 0),
	)
	if out["listTruncated"] != true {
		t.Errorf("listTruncated = %#v, want true", out["listTruncated"])
	}
	if got := len(nodePodItems(t, out)); got != 1 {
		t.Errorf("listed %d, want 1 (capped)", got)
	}
	if out["count"] != int64(2) {
		t.Errorf("count = %#v, want 2 (full match, not capped)", out["count"])
	}
}

func TestListNodePodsParamErrors(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("list_node_pods")
	for _, p := range []map[string]string{
		{},                                       // missing node
		{"node": "node-1", "namePattern": "("},   // bad regex
		{"node": "node-1", "maxListItems": "-1"}, // bad cap
	} {
		if _, err := m.Run(context.Background(), Deps{Kube: client}, p); err == nil {
			t.Errorf("params %v: expected error, got nil", p)
		}
	}
}
