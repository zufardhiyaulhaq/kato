package methods

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

func stuckDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "payments"},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(3)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
		},
		Status: appsv1.DeploymentStatus{
			Replicas: 3, ReadyReplicas: 1, UpdatedReplicas: 1, UnavailableReplicas: 2,
			Conditions: []appsv1.DeploymentCondition{{
				Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse,
				Reason: "ProgressDeadlineExceeded", Message: "ReplicaSet has timed out progressing",
			}},
		},
	}
}

func TestCheckDeploymentStatus(t *testing.T) {
	client := fake.NewSimpleClientset(stuckDeployment())
	m, ok := Builtin().Get("check_deployment_status")
	if !ok {
		t.Fatal("check_deployment_status not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := Outputs{
		"desiredReplicas": int64(3), "readyReplicas": int64(1), "updatedReplicas": int64(1),
		"available": false, "progressing": false,
		"progressingReason": "ProgressDeadlineExceeded",
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %#v, want %#v", k, out[k], v)
		}
	}
}

func TestDescribeDeployment(t *testing.T) {
	client := fake.NewSimpleClientset(stuckDeployment())
	m, ok := Builtin().Get("describe_deployment")
	if !ok {
		t.Fatal("describe_deployment not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out["manifest"].(string), "ProgressDeadlineExceeded") {
		t.Errorf("manifest missing condition: %q", out["manifest"])
	}
}

func TestDescribeDeploymentStructuredOutputs(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "payments"},
		Spec: appsv1.DeploymentSpec{
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: "api-sa",
					Containers: []corev1.Container{{
						Name: "api", Image: "api:v3",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")},
						},
					}},
				},
			},
		},
	}
	client := fake.NewSimpleClientset(dep)
	m, _ := Builtin().Get("describe_deployment")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]string{
		"containers":       "api",
		"images":           "api:v3",
		"resourceRequests": "api: cpu=250m",
		"resourceLimits":   "",
		"strategy":         "RollingUpdate",
		"serviceAccount":   "api-sa",
	}
	for field, want := range checks {
		if got := out[field]; got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}
}

func TestCheckReplicaSet(t *testing.T) {
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-7d9f8b", Namespace: "payments",
			Labels:          map[string]string{"app": "api"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "api"}},
		},
		Spec: appsv1.ReplicaSetSpec{Replicas: ptr.To(int32(3))},
		Status: appsv1.ReplicaSetStatus{
			Replicas: 0, ReadyReplicas: 0,
			Conditions: []appsv1.ReplicaSetCondition{{
				Type: appsv1.ReplicaSetReplicaFailure, Status: corev1.ConditionTrue,
				Reason: "FailedCreate", Message: `pods "api-7d9f8b-" is forbidden: exceeded quota`,
			}},
		},
	}
	client := fake.NewSimpleClientset(rs)
	m, ok := Builtin().Get("check_replicaset")
	if !ok {
		t.Fatal("check_replicaset not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "deployment": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["replicaFailure"] != true {
		t.Errorf("replicaFailure = %v", out["replicaFailure"])
	}
	if !strings.Contains(out["failureMessage"].(string), "exceeded quota") {
		t.Errorf("failureMessage = %q", out["failureMessage"])
	}
}

func TestDescribeDeploymentRolloutFields(t *testing.T) {
	ms := intstr.FromString("25%")
	mu := intstr.FromInt(1)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: i32(3),
			Paused:   true,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Strategy: appsv1.DeploymentStrategy{
				Type:          appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{MaxSurge: &ms, MaxUnavailable: &mu},
			},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				NodeSelector: map[string]string{"tier": "be"},
				Containers: []corev1.Container{{
					Name: "api", Image: "api:v1",
					LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{Port: intstr.FromInt(8080), Path: "/live"}}},
				}},
			}},
		},
	}
	client := fake.NewSimpleClientset(d)
	m, _ := Builtin().Get("describe_deployment")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "name": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"replicas":       int64(3),
		"paused":         true,
		"selector":       "app=api",
		"maxSurge":       "25%",
		"maxUnavailable": "1",
		"nodeSelector":   "tier=be",
		"probes":         "api: liveness=httpGet:8080/live readiness=— startup=—",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}
