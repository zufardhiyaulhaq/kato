package engine

import (
	"strings"
	"testing"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

func validUseCase() *v1alpha1.UseCase {
	return &v1alpha1.UseCase{
		Spec: v1alpha1.UseCaseSpec{
			Inputs: []v1alpha1.InputDecl{
				{Name: "namespace", Required: true}, {Name: "pod", Required: true},
			},
			Steps: []v1alpha1.Step{
				{Name: "status", Method: "check_pod_status",
					With: map[string]string{"namespace": "$(inputs.namespace)", "name": "$(inputs.pod)"}},
				{Name: "prev-logs", Method: "check_pod_logs",
					When: "$(steps.status.restartCount) > 0",
					With: map[string]string{"namespace": "$(inputs.namespace)", "name": "$(inputs.pod)", "previous": "true"},
					SummaryFilter: []string{"logs"}},
			},
			Summary: v1alpha1.SummarySpec{Prompt: "diagnose"},
		},
	}
}

func validate(uc *v1alpha1.UseCase) []string {
	return ValidateUseCase(uc, methods.Builtin(), func(string) bool { return true })
}

func TestValidateUseCaseOK(t *testing.T) {
	if errs := validate(validUseCase()); len(errs) != 0 {
		t.Fatalf("valid use case rejected: %v", errs)
	}
}

func TestValidateUseCaseErrors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*v1alpha1.UseCase)
		wantSub string
	}{
		{"unknown method", func(u *v1alpha1.UseCase) { u.Spec.Steps[0].Method = "check_nothing" }, "unknown method"},
		{"duplicate step name", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].Name = "status" }, "duplicate"},
		{"unknown output in when", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].When = "$(steps.status.restartCnt) > 0" }, "restartCnt"},
		{"forward reference", func(u *v1alpha1.UseCase) { u.Spec.Steps[0].When = "$(steps.prev-logs.logs) != \"\"" }, "unknown or not before"},
		{"type error", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].When = "$(steps.status.phase) > 3" }, "when"},
		{"missing required param", func(u *v1alpha1.UseCase) { delete(u.Spec.Steps[0].With, "name") }, "required param"},
		{"unknown param", func(u *v1alpha1.UseCase) { u.Spec.Steps[0].With["bogus"] = "x" }, "unknown param"},
		{"bad summaryFilter", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].SummaryFilter = []string{"nope"} }, "summaryFilter"},
		{"unknown input ref", func(u *v1alpha1.UseCase) { u.Spec.Steps[0].With["name"] = "$(inputs.podd)" }, "unknown input"},
	}
	for _, tc := range cases {
		uc := validUseCase()
		tc.mutate(uc)
		errs := validate(uc)
		if len(errs) == 0 {
			t.Errorf("%s: expected validation error", tc.name)
			continue
		}
		joined := strings.Join(errs, "; ")
		if !strings.Contains(joined, tc.wantSub) {
			t.Errorf("%s: errors %q do not mention %q", tc.name, joined, tc.wantSub)
		}
	}
}

func TestValidateUseCaseMissingModelConfig(t *testing.T) {
	uc := validUseCase()
	uc.Spec.Summary.ModelConfigRef = "missing"
	errs := ValidateUseCase(uc, methods.Builtin(), func(string) bool { return false })
	if len(errs) == 0 || !strings.Contains(errs[0], "modelConfigRef") {
		t.Fatalf("expected modelConfigRef error, got %v", errs)
	}
}
