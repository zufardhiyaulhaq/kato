package methods

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkCronJob struct{}

func (checkCronJob) Name() string        { return "check_cronjob" }
func (checkCronJob) Description() string { return "CronJob schedule and recent run status" }

func (checkCronJob) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "CronJob namespace"},
		{Name: "name", Required: true, Description: "CronJob name"},
	}
}

func (checkCronJob) OutputFields() []OutputField {
	return []OutputField{
		{Name: "exists", Type: FieldBool, Description: "CronJob exists"},
		{Name: "schedule", Type: FieldString, Description: "spec.schedule (cron expression)"},
		{Name: "suspended", Type: FieldBool, Description: "spec.suspend"},
		{Name: "activeJobs", Type: FieldInt, Description: "number of currently active jobs"},
		{Name: "lastScheduleTime", Type: FieldString, Description: `RFC3339, "" if never scheduled`},
		{Name: "lastSuccessfulTime", Type: FieldString, Description: `RFC3339, "" if never succeeded`},
		{Name: "concurrencyPolicy", Type: FieldString, Description: "Allow|Forbid|Replace"},
	}
}

func (checkCronJob) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	out := Outputs{
		"exists": false, "schedule": "", "suspended": false, "activeJobs": int64(0),
		"lastScheduleTime": "", "lastSuccessfulTime": "", "concurrencyPolicy": "",
	}
	cj, err := deps.Kube.BatchV1().CronJobs(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get cronjob %s/%s: %w", params["namespace"], params["name"], err)
	}
	out["exists"] = true
	out["schedule"] = cj.Spec.Schedule
	if cj.Spec.Suspend != nil {
		out["suspended"] = *cj.Spec.Suspend
	}
	out["activeJobs"] = int64(len(cj.Status.Active))
	if cj.Status.LastScheduleTime != nil {
		out["lastScheduleTime"] = cj.Status.LastScheduleTime.Time.UTC().Format(time.RFC3339)
	}
	if cj.Status.LastSuccessfulTime != nil {
		out["lastSuccessfulTime"] = cj.Status.LastSuccessfulTime.Time.UTC().Format(time.RFC3339)
	}
	out["concurrencyPolicy"] = string(cj.Spec.ConcurrencyPolicy)
	return out, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkCronJob{}) }) }
