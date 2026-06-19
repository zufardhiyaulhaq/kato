package methods

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
)

type listNodePods struct{}

func (listNodePods) Name() string { return "list_node_pods" }
func (listNodePods) Description() string {
	return "Pods scheduled on a node, optionally filtered by a name regex, worst-first"
}

func (listNodePods) Params() []Param {
	return []Param{
		{Name: "node", Required: true, Description: "Node name; lists pods with spec.nodeName == this"},
		{Name: "namePattern", Description: `RE2 regex matched (partial) against pod name, e.g. "coredns|terway"; empty = all pods on the node`},
		{Name: "namespace", Description: "restrict to this namespace; empty = all namespaces"},
		{Name: "maxListItems", Description: `cap the pods list at this many items, worst-first (default "50"); "0" = unlimited`},
	}
}

func (listNodePods) OutputFields() []OutputField {
	return []OutputField{
		{Name: "count", Type: FieldInt, Description: "pods matched (node + namespace + namePattern)"},
		{Name: "notReadyCount", Type: FieldInt, Description: "matched pods whose Ready condition is not True"},
		{Name: "listTruncated", Type: FieldBool, Description: "true if more pods matched than the pods list carries"},
	}
}

func (listNodePods) ListOutputs() []ListOutputField {
	return []ListOutputField{{
		Name:        "pods",
		Description: "pods on the node matching the filters, sorted not-ready first then by restartCount",
		ItemFields: []OutputField{
			{Name: "namespace", Type: FieldString, Description: "pod namespace"},
			{Name: "name", Type: FieldString, Description: "pod name"},
			{Name: "ready", Type: FieldBool, Description: "Ready condition is True"},
			{Name: "restartCount", Type: FieldInt, Description: "max restartCount across containers"},
			{Name: "phase", Type: FieldString, Description: "pod phase (Pending|Running|Succeeded|Failed|Unknown)"},
			{Name: "reason", Type: FieldString, Description: "dominant waiting/termination reason, empty if none"},
		},
	}}
}

func (listNodePods) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	node := params["node"]
	if node == "" {
		return nil, fmt.Errorf("param node: required")
	}
	var namePat *regexp.Regexp
	if p := params["namePattern"]; p != "" {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("param namePattern: %w", err)
		}
		namePat = re
	}
	maxItems, err := parseMaxListItems(params)
	if err != nil {
		return nil, err
	}

	// Field-select by node server-side so a large cluster does not return every pod;
	// the client-side nodeName check below keeps correctness even when the apiserver
	// (or a fake client) ignores the selector.
	opts := metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("spec.nodeName", node).String()}
	list, err := deps.Kube.CoreV1().Pods(params["namespace"]).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list pods on node %s: %w", node, err)
	}

	items := []map[string]any{}
	notReady := 0
	for i := range list.Items {
		p := &list.Items[i]
		if p.Spec.NodeName != node {
			continue
		}
		if namePat != nil && !namePat.MatchString(p.Name) {
			continue
		}
		ready := podReady(p)
		if !ready {
			notReady++
		}
		items = append(items, map[string]any{
			"namespace": p.Namespace, "name": p.Name, "ready": ready,
			"restartCount": int64(maxRestart(p)), "phase": string(p.Status.Phase),
			"reason": podDominantReason(p),
		})
	}

	// Not-ready first, then by restartCount desc, then name for stability.
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := items[i]["ready"].(bool), items[j]["ready"].(bool)
		if ri != rj {
			return !ri // not-ready first
		}
		ci, cj := items[i]["restartCount"].(int64), items[j]["restartCount"].(int64)
		if ci != cj {
			return ci > cj
		}
		return items[i]["name"].(string) < items[j]["name"].(string)
	})

	total := len(items)
	capped, truncated := capItems(items, maxItems)
	return Outputs{
		"count":         int64(total),
		"notReadyCount": int64(notReady),
		"listTruncated": truncated,
		"pods":          capped,
	}, nil
}

// podDominantReason returns the most informative current reason: a container's
// waiting reason (e.g. CrashLoopBackOff), else the last-termination reason of a
// not-ready container (e.g. OOMKilled), else "".
func podDominantReason(p *corev1.Pod) string {
	for _, cs := range p.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil && w.Reason != "" {
			return w.Reason
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			continue
		}
		if t := cs.LastTerminationState.Terminated; t != nil {
			if t.Reason != "" {
				return t.Reason
			}
			if t.ExitCode != 0 {
				return "Error"
			}
		}
	}
	return ""
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(listNodePods{}) }) }
