package methods

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckDaemonSetStatus(t *testing.T) {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "node-exporter", Namespace: "monitoring"},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 5,
			CurrentNumberScheduled: 5,
			NumberReady:            3,
			NumberAvailable:        3,
			NumberMisscheduled:     1,
			UpdatedNumberScheduled: 4,
		},
	}
	client := fake.NewSimpleClientset(ds)
	m, ok := Builtin().Get("check_daemonset_status")
	if !ok {
		t.Fatal("check_daemonset_status not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "monitoring", "name": "node-exporter"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := Outputs{
		"desiredScheduled": int64(5), "currentScheduled": int64(5),
		"ready": int64(3), "available": int64(3),
		"misscheduled": int64(1), "updatedScheduled": int64(4),
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %#v, want %#v", k, out[k], v)
		}
	}
}

func TestCheckDaemonSetStatusNotFound(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_daemonset_status")
	if _, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "monitoring", "name": "ghost"}); err == nil {
		t.Fatal("expected error for missing daemonset")
	}
}
