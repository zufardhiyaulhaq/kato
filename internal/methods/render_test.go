package methods

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestRenderKVMap(t *testing.T) {
	if got := renderKVMap(nil); got != "" {
		t.Errorf("empty = %q, want \"\"", got)
	}
	if got := renderKVMap(map[string]string{"tier": "backend", "app": "api"}); got != "app=api, tier=backend" {
		t.Errorf("renderKVMap = %q", got)
	}
}

func TestRenderTolerations(t *testing.T) {
	if got := renderTolerations(nil); got != "" {
		t.Errorf("empty = %q", got)
	}
	got := renderTolerations([]corev1.Toleration{
		{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
		{Operator: corev1.TolerationOpExists}, // bare exists -> tolerates all
	})
	if got != "dedicated=gpu:NoSchedule, <all>" {
		t.Errorf("renderTolerations = %q", got)
	}
}

func TestRenderOwnerRefs(t *testing.T) {
	if got := renderOwnerRefs([]metav1.OwnerReference{{Kind: "ReplicaSet", Name: "api-abc"}}); got != "ReplicaSet/api-abc" {
		t.Errorf("renderOwnerRefs = %q", got)
	}
}

func TestRenderPodConditions(t *testing.T) {
	got := renderPodConditions([]corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"},
		{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, Reason: "ignored-when-true"},
	})
	if got != "Ready=False (ContainersNotReady), PodScheduled=True" {
		t.Errorf("renderPodConditions = %q", got)
	}
}

func TestRenderProbes(t *testing.T) {
	cs := []corev1.Container{
		{Name: "app", LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Port: intstr.FromInt(8080), Path: "/healthz"}}}},
		{Name: "sidecar"}, // no probes -> omitted entirely
	}
	if got := renderProbes(cs); got != "app: liveness=httpGet:8080/healthz readiness=— startup=—" {
		t.Errorf("renderProbes = %q", got)
	}
	if got := renderProbes([]corev1.Container{{Name: "x"}}); got != "" {
		t.Errorf("no-probe = %q, want \"\"", got)
	}
}

func TestRenderPorts(t *testing.T) {
	got := renderPorts([]corev1.ServicePort{
		{Name: "http", Port: 80, TargetPort: intstr.FromInt(8080), Protocol: corev1.ProtocolTCP},
		{Port: 443, TargetPort: intstr.FromString("https")}, // no protocol -> defaults TCP
	})
	if got != "http:80→8080/TCP, 443→https/TCP" {
		t.Errorf("renderPorts = %q", got)
	}
}
