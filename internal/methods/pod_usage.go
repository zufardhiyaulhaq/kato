package methods

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

type checkPodUsage struct{}

func (checkPodUsage) Name() string { return "check_pod_usage" }
func (checkPodUsage) Description() string {
	return "Live pod CPU/memory usage from metrics-server (metrics.k8s.io)"
}

func (checkPodUsage) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Pod namespace"},
		{Name: "name", Required: true, Description: "Pod name"},
	}
}

func (checkPodUsage) OutputFields() []OutputField {
	return []OutputField{
		{Name: "cpuMillicores", Type: FieldInt, Description: "current CPU usage in millicores, 0 if unavailable"},
		{Name: "memoryBytes", Type: FieldInt, Description: "current memory usage in bytes, 0 if unavailable"},
		{Name: "memoryHuman", Type: FieldString, Description: `current memory usage, e.g. "142Mi"; "0" if unavailable`},
		{Name: "metricsAvailable", Type: FieldBool, Description: "false if metrics-server is absent or has no data for this pod yet"},
	}
}

func (checkPodUsage) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	if deps.Metrics == nil {
		return unavailableUsage(), nil
	}
	pm, err := deps.Metrics.MetricsV1beta1().PodMetricses(params["namespace"]).
		Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		// metrics-server absent, or no metrics scraped for this pod yet: report
		// unavailable rather than failing the step (live usage is a best-effort
		// signal; check_pod_resources covers the always-available spec values).
		return unavailableUsage(), nil
	}
	cpu, mem := sumPodUsage(pm.Containers)
	return usageOutputs(cpu, mem), nil
}

func unavailableUsage() Outputs {
	return Outputs{
		"cpuMillicores": int64(0), "memoryBytes": int64(0),
		"memoryHuman": "0", "metricsAvailable": false,
	}
}

// sumPodUsage totals CPU and memory usage across a pod's containers.
func sumPodUsage(containers []metricsv1beta1.ContainerMetrics) (cpu, mem resource.Quantity) {
	for _, c := range containers {
		cpu.Add(c.Usage[corev1.ResourceCPU])
		mem.Add(c.Usage[corev1.ResourceMemory])
	}
	return cpu, mem
}

func usageOutputs(cpu, mem resource.Quantity) Outputs {
	return Outputs{
		"cpuMillicores":    cpu.MilliValue(),
		"memoryBytes":      mem.Value(),
		"memoryHuman":      mem.String(),
		"metricsAvailable": true,
	}
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkPodUsage{}) })
}
