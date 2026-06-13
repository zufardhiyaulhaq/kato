package methods

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkPodResources struct{}

func (checkPodResources) Name() string { return "check_pod_resources" }
func (checkPodResources) Description() string {
	return "Configured CPU/memory requests and limits, per container (init containers marked)"
}

func (checkPodResources) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Pod namespace"},
		{Name: "name", Required: true, Description: "Pod name"},
	}
}

func (checkPodResources) OutputFields() []OutputField {
	return []OutputField{
		{Name: "containers", Type: FieldString, Description: `per-container requests and limits, one line each; an unset value shown as "-"`},
		{Name: "noLimitsSet", Type: FieldBool, Description: "true if no (non-init) container sets any CPU or memory limit"},
	}
}

// qtyOr renders a resource quantity from the list, or "-" when it is not set.
func qtyOr(rl corev1.ResourceList, name corev1.ResourceName) string {
	if q, ok := rl[name]; ok {
		return q.String()
	}
	return "-"
}

func (checkPodResources) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	pod, err := deps.Kube.CoreV1().Pods(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", params["namespace"], params["name"], err)
	}

	var b strings.Builder
	anyRegularLimit := false
	render := func(c *corev1.Container, init bool) {
		_, hasCPULim := c.Resources.Limits[corev1.ResourceCPU]
		_, hasMemLim := c.Resources.Limits[corev1.ResourceMemory]
		if !init && (hasCPULim || hasMemLim) {
			anyRegularLimit = true
		}
		suffix := ""
		if init {
			suffix = " (init)"
		}
		fmt.Fprintf(&b, "%s%s: req cpu=%s mem=%s; lim cpu=%s mem=%s\n",
			c.Name, suffix,
			qtyOr(c.Resources.Requests, corev1.ResourceCPU), qtyOr(c.Resources.Requests, corev1.ResourceMemory),
			qtyOr(c.Resources.Limits, corev1.ResourceCPU), qtyOr(c.Resources.Limits, corev1.ResourceMemory))
	}
	for i := range pod.Spec.InitContainers {
		render(&pod.Spec.InitContainers[i], true)
	}
	for i := range pod.Spec.Containers {
		render(&pod.Spec.Containers[i], false)
	}

	return Outputs{
		"containers":  strings.TrimRight(b.String(), "\n"),
		"noLimitsSet": !anyRegularLimit,
	}, nil
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkPodResources{}) })
}
