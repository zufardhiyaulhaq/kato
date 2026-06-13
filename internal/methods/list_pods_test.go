package methods

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestListPodsUnsupportedKind(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("list_pods")
	if _, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "x", "kind": "ReplicaSet", "name": "y"}); err == nil {
		t.Fatal("expected error for unsupported kind")
	}
}
