package methods

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func runningPod(name, ns, node string, owner metav1.OwnerReference, ready bool, restarts int32) *corev1.Pod {
	cond := corev1.ConditionTrue
	if !ready {
		cond = corev1.ConditionFalse
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, OwnerReferences: []metav1.OwnerReference{owner}},
		Spec:       corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: cond}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "terway", RestartCount: restarts, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
}

func TestListPodsDaemonSet(t *testing.T) {
	client := fake.NewSimpleClientset(
		runningPod("terway-a", "kube-system", "node-1", dsOwner("terway-eniip"), true, 0),
		runningPod("terway-b", "kube-system", "node-2", dsOwner("terway-eniip"), false, 3),
		runningPod("terway-c", "kube-system", "node-3", dsOwner("terway-eniip"), true, 1),
		runningPod("other", "kube-system", "node-4", dsOwner("different"), true, 0), // wrong owner, excluded
	)
	m, ok := Builtin().Get("list_pods")
	if !ok {
		t.Fatal("list_pods not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "kube-system", "kind": "DaemonSet", "name": "terway-eniip"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != int64(3) {
		t.Errorf("count = %v, want 3", out["count"])
	}
	if out["notReadyCount"] != int64(1) {
		t.Errorf("notReadyCount = %v, want 1", out["notReadyCount"])
	}
	pods, _ := out["pods"].([]map[string]any)
	if len(pods) != 3 {
		t.Fatalf("pods len = %d, want 3", len(pods))
	}
	// Not-ready pod sorts first, carries node + restartCount.
	if pods[0]["name"] != "terway-b" || pods[0]["ready"] != false {
		t.Errorf("first pod = %+v, want terway-b (not ready) first", pods[0])
	}
	if pods[0]["node"] != "node-2" || pods[0]["restartCount"] != int64(3) {
		t.Errorf("first pod fields = %+v", pods[0])
	}
}

func TestListPodsCapsList(t *testing.T) {
	objs := make([]runtime.Object, 0, 60)
	for i := 0; i < 60; i++ {
		// All not-ready with distinct restartCounts -> deterministic worst-first order.
		objs = append(objs, runningPod(fmt.Sprintf("p-%02d", i), "default", "n", dsOwner("big"), false, int32(i)))
	}
	client := fake.NewSimpleClientset(objs...)
	m, _ := Builtin().Get("list_pods")

	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "kind": "DaemonSet", "name": "big"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != int64(60) {
		t.Errorf("count = %v, want 60 (true total)", out["count"])
	}
	if out["notReadyCount"] != int64(60) {
		t.Errorf("notReadyCount = %v, want 60 (true total)", out["notReadyCount"])
	}
	if out["listTruncated"] != true {
		t.Errorf("listTruncated = %v, want true", out["listTruncated"])
	}
	pods := out["pods"].([]map[string]any)
	if len(pods) != 50 {
		t.Fatalf("pods len = %d, want 50 (capped)", len(pods))
	}
	// Worst-first survives the cap: highest restartCount (59) first.
	if pods[0]["restartCount"] != int64(59) {
		t.Errorf("first kept pod restartCount = %v, want 59", pods[0]["restartCount"])
	}
}

func TestListPodsDeclaresListTruncated(t *testing.T) {
	m, _ := Builtin().Get("list_pods")
	found := false
	for _, f := range m.OutputFields() {
		if f.Name == "listTruncated" {
			if f.Type != FieldBool {
				t.Errorf("listTruncated type = %v, want bool", f.Type)
			}
			found = true
		}
	}
	if !found {
		t.Error("listTruncated not declared in OutputFields")
	}
}

func TestListPodsUnderCapNotTruncated(t *testing.T) {
	client := fake.NewSimpleClientset(
		runningPod("a", "default", "n1", dsOwner("ds"), true, 0),
		runningPod("b", "default", "n2", dsOwner("ds"), false, 1),
	)
	m, _ := Builtin().Get("list_pods")
	out, _ := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "kind": "DaemonSet", "name": "ds"})
	if out["listTruncated"] != false {
		t.Errorf("listTruncated = %v, want false", out["listTruncated"])
	}
	if len(out["pods"].([]map[string]any)) != 2 {
		t.Errorf("pods len = %d, want 2", len(out["pods"].([]map[string]any)))
	}
}

func TestListPodsMaxListItemsParam(t *testing.T) {
	objs := make([]runtime.Object, 0, 60)
	for i := 0; i < 60; i++ {
		objs = append(objs, runningPod(fmt.Sprintf("p-%02d", i), "default", "n", dsOwner("big"), false, int32(i)))
	}
	client := fake.NewSimpleClientset(objs...)
	m, _ := Builtin().Get("list_pods")

	out, err := m.Run(context.Background(), Deps{Kube: client}, map[string]string{
		"namespace": "default", "kind": "DaemonSet", "name": "big", "maxListItems": "7"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != int64(60) || out["listTruncated"] != true {
		t.Errorf("count=%v listTruncated=%v, want 60,true", out["count"], out["listTruncated"])
	}
	if len(out["pods"].([]map[string]any)) != 7 {
		t.Errorf("pods len = %d, want 7", len(out["pods"].([]map[string]any)))
	}

	// "0" disables the cap: all 60 returned, not truncated.
	out, err = m.Run(context.Background(), Deps{Kube: client}, map[string]string{
		"namespace": "default", "kind": "DaemonSet", "name": "big", "maxListItems": "0"})
	if err != nil {
		t.Fatalf("Run (unlimited): %v", err)
	}
	if out["listTruncated"] != false || len(out["pods"].([]map[string]any)) != 60 {
		t.Errorf("unlimited: listTruncated=%v len=%d, want false,60", out["listTruncated"], len(out["pods"].([]map[string]any)))
	}

	if _, err := m.Run(context.Background(), Deps{Kube: client}, map[string]string{
		"namespace": "default", "kind": "DaemonSet", "name": "big", "maxListItems": "-3"}); err == nil {
		t.Error("expected error for negative maxListItems")
	}
}

func TestListPodsUnsupportedKind(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("list_pods")
	if _, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "x", "kind": "ReplicaSet", "name": "y"}); err == nil {
		t.Fatal("expected error for unsupported kind")
	}
}
