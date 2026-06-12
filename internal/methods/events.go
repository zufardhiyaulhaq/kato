package methods

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkEvents struct{}

func (checkEvents) Name() string { return "check_events" }
func (checkEvents) Description() string {
	return "Kubernetes events for an object or namespace, warnings first"
}

func (checkEvents) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Namespace to read events from"},
		{Name: "involvedObject", Description: "filter to events about this object name; empty = whole namespace"},
	}
}

func (checkEvents) OutputFields() []OutputField {
	return []OutputField{
		{Name: "events", Type: FieldString, Description: "rendered event lines, warnings first"},
		{Name: "count", Type: FieldInt, Description: "number of events matched"},
		{Name: "warningCount", Type: FieldInt, Description: "number of Warning events matched"},
	}
}

func (checkEvents) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	list, err := deps.Kube.CoreV1().Events(params["namespace"]).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list events in %s: %w", params["namespace"], err)
	}
	var matched []corev1.Event
	for _, e := range list.Items {
		if obj := params["involvedObject"]; obj != "" && e.InvolvedObject.Name != obj {
			continue
		}
		matched = append(matched, e)
	}
	// Warnings first, then by lastTimestamp descending.
	sort.SliceStable(matched, func(i, j int) bool {
		wi, wj := matched[i].Type == corev1.EventTypeWarning, matched[j].Type == corev1.EventTypeWarning
		if wi != wj {
			return wi
		}
		return matched[j].LastTimestamp.Before(&matched[i].LastTimestamp)
	})
	var b strings.Builder
	warnings := int64(0)
	for _, e := range matched {
		if e.Type == corev1.EventTypeWarning {
			warnings++
		}
		fmt.Fprintf(&b, "[%s] %s %s/%s: %s (x%d)\n",
			e.Type, e.Reason, e.InvolvedObject.Kind, e.InvolvedObject.Name, e.Message, max(e.Count, 1))
	}
	return Outputs{
		"events":       Truncate(b.String(), defaultLogBytes),
		"count":        int64(len(matched)),
		"warningCount": warnings,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkEvents{}) }) }
