package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestSchemeRegistersAllKinds(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	for _, obj := range []runtime.Object{
		&UseCase{}, &UseCaseList{},
		&ModelConfig{}, &ModelConfigList{},
		&Run{}, &RunList{},
	} {
		if _, _, err := s.ObjectKinds(obj); err != nil {
			t.Errorf("kind not registered: %T: %v", obj, err)
		}
	}
}

func TestForEachFieldsAndDeepCopy(t *testing.T) {
	uc := &UseCase{
		Spec: UseCaseSpec{
			Steps: []Step{{
				Name: "logs", Method: "check_pod_logs",
				ForEach: "$(steps.crashing.pods)", MaxItems: 3,
				With: map[string]string{"name": "$(item.name)"},
			}},
		},
	}
	cp := uc.DeepCopy()
	if cp.Spec.Steps[0].ForEach != "$(steps.crashing.pods)" || cp.Spec.Steps[0].MaxItems != 3 {
		t.Fatalf("forEach fields not copied: %+v", cp.Spec.Steps[0])
	}

	run := &Run{Status: RunStatus{Steps: []RunStep{{
		Name: "logs", Outcome: "completed",
		Note:       "matched 12, checked 3 (worst-first); 9 not examined",
		Iterations: []RunStepIteration{{Item: map[string]string{"name": "nld-a"}, Outcome: "completed"}},
	}}}}
	rc := run.DeepCopy()
	if rc.Status.Steps[0].Note == "" || len(rc.Status.Steps[0].Iterations) != 1 {
		t.Fatalf("run iteration fields not copied: %+v", rc.Status.Steps[0])
	}
}
