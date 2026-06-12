package methods

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type listFailingPods struct{}

func (listFailingPods) Name() string { return "list_failing_pods" }
func (listFailingPods) Description() string {
	return "Failing pods of a workload (Deployment/DaemonSet/StatefulSet), worst-first"
}

func (listFailingPods) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Workload namespace"},
		{Name: "kind", Required: true, Description: "Deployment | DaemonSet | StatefulSet"},
		{Name: "name", Required: true, Description: "Workload name"},
		{Name: "minRestarts", Description: "only include pods with restartCount >= this (default 0)"},
		{Name: "includeCrashLoop", Description: `count CrashLoopBackOff pods (default "true")`},
		{Name: "includeImagePull", Description: `count ImagePullBackOff/ErrImagePull/CreateContainerError pods (default "true")`},
		{Name: "includeOOM", Description: `count OOMKilled / non-zero last-exit pods (default "true")`},
		{Name: "includeNotReady", Description: `count any not-Ready pod (default "false")`},
	}
}

func (listFailingPods) OutputFields() []OutputField {
	return []OutputField{
		{Name: "count", Type: FieldInt, Description: "number of failing pods matched"},
		{Name: "anyFailing", Type: FieldBool, Description: "count > 0"},
	}
}

func (listFailingPods) ListOutputs() []ListOutputField {
	return []ListOutputField{{
		Name:        "pods",
		Description: "failing pods, sorted worst-first by restartCount",
		ItemFields: []OutputField{
			{Name: "namespace", Type: FieldString, Description: "pod namespace"},
			{Name: "name", Type: FieldString, Description: "pod name"},
			{Name: "reason", Type: FieldString, Description: "dominant failure reason"},
			{Name: "restartCount", Type: FieldInt, Description: "max restartCount across containers"},
		},
	}}
}

func (listFailingPods) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	crit, err := parseFailCriteria(params)
	if err != nil {
		return nil, err
	}
	pods, err := ownedPods(ctx, deps.Kube, params["namespace"], params["kind"], params["name"])
	if err != nil {
		return nil, err
	}

	items := []map[string]any{}
	for i := range pods {
		p := &pods[i]
		reason, restarts, failing := classifyPod(p, crit)
		if !failing || int(restarts) < crit.minRestarts {
			continue
		}
		items = append(items, map[string]any{
			"namespace": p.Namespace, "name": p.Name,
			"reason": reason, "restartCount": int64(restarts),
		})
	}
	// Worst-first by restartCount, then name for stability.
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := items[i]["restartCount"].(int64), items[j]["restartCount"].(int64)
		if ri != rj {
			return ri > rj
		}
		return items[i]["name"].(string) < items[j]["name"].(string)
	})

	return Outputs{
		"count":      int64(len(items)),
		"anyFailing": len(items) > 0,
		"pods":       items,
	}, nil
}

type failCriteria struct {
	minRestarts                         int
	crashLoop, imagePull, oom, notReady bool
}

func parseBoolDefault(params map[string]string, key string, def bool) (bool, error) {
	v, ok := params[key]
	if !ok || v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("param %s: %w", key, err)
	}
	return b, nil
}

func parseFailCriteria(params map[string]string) (failCriteria, error) {
	c := failCriteria{}
	if v := params["minRestarts"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("param minRestarts: %w", err)
		}
		c.minRestarts = n
	}
	var err error
	if c.crashLoop, err = parseBoolDefault(params, "includeCrashLoop", true); err != nil {
		return c, err
	}
	if c.imagePull, err = parseBoolDefault(params, "includeImagePull", true); err != nil {
		return c, err
	}
	if c.oom, err = parseBoolDefault(params, "includeOOM", true); err != nil {
		return c, err
	}
	if c.notReady, err = parseBoolDefault(params, "includeNotReady", false); err != nil {
		return c, err
	}
	return c, nil
}

var imagePullReasons = map[string]bool{
	"ImagePullBackOff": true, "ErrImagePull": true, "CreateContainerError": true,
}

// classifyPod returns the dominant failure reason, the max restart count, and
// whether the pod is failing under the given criteria.
func classifyPod(p *corev1.Pod, c failCriteria) (reason string, restarts int32, failing bool) {
	ready := false
	for _, cond := range p.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.RestartCount > restarts {
			restarts = cs.RestartCount
		}
		if w := cs.State.Waiting; w != nil {
			if c.crashLoop && w.Reason == "CrashLoopBackOff" {
				return "CrashLoopBackOff", maxRestart(p), true
			}
			if c.imagePull && imagePullReasons[w.Reason] {
				return w.Reason, maxRestart(p), true
			}
		}
		if t := cs.LastTerminationState.Terminated; t != nil && c.oom {
			if t.Reason == "OOMKilled" || t.ExitCode != 0 {
				r := t.Reason
				if r == "" {
					r = "Error"
				}
				return r, maxRestart(p), true
			}
		}
	}
	if c.notReady && !ready {
		return "NotReady", restarts, true
	}
	return "", restarts, false
}

func maxRestart(p *corev1.Pod) int32 {
	var m int32
	for _, cs := range p.Status.ContainerStatuses {
		if cs.RestartCount > m {
			m = cs.RestartCount
		}
	}
	return m
}

// ownedPods returns the pods owned by the named workload in the namespace.
func ownedPods(ctx context.Context, client kubernetes.Interface, ns, kind, name string) ([]corev1.Pod, error) {
	all, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", ns, err)
	}
	switch kind {
	case "DaemonSet", "StatefulSet":
		return filterByOwner(all.Items, kind, name), nil
	case "Deployment":
		rsList, err := client.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list replicasets in %s: %w", ns, err)
		}
		rsNames := map[string]bool{}
		for _, rs := range rsList.Items {
			if ownedBy(rs.OwnerReferences, "Deployment", name) {
				rsNames[rs.Name] = true
			}
		}
		var out []corev1.Pod
		for _, p := range all.Items {
			for _, ref := range p.OwnerReferences {
				if ref.Kind == "ReplicaSet" && rsNames[ref.Name] {
					out = append(out, p)
					break
				}
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported kind %q (want Deployment|DaemonSet|StatefulSet)", kind)
	}
}

func filterByOwner(pods []corev1.Pod, kind, name string) []corev1.Pod {
	var out []corev1.Pod
	for _, p := range pods {
		if ownedBy(p.OwnerReferences, kind, name) {
			out = append(out, p)
		}
	}
	return out
}

func ownedBy(refs []metav1.OwnerReference, kind, name string) bool {
	for _, r := range refs {
		if r.Kind == kind && r.Name == name {
			return true
		}
	}
	return false
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(listFailingPods{}) })
}
