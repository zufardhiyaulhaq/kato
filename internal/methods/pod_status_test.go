package methods

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func crashloopPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "payments"},
		Spec:       corev1.PodSpec{NodeName: "node-7"},
		Status: corev1.PodStatus{
			Phase:    corev1.PodRunning,
			QOSClass: corev1.PodQOSBurstable,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "app",
				Ready:        false,
				RestartCount: 17,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: "CrashLoopBackOff", Message: "back-off 5m0s",
				}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 137, Reason: "OOMKilled",
				}},
			}},
		},
	}
}

func TestCheckPodStatusCrashloop(t *testing.T) {
	client := fake.NewSimpleClientset(crashloopPod())
	m, ok := Builtin().Get("check_pod_status")
	if !ok {
		t.Fatal("check_pod_status not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := Outputs{
		"phase": "Running", "ready": false, "restartCount": int64(17),
		"nodeName": "node-7", "waitingReason": "CrashLoopBackOff",
		"waitingMessage": "back-off 5m0s", "lastTerminationReason": "OOMKilled",
		"lastTerminationExitCode": int64(137), "qosClass": "Burstable",
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %#v, want %#v", k, out[k], v)
		}
	}
}

func TestCheckPodStatusHealthyDefaults(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app", Ready: true,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
	client := fake.NewSimpleClientset(pod)
	m, _ := Builtin().Get("check_pod_status")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "name": "ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Guaranteed-present defaults (spec §7): never missing, never nil.
	if out["waitingReason"] != "" || out["lastTerminationReason"] != "" {
		t.Errorf("expected empty-string defaults, got %v", out)
	}
	if out["lastTerminationExitCode"] != int64(-1) {
		t.Errorf("lastTerminationExitCode = %v, want -1", out["lastTerminationExitCode"])
	}
	if out["restartCount"] != int64(0) || out["ready"] != true {
		t.Errorf("unexpected: %v", out)
	}
}

func TestCheckPodStatusContainersList(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "payments"},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: false, RestartCount: 17},
				{Name: "istio-proxy", Ready: true, RestartCount: 0},
			},
		},
	}
	client := fake.NewSimpleClientset(pod)
	m, _ := Builtin().Get("check_pod_status")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	cs, ok := out["containers"].([]map[string]any)
	if !ok || len(cs) != 2 {
		t.Fatalf("containers = %#v", out["containers"])
	}
	if cs[0]["name"] != "app" || cs[0]["restartCount"] != int64(17) || cs[0]["ready"] != false {
		t.Errorf("container[0] = %#v", cs[0])
	}
	if cs[1]["name"] != "istio-proxy" || cs[1]["ready"] != true {
		t.Errorf("container[1] = %#v", cs[1])
	}
}

func TestCheckPodStatusNotFound(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_pod_status")
	if _, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "name": "ghost"}); err == nil {
		t.Fatal("expected error for missing pod")
	}
}
