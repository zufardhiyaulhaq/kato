package methods

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDescribeStatefulSet(t *testing.T) {
	sc := "gp3"
	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "data"},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         "db-headless",
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}},
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type:          appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: i32(2)},
			},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "db", Image: "postgres:16"}},
			}},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "data"},
				Spec: corev1.PersistentVolumeClaimSpec{
					StorageClassName: &sc,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
					},
				},
			}},
		},
	}
	client := fake.NewSimpleClientset(ss)
	m, ok := Builtin().Get("describe_statefulset")
	if !ok {
		t.Fatal("describe_statefulset not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "data", "name": "db"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"serviceName":          "db-headless",
		"podManagementPolicy":  "Parallel",
		"partition":            int64(2),
		"volumeClaimTemplates": "data: 10Gi (gp3)",
		"selector":             "app=db",
		"updateStrategy":       "RollingUpdate",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}
