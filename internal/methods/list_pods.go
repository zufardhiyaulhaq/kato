package methods

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
)

type listPods struct{}

func (listPods) Name() string { return "list_pods" }
func (listPods) Description() string {
	return "All pods of a workload (Deployment/DaemonSet/StatefulSet), not-ready first"
}

func (listPods) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Workload namespace"},
		{Name: "kind", Required: true, Description: "Deployment | DaemonSet | StatefulSet"},
		{Name: "name", Required: true, Description: "Workload name"},
		{Name: "maxListItems", Description: `cap the pods list at this many items, worst-first (default "50"); "0" = unlimited`},
	}
}

func (listPods) OutputFields() []OutputField {
	return []OutputField{
		{Name: "count", Type: FieldInt, Description: "number of pods owned by the workload"},
		{Name: "notReadyCount", Type: FieldInt, Description: "pods whose Ready condition is not True"},
		{Name: "listTruncated", Type: FieldBool, Description: "true if more pods were owned than the pods list carries"},
	}
}

func (listPods) ListOutputs() []ListOutputField {
	return []ListOutputField{{
		Name:        "pods",
		Description: "owned pods, not-ready first then by restartCount",
		ItemFields: []OutputField{
			{Name: "namespace", Type: FieldString, Description: "pod namespace"},
			{Name: "name", Type: FieldString, Description: "pod name"},
			{Name: "ready", Type: FieldBool, Description: "Ready condition is True"},
			{Name: "restartCount", Type: FieldInt, Description: "max restartCount across containers"},
			{Name: "node", Type: FieldString, Description: `scheduled node, "" if unscheduled`},
		},
	}}
}

func (listPods) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	maxItems, err := parseMaxListItems(params)
	if err != nil {
		return nil, err
	}
	pods, err := ownedPods(ctx, deps.Kube, params["namespace"], params["kind"], params["name"])
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(pods))
	notReady := 0
	for i := range pods {
		p := &pods[i]
		ready := podReady(p)
		if !ready {
			notReady++
		}
		items = append(items, map[string]any{
			"namespace":    p.Namespace,
			"name":         p.Name,
			"ready":        ready,
			"restartCount": int64(maxRestart(p)),
			"node":         p.Spec.NodeName,
		})
	}
	// Not-ready first, then higher restartCount, then name — so a bounded forEach
	// examines the most interesting pods first.
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := items[i]["ready"].(bool), items[j]["ready"].(bool)
		if ri != rj {
			return !ri // not-ready (false) sorts first
		}
		ci, cj := items[i]["restartCount"].(int64), items[j]["restartCount"].(int64)
		if ci != cj {
			return ci > cj
		}
		return items[i]["name"].(string) < items[j]["name"].(string)
	})
	// Scalars reflect the true totals; the list is capped (worst-first) so a workload
	// with very many pods can't overflow the Run CR or the LLM evidence.
	total := len(items)
	capped, truncated := capItems(items, maxItems)

	return Outputs{
		"count":         int64(total),
		"notReadyCount": int64(notReady),
		"listTruncated": truncated,
		"pods":          capped,
	}, nil
}

func podReady(p *corev1.Pod) bool {
	for _, cond := range p.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(listPods{}) })
}
