package methods

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkJob struct{}

func (checkJob) Name() string        { return "check_job" }
func (checkJob) Description() string { return "Job completion and failure status" }

func (checkJob) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Job namespace"},
		{Name: "name", Required: true, Description: "Job name"},
	}
}

func (checkJob) OutputFields() []OutputField {
	return []OutputField{
		{Name: "exists", Type: FieldBool, Description: "Job exists"},
		{Name: "active", Type: FieldInt, Description: "status.active"},
		{Name: "succeeded", Type: FieldInt, Description: "status.succeeded"},
		{Name: "failed", Type: FieldInt, Description: "status.failed"},
		{Name: "completions", Type: FieldInt, Description: "spec.completions, -1 if unset"},
		{Name: "parallelism", Type: FieldInt, Description: "spec.parallelism, 1 if unset"},
		{Name: "backoffLimit", Type: FieldInt, Description: "spec.backoffLimit, 6 if unset (k8s default)"},
		{Name: "complete", Type: FieldBool, Description: "Complete condition is True"},
		{Name: "failedCondition", Type: FieldBool, Description: "Failed condition is True"},
		{Name: "conditionReason", Type: FieldString, Description: `e.g. BackoffLimitExceeded, DeadlineExceeded, "" if none`},
	}
}

func (checkJob) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	out := Outputs{
		"exists": false, "active": int64(0), "succeeded": int64(0), "failed": int64(0),
		"completions": int64(-1), "parallelism": int64(1), "backoffLimit": int64(6),
		"complete": false, "failedCondition": false, "conditionReason": "",
	}
	job, err := deps.Kube.BatchV1().Jobs(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get job %s/%s: %w", params["namespace"], params["name"], err)
	}
	out["exists"] = true
	out["active"] = int64(job.Status.Active)
	out["succeeded"] = int64(job.Status.Succeeded)
	out["failed"] = int64(job.Status.Failed)
	if job.Spec.Completions != nil {
		out["completions"] = int64(*job.Spec.Completions)
	}
	if job.Spec.Parallelism != nil {
		out["parallelism"] = int64(*job.Spec.Parallelism)
	}
	if job.Spec.BackoffLimit != nil {
		out["backoffLimit"] = int64(*job.Spec.BackoffLimit)
	}
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			out["complete"] = true
		case batchv1.JobFailed:
			out["failedCondition"] = true
			out["conditionReason"] = c.Reason
		}
	}
	return out, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkJob{}) }) }
