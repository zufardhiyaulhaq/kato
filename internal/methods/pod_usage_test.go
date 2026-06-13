package methods

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

// The metrics fake clientset cannot return seeded PodMetrics via Get, so the
// per-container output assembly is tested through its pure helper; the method's
// nil/unavailable path is tested through the Method interface.
func TestUsageOutputsPerContainer(t *testing.T) {
	containers := []metricsv1beta1.ContainerMetrics{
		{Name: "sidecar", Usage: corev1.ResourceList{ // out of order on purpose
			corev1.ResourceCPU:    resource.MustParse("42m"),
			corev1.ResourceMemory: resource.MustParse("42Mi"),
		}},
		{Name: "app", Usage: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("100Mi"),
		}},
	}
	out := usageOutputs(containers)

	if out["metricsAvailable"] != true {
		t.Errorf("metricsAvailable = %v, want true", out["metricsAvailable"])
	}
	// Per container, sorted by name — NOT summed.
	want := "app: cpu=100m mem=100Mi\nsidecar: cpu=42m mem=42Mi"
	if got, _ := out["containers"].(string); got != want {
		t.Errorf("containers =\n%q\nwant\n%q", got, want)
	}
}

func TestCheckPodUsageNoMetricsServer(t *testing.T) {
	m, ok := Builtin().Get("check_pod_usage")
	if !ok {
		t.Fatal("check_pod_usage not registered")
	}

	// Nil Metrics client -> unavailable, not an error.
	out, err := m.Run(context.Background(), Deps{},
		map[string]string{"namespace": "x", "name": "y"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["metricsAvailable"] != false || out["containers"] != "" {
		t.Errorf("expected unavailable/empty, got %v", out)
	}

	// Metrics client present but no data for this pod -> unavailable, not error.
	out, err = m.Run(context.Background(), Deps{Metrics: metricsfake.NewSimpleClientset()},
		map[string]string{"namespace": "x", "name": "missing"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["metricsAvailable"] != false {
		t.Errorf("metricsAvailable = %v, want false", out["metricsAvailable"])
	}
}
