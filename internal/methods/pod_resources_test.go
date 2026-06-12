package methods

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckPodResourcesSumsContainers(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "payments"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{
				Name: "app",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			},
			{
				Name: "sidecar",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("150m")},
				},
			},
		}},
	}
	client := fake.NewSimpleClientset(pod)
	m, ok := Builtin().Get("check_pod_resources")
	if !ok {
		t.Fatal("check_pod_resources not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := Outputs{
		"cpuRequest": "250m", "cpuLimit": "500m",
		"memoryRequest": "128Mi", "memoryLimit": "256Mi",
		"noLimitsSet": false,
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %#v, want %#v", k, out[k], v)
		}
	}
}

func TestCheckPodResourcesNoLimits(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "best-effort", Namespace: "default"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	client := fake.NewSimpleClientset(pod)
	m, _ := Builtin().Get("check_pod_resources")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "name": "best-effort"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["noLimitsSet"] != true {
		t.Errorf("noLimitsSet = %v, want true", out["noLimitsSet"])
	}
	if out["cpuRequest"] != "0" || out["memoryLimit"] != "0" {
		t.Errorf("expected zero quantities, got cpuRequest=%v memoryLimit=%v", out["cpuRequest"], out["memoryLimit"])
	}
}
