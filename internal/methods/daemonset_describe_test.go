package methods

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDescribeDaemonSet(t *testing.T) {
	mu := intstr.FromInt(1)
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "fluentd", Namespace: "logging"},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "fluentd"}},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type:          appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{MaxUnavailable: &mu},
			},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				ServiceAccountName: "fluentd-sa",
				NodeSelector:       map[string]string{"role": "log"},
				Tolerations:        []corev1.Toleration{{Key: "node-role", Effect: corev1.TaintEffectNoSchedule}},
				Containers:         []corev1.Container{{Name: "fluentd", Image: "fluentd:v1"}},
			}},
		},
	}
	client := fake.NewSimpleClientset(ds)
	m, ok := Builtin().Get("describe_daemonset")
	if !ok {
		t.Fatal("describe_daemonset not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "logging", "name": "fluentd"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"containers":     "fluentd",
		"images":         "fluentd:v1",
		"serviceAccount": "fluentd-sa",
		"selector":       "app=fluentd",
		"updateStrategy": "RollingUpdate",
		"maxUnavailable": "1",
		"nodeSelector":   "role=log",
		"tolerations":    "node-role:NoSchedule",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}
