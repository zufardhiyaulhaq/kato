package methods

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckPodResourcesPerContainer(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "payments"},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{
				Name: "setup",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")},
				},
			}},
			Containers: []corev1.Container{
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
					Name: "sidecar", // request only, no limits
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("150m")},
					},
				},
			},
		},
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
	got, _ := out["containers"].(string)
	for _, want := range []string{
		"setup (init): req cpu=10m mem=-; lim cpu=- mem=-",
		"app: req cpu=100m mem=128Mi; lim cpu=500m mem=256Mi",
		"sidecar: req cpu=150m mem=-; lim cpu=- mem=-",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("containers missing line %q\n--- got ---\n%s", want, got)
		}
	}
	// init container comes before regular containers.
	if strings.Index(got, "setup (init)") > strings.Index(got, "app:") {
		t.Errorf("init container should be listed first:\n%s", got)
	}
	if out["noLimitsSet"] != false { // app sets limits
		t.Errorf("noLimitsSet = %v, want false", out["noLimitsSet"])
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
	if got, _ := out["containers"].(string); got != "app: req cpu=- mem=-; lim cpu=- mem=-" {
		t.Errorf("containers = %q", got)
	}
}
