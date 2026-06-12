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

func TestDescribePodSanitizes(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-1", Namespace: "payments",
			ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl"}},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app", Image: "app:v1",
			Env: []corev1.EnvVar{{Name: "DB_PASSWORD", Value: "hunter2"}},
		}}},
	}
	client := fake.NewSimpleClientset(pod)
	m, ok := Builtin().Get("describe_pod")
	if !ok {
		t.Fatal("describe_pod not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	manifest := out["manifest"].(string)
	if strings.Contains(manifest, "hunter2") {
		t.Error("env var value leaked into manifest")
	}
	if !strings.Contains(manifest, "DB_PASSWORD") || !strings.Contains(manifest, "[REDACTED]") {
		t.Error("env var name/redaction marker missing")
	}
	if strings.Contains(manifest, "managedFields") || strings.Contains(manifest, "kubectl") {
		t.Error("managedFields not stripped")
	}
	if !strings.Contains(manifest, "app:v1") {
		t.Error("expected spec content missing")
	}
}

func TestDescribePodStructuredOutputs(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "payments"},
		Spec: corev1.PodSpec{
			RestartPolicy:      corev1.RestartPolicyAlways,
			ServiceAccountName: "payments-sa",
			Volumes:            []corev1.Volume{{Name: "config"}, {Name: "data"}},
			Containers: []corev1.Container{
				{
					Name: "app", Image: "app:v1",
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
				{Name: "sidecar", Image: "proxy:v2"}, // no resources set
			},
		},
	}
	client := fake.NewSimpleClientset(pod)
	m, _ := Builtin().Get("describe_pod")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]string{
		"containers":       "app, sidecar",
		"images":           "app:v1, proxy:v2",
		"resourceRequests": "app: cpu=100m mem=128Mi",
		"resourceLimits":   "app: cpu=500m mem=256Mi",
		"restartPolicy":    "Always",
		"serviceAccount":   "payments-sa",
		"volumes":          "config, data",
	}
	for field, want := range checks {
		if got := out[field]; got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}
	// sidecar has no resources, so it must not appear in the resource strings.
	if strings.Contains(out["resourceRequests"].(string), "sidecar") {
		t.Errorf("sidecar with no requests leaked: %q", out["resourceRequests"])
	}
}
