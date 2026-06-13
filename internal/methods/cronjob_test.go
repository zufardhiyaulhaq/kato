package methods

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckCronJob(t *testing.T) {
	suspend := false
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default"},
		Spec: batchv1.CronJobSpec{
			Schedule:          "0 2 * * *",
			Suspend:           &suspend,
			ConcurrencyPolicy: batchv1.ForbidConcurrent,
		},
		Status: batchv1.CronJobStatus{Active: []corev1.ObjectReference{{Name: "backup-123"}}},
	}
	client := fake.NewSimpleClientset(cj)
	m, ok := Builtin().Get("check_cronjob")
	if !ok {
		t.Fatal("check_cronjob not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "name": "backup"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"exists": true, "schedule": "0 2 * * *", "suspended": false,
		"activeJobs": int64(1), "concurrencyPolicy": "Forbid", "lastScheduleTime": "",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}

func TestCheckCronJobMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_cronjob")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "name": "nope"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["exists"] != false || out["schedule"] != "" {
		t.Errorf("missing cronjob defaults wrong: %v", out)
	}
}
