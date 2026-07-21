package methods

import (
	"context"
	"testing"

	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

func TestCheckPDBHealthy(t *testing.T) {
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: ptr.To(intstr.FromString("50%")),
			Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
		},
		Status: policyv1.PodDisruptionBudgetStatus{
			ExpectedPods: 4, CurrentHealthy: 4, DesiredHealthy: 2, DisruptionsAllowed: 2,
			Conditions: []metav1.Condition{{
				Type: policyv1.DisruptionAllowedCondition, Status: metav1.ConditionTrue,
				Reason: "SufficientPods",
			}},
		},
	}
	client := fake.NewSimpleClientset(pdb)
	m, ok := Builtin().Get("check_pdb")
	if !ok {
		t.Fatal("check_pdb not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "prod", "name": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := Outputs{
		"exists": true, "minAvailable": "50%", "maxUnavailable": "", "selector": "app=api",
		"expectedPods": int64(4), "currentHealthy": int64(4), "desiredHealthy": int64(2),
		"disruptionsAllowed": int64(2), "blocked": false, "conditionReason": "",
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("out[%q] = %v, want %v", k, out[k], v)
		}
	}
}

func TestCheckPDBBlocked(t *testing.T) {
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: ptr.To(intstr.FromInt32(1)),
		},
		Status: policyv1.PodDisruptionBudgetStatus{
			ExpectedPods: 3, CurrentHealthy: 1, DesiredHealthy: 2, DisruptionsAllowed: 0,
			Conditions: []metav1.Condition{{
				Type: policyv1.DisruptionAllowedCondition, Status: metav1.ConditionFalse,
				Reason: "InsufficientPods",
			}},
		},
	}
	client := fake.NewSimpleClientset(pdb)
	m, _ := Builtin().Get("check_pdb")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "prod", "name": "db"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["blocked"] != true || out["disruptionsAllowed"] != int64(0) {
		t.Fatalf("want blocked=true at zero budget, got %v", out)
	}
	if out["conditionReason"] != "InsufficientPods" || out["maxUnavailable"] != "1" {
		t.Fatalf("bad condition/budget rendering: %v", out)
	}
}

func TestCheckPDBMissingIsAFinding(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_pdb")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "prod", "name": "nope"})
	if err != nil {
		t.Fatalf("missing PDB must be a finding, not an error: %v", err)
	}
	want := Outputs{
		"exists": false, "minAvailable": "", "maxUnavailable": "", "selector": "",
		"expectedPods": int64(0), "currentHealthy": int64(0), "desiredHealthy": int64(0),
		"disruptionsAllowed": int64(0), "blocked": false, "conditionReason": "",
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("out[%q] = %v, want %v", k, out[k], v)
		}
	}
}
