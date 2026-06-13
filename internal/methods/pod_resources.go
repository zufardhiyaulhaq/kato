package methods

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkPodResources struct{}

func (checkPodResources) Name() string { return "check_pod_resources" }
func (checkPodResources) Description() string {
	return "Configured CPU/memory requests and limits from the pod spec (summed across containers)"
}

// Note on summing: requests sum over ALL containers, but limits sum only over the
// containers that set them. So summed request can exceed summed limit when limits
// are incomplete — the *LimitComplete flags report whether every container sets
// that limit.

func (checkPodResources) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Pod namespace"},
		{Name: "name", Required: true, Description: "Pod name"},
	}
}

func (checkPodResources) OutputFields() []OutputField {
	return []OutputField{
		{Name: "cpuRequest", Type: FieldString, Description: `summed CPU request, e.g. "250m"; "0" if none set`},
		{Name: "cpuLimit", Type: FieldString, Description: `summed CPU limit; "0" if none set`},
		{Name: "memoryRequest", Type: FieldString, Description: `summed memory request, e.g. "256Mi"; "0" if none set`},
		{Name: "memoryLimit", Type: FieldString, Description: `summed memory limit; "0" if none set`},
		{Name: "noLimitsSet", Type: FieldBool, Description: "true if no container sets any CPU or memory limit"},
		{Name: "cpuLimitComplete", Type: FieldBool, Description: "true only if EVERY container sets a CPU limit; when false, cpuLimit is partial and may be below cpuRequest"},
		{Name: "memoryLimitComplete", Type: FieldBool, Description: "true only if EVERY container sets a memory limit; when false, memoryLimit is partial and may be below memoryRequest"},
	}
}

func (checkPodResources) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	pod, err := deps.Kube.CoreV1().Pods(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", params["namespace"], params["name"], err)
	}
	var cpuReq, cpuLim, memReq, memLim resource.Quantity
	withCPULimit, withMemLimit := 0, 0
	for _, c := range pod.Spec.Containers {
		if q, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
			cpuReq.Add(q)
		}
		if q, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
			memReq.Add(q)
		}
		if q, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
			cpuLim.Add(q)
			withCPULimit++
		}
		if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			memLim.Add(q)
			withMemLimit++
		}
	}
	n := len(pod.Spec.Containers)
	return Outputs{
		"cpuRequest":          cpuReq.String(),
		"cpuLimit":            cpuLim.String(),
		"memoryRequest":       memReq.String(),
		"memoryLimit":         memLim.String(),
		"noLimitsSet":         withCPULimit == 0 && withMemLimit == 0,
		"cpuLimitComplete":    withCPULimit == n,
		"memoryLimitComplete": withMemLimit == n,
	}, nil
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkPodResources{}) })
}
