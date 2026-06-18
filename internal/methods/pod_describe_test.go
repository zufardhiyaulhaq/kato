package methods

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
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

func TestDescribePodTroubleshootingFields(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-1", Namespace: "payments",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "app-rs-abc"}},
		},
		Spec: corev1.PodSpec{
			NodeName:          "node-3",
			PriorityClassName: "high",
			HostNetwork:       true,
			NodeSelector:      map[string]string{"disktype": "ssd"},
			Tolerations:       []corev1.Toleration{{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}},
			Containers: []corev1.Container{{
				Name: "app", Image: "app:v1",
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
					TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(8080)}}},
			}},
		},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"},
		}},
	}
	client := fake.NewSimpleClientset(pod)
	m, _ := Builtin().Get("describe_pod")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"nodeName":          "node-3",
		"priorityClassName": "high",
		"hostNetwork":       true,
		"nodeSelector":      "disktype=ssd",
		"tolerations":       "dedicated=gpu:NoSchedule",
		"ownerReferences":   "ReplicaSet/app-rs-abc",
		"conditions":        "Ready=False (ContainersNotReady)",
		"probes":            "app: liveness=— readiness=tcp:8080 startup=—",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}

func TestDescribePodContainerList(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "payments"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app", Image: "app:v1"},
			{Name: "sidecar", Image: "proxy:v2"},
		}},
	}
	client := fake.NewSimpleClientset(pod)
	m, _ := Builtin().Get("describe_pod")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	cl, ok := out["containerList"].([]map[string]any)
	if !ok || len(cl) != 2 {
		t.Fatalf("containerList = %#v", out["containerList"])
	}
	if cl[0]["name"] != "app" || cl[0]["image"] != "app:v1" {
		t.Errorf("containerList[0] = %#v", cl[0])
	}
	if cl[1]["name"] != "sidecar" || cl[1]["image"] != "proxy:v2" {
		t.Errorf("containerList[1] = %#v", cl[1])
	}
}
