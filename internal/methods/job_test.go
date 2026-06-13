package methods

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckJobFailed(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "default"},
		Spec:       batchv1.JobSpec{Completions: i32(1), Parallelism: i32(1), BackoffLimit: i32(4)},
		Status: batchv1.JobStatus{
			Failed: 5,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"},
			},
		},
	}
	client := fake.NewSimpleClientset(job)
	m, ok := Builtin().Get("check_job")
	if !ok {
		t.Fatal("check_job not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "name": "migrate"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"exists": true, "failed": int64(5), "completions": int64(1),
		"parallelism": int64(1), "backoffLimit": int64(4),
		"failedCondition": true, "complete": false, "conditionReason": "BackoffLimitExceeded",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}

func TestCheckJobMissingDefaults(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_job")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "name": "nope"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["exists"] != false || out["completions"] != int64(-1) || out["backoffLimit"] != int64(6) {
		t.Errorf("missing job defaults wrong: %v", out)
	}
}
