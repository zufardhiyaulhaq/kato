package methods

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

type checkPodUsage struct{}

func (checkPodUsage) Name() string { return "check_pod_usage" }
func (checkPodUsage) Description() string {
	return "Live per-container CPU/memory usage from metrics-server (metrics.k8s.io)"
}

func (checkPodUsage) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Pod namespace"},
		{Name: "name", Required: true, Description: "Pod name"},
	}
}

func (checkPodUsage) OutputFields() []OutputField {
	return []OutputField{
		{Name: "containers", Type: FieldString, Description: `per-container live usage, one line each: "<name>: cpu=<m>m mem=<n>Mi"; empty if unavailable`},
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
	return usageOutputs(pm.Containers), nil
}

func unavailableUsage() Outputs {
	return Outputs{"containers": "", "metricsAvailable": false}
}

// usageOutputs renders each container's usage on its own line (CPU in millicores,
// memory in MiB), sorted by container name for stable output.
func usageOutputs(containers []metricsv1beta1.ContainerMetrics) Outputs {
	sorted := make([]metricsv1beta1.ContainerMetrics, len(containers))
	copy(sorted, containers)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var b strings.Builder
	for _, c := range sorted {
		cpu := c.Usage[corev1.ResourceCPU]
		mem := c.Usage[corev1.ResourceMemory]
		fmt.Fprintf(&b, "%s: cpu=%dm mem=%dMi\n", c.Name, cpu.MilliValue(), mem.Value()/(1024*1024))
	}
	return Outputs{
		"containers":       strings.TrimRight(b.String(), "\n"),
		"metricsAvailable": true,
	}
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkPodUsage{}) })
}
