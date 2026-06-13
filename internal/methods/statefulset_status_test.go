package methods

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckStatefulSetStatus(t *testing.T) {
	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "data"},
		Spec:       appsv1.StatefulSetSpec{Replicas: i32(3)},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: 2, CurrentReplicas: 3, UpdatedReplicas: 1, AvailableReplicas: 2,
			CurrentRevision: "db-1", UpdateRevision: "db-2",
		},
	}
	client := fake.NewSimpleClientset(ss)
	m, ok := Builtin().Get("check_statefulset_status")
	if !ok {
		t.Fatal("check_statefulset_status not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "data", "name": "db"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"desiredReplicas":       int64(3),
		"readyReplicas":         int64(2),
		"currentReplicas":       int64(3),
		"updatedReplicas":       int64(1),
		"availableReplicas":     int64(2),
		"updateRevisionPending": true,
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}

func TestCheckStatefulSetStatusMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_statefulset_status")
	if _, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "data", "name": "db"}); err == nil {
		t.Fatal("expected error for missing statefulset")
	}
}
