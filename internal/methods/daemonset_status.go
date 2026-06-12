package methods

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkDaemonSetStatus struct{}

func (checkDaemonSetStatus) Name() string        { return "check_daemonset_status" }
func (checkDaemonSetStatus) Description() string { return "DaemonSet scheduling and readiness counts" }

func (checkDaemonSetStatus) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "DaemonSet namespace"},
		{Name: "name", Required: true, Description: "DaemonSet name"},
	}
}

func (checkDaemonSetStatus) OutputFields() []OutputField {
	return []OutputField{
		{Name: "desiredScheduled", Type: FieldInt, Description: "nodes that should run the pod (status.desiredNumberScheduled)"},
		{Name: "currentScheduled", Type: FieldInt, Description: "nodes running at least one pod (status.currentNumberScheduled)"},
		{Name: "ready", Type: FieldInt, Description: "pods ready (status.numberReady)"},
		{Name: "available", Type: FieldInt, Description: "pods available (status.numberAvailable)"},
		{Name: "misscheduled", Type: FieldInt, Description: "pods running where they should not be (status.numberMisscheduled)"},
		{Name: "updatedScheduled", Type: FieldInt, Description: "pods on the updated template (status.updatedNumberScheduled)"},
	}
}

func (checkDaemonSetStatus) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	ds, err := deps.Kube.AppsV1().DaemonSets(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get daemonset %s/%s: %w", params["namespace"], params["name"], err)
	}
	return Outputs{
		"desiredScheduled": int64(ds.Status.DesiredNumberScheduled),
		"currentScheduled": int64(ds.Status.CurrentNumberScheduled),
		"ready":            int64(ds.Status.NumberReady),
		"available":        int64(ds.Status.NumberAvailable),
		"misscheduled":     int64(ds.Status.NumberMisscheduled),
		"updatedScheduled": int64(ds.Status.UpdatedNumberScheduled),
	}, nil
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkDaemonSetStatus{}) })
}
