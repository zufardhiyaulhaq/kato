package methods

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type listNodes struct{}

func (listNodes) Name() string { return "list_nodes" }
func (listNodes) Description() string {
	return "Fleet node health bucketed by status; lists only the not-fully-healthy nodes"
}

func (listNodes) Params() []Param {
	return []Param{
		{Name: "labelSelector", Description: "k8s label selector to scope the scan (e.g. a nodepool/role label); empty = all nodes"},
		{Name: "includeHealthy", Description: `"true" to also list Ready/schedulable/no-pressure nodes (default "false" = problem nodes only)`},
		{Name: "maxListItems", Description: `cap the nodes list at this many items, worst-first (default "50"); "0" = unlimited`},
	}
}

func (listNodes) OutputFields() []OutputField {
	return []OutputField{
		{Name: "total", Type: FieldInt, Description: "nodes matched by the selector"},
		{Name: "ready", Type: FieldInt, Description: "nodes with Ready=True"},
		{Name: "notReady", Type: FieldInt, Description: "nodes with Ready!=True"},
		{Name: "memoryPressure", Type: FieldInt, Description: "nodes with MemoryPressure=True"},
		{Name: "diskPressure", Type: FieldInt, Description: "nodes with DiskPressure=True"},
		{Name: "pidPressure", Type: FieldInt, Description: "nodes with PIDPressure=True"},
		{Name: "unschedulable", Type: FieldInt, Description: "cordoned nodes (spec.unschedulable)"},
		{Name: "anyUnhealthy", Type: FieldBool, Description: "notReady > 0 or any pressure > 0"},
		{Name: "listTruncated", Type: FieldBool, Description: "true if more problem nodes matched than the list carries"},
	}
}

func (listNodes) ListOutputs() []ListOutputField {
	return []ListOutputField{{
		Name:        "nodes",
		Description: "not-fully-healthy nodes (NotReady/pressured/cordoned), worst-first; includes healthy nodes only when includeHealthy is true",
		ItemFields: []OutputField{
			{Name: "name", Type: FieldString, Description: "node name"},
			{Name: "ready", Type: FieldBool, Description: "Ready condition is True"},
			{Name: "status", Type: FieldString, Description: "compact status label, e.g. NotReady, Ready,MemoryPressure, Ready,SchedulingDisabled"},
			{Name: "reason", Type: FieldString, Description: "Ready reason when NotReady, else pressure summary; empty if none"},
			{Name: "unschedulable", Type: FieldBool, Description: "spec.unschedulable (cordoned)"},
		},
	}}
}

// nodeHealth is the per-node classification used for counting, the list item, and
// the worst-first ordering rank.
type nodeHealth struct {
	ready                                  bool
	readyReason                            string
	memoryPressure, diskPressure, pidPress bool
	unschedulable                          bool
}

func classifyNode(n *corev1.Node) nodeHealth {
	h := nodeHealth{unschedulable: n.Spec.Unschedulable}
	for _, c := range n.Status.Conditions {
		isTrue := c.Status == corev1.ConditionTrue
		switch c.Type {
		case corev1.NodeReady:
			h.ready = isTrue
			if !isTrue {
				h.readyReason = c.Reason
			}
		case corev1.NodeMemoryPressure:
			h.memoryPressure = isTrue
		case corev1.NodeDiskPressure:
			h.diskPressure = isTrue
		case corev1.NodePIDPressure:
			h.pidPress = isTrue
		}
	}
	return h
}

// pressured reports whether any pressure condition is set.
func (h nodeHealth) pressured() bool { return h.memoryPressure || h.diskPressure || h.pidPress }

// healthy reports a fully-normal node: Ready, no pressure, schedulable.
func (h nodeHealth) healthy() bool { return h.ready && !h.pressured() && !h.unschedulable }

// rank orders nodes worst-first: NotReady < pressured < cordoned-only < healthy.
func (h nodeHealth) rank() int {
	switch {
	case !h.ready:
		return 0
	case h.pressured():
		return 1
	case h.unschedulable:
		return 2
	default:
		return 3
	}
}

// status renders the compact label, e.g. "NotReady", "Ready,MemoryPressure,SchedulingDisabled".
func (h nodeHealth) status() string {
	parts := []string{"Ready"}
	if !h.ready {
		parts[0] = "NotReady"
	}
	if h.memoryPressure {
		parts = append(parts, "MemoryPressure")
	}
	if h.diskPressure {
		parts = append(parts, "DiskPressure")
	}
	if h.pidPress {
		parts = append(parts, "PIDPressure")
	}
	if h.unschedulable {
		parts = append(parts, "SchedulingDisabled")
	}
	return strings.Join(parts, ",")
}

// reason is the Ready reason when NotReady, otherwise a compact pressure summary.
func (h nodeHealth) reason() string {
	if !h.ready {
		return h.readyReason
	}
	var p []string
	if h.memoryPressure {
		p = append(p, "MemoryPressure")
	}
	if h.diskPressure {
		p = append(p, "DiskPressure")
	}
	if h.pidPress {
		p = append(p, "PIDPressure")
	}
	return strings.Join(p, ",")
}

func (listNodes) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	includeHealthy, err := parseBoolDefault(params, "includeHealthy", false)
	if err != nil {
		return nil, err
	}
	maxItems, err := parseMaxListItems(params)
	if err != nil {
		return nil, err
	}
	sel := params["labelSelector"]
	if sel != "" {
		if _, perr := labels.Parse(sel); perr != nil {
			return nil, fmt.Errorf("param labelSelector: %w", perr)
		}
	}

	list, err := deps.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	var ready, notReady, mem, disk, pid, cordon int
	items := []map[string]any{}
	healths := map[string]nodeHealth{}
	for i := range list.Items {
		n := &list.Items[i]
		h := classifyNode(n)
		if h.ready {
			ready++
		} else {
			notReady++
		}
		if h.memoryPressure {
			mem++
		}
		if h.diskPressure {
			disk++
		}
		if h.pidPress {
			pid++
		}
		if h.unschedulable {
			cordon++
		}
		if !h.healthy() || includeHealthy {
			healths[n.Name] = h
			items = append(items, map[string]any{
				"name": n.Name, "ready": h.ready, "status": h.status(),
				"reason": h.reason(), "unschedulable": h.unschedulable,
			})
		}
	}

	// Worst-first by rank, then name for stability.
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := healths[items[i]["name"].(string)].rank(), healths[items[j]["name"].(string)].rank()
		if ri != rj {
			return ri < rj
		}
		return items[i]["name"].(string) < items[j]["name"].(string)
	})
	capped, truncated := capItems(items, maxItems)

	return Outputs{
		"total":          int64(len(list.Items)),
		"ready":          int64(ready),
		"notReady":       int64(notReady),
		"memoryPressure": int64(mem),
		"diskPressure":   int64(disk),
		"pidPressure":    int64(pid),
		"unschedulable":  int64(cordon),
		"anyUnhealthy":   notReady > 0 || mem > 0 || disk > 0 || pid > 0,
		"listTruncated":  truncated,
		"nodes":          capped,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(listNodes{}) }) }
