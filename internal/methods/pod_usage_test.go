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
// usage arithmetic and output assembly are tested through their pure helpers;
// the method's nil/unavailable path is tested through the Method interface.
func TestSumPodUsageAndOutputs(t *testing.T) {
	containers := []metricsv1beta1.ContainerMetrics{
		{Name: "app", Usage: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("100Mi"),
		}},
		{Name: "sidecar", Usage: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("42m"),
			corev1.ResourceMemory: resource.MustParse("42Mi"),
		}},
	}
	cpu, mem := sumPodUsage(containers)
	out := usageOutputs(cpu, mem)

	if out["metricsAvailable"] != true {
		t.Errorf("metricsAvailable = %v, want true", out["metricsAvailable"])
	}
	if out["cpuMillicores"] != int64(142) { // 100m + 42m
		t.Errorf("cpuMillicores = %v, want 142", out["cpuMillicores"])
	}
	if out["memoryBytes"] != int64(142*1024*1024) { // 100Mi + 42Mi
		t.Errorf("memoryBytes = %v, want %d", out["memoryBytes"], 142*1024*1024)
	}
	if out["memoryHuman"] != "142Mi" {
		t.Errorf("memoryHuman = %v, want 142Mi", out["memoryHuman"])
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
	if out["metricsAvailable"] != false || out["cpuMillicores"] != int64(0) {
		t.Errorf("expected unavailable zeros, got %v", out)
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
