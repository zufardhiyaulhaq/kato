package engine

import (
	"strings"
	"testing"

	"github.com/gopaytech/kato/api/v1alpha1"
	"github.com/gopaytech/kato/internal/methods"
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
					When:          "$(steps.status.restartCount) > 0",
					With:          map[string]string{"namespace": "$(inputs.namespace)", "name": "$(inputs.pod)", "previous": "true"},
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

func foreachUseCase() *v1alpha1.UseCase {
	return &v1alpha1.UseCase{
		Spec: v1alpha1.UseCaseSpec{
			Inputs: []v1alpha1.InputDecl{
				{Name: "namespace", Required: true}, {Name: "workload", Required: true},
			},
			Steps: []v1alpha1.Step{
				{Name: "crashing", Method: "list_failing_pods",
					With: map[string]string{"namespace": "$(inputs.namespace)", "kind": "DaemonSet", "name": "$(inputs.workload)"}},
				{Name: "logs", Method: "check_pod_logs",
					ForEach:  "$(steps.crashing.pods)",
					MaxItems: 3,
					When:     "$(steps.crashing.anyFailing)",
					With:     map[string]string{"namespace": "$(item.namespace)", "name": "$(item.name)"}},
			},
			Summary: v1alpha1.SummarySpec{Prompt: "x"},
		},
	}
}

func validateFE(uc *v1alpha1.UseCase) []string {
	return ValidateUseCase(uc, methods.Builtin(), func(string) bool { return true })
}

func TestValidateForEachOK(t *testing.T) {
	if errs := validateFE(foreachUseCase()); len(errs) != 0 {
		t.Fatalf("valid forEach use case rejected: %v", errs)
	}
}

func TestValidateForEachErrors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*v1alpha1.UseCase)
		wantSub string
	}{
		{"forEach not a list", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].ForEach = "$(steps.crashing.count)" }, "not a list output"},
		{"forEach unknown step", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].ForEach = "$(steps.nope.pods)" }, "unknown or not before"},
		{"forEach forward ref", func(u *v1alpha1.UseCase) {
			u.Spec.Steps[0].ForEach = "$(steps.logs.pods)"
			u.Spec.Steps[0].With = map[string]string{"name": "$(item.name)"}
		}, "unknown or not before"},
		{"item field unknown", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].With["name"] = "$(item.bogus)" }, "no item field"},
		{"item without forEach", func(u *v1alpha1.UseCase) { u.Spec.Steps[0].With["name"] = "$(item.name)" }, "only valid in a forEach"},
		{"item in when", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].When = `$(item.name) != ""` }, "not allowed in a when"},
		{"negative maxItems", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].MaxItems = -1 }, "maxItems"},
		{"forEach two refs", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].ForEach = "$(steps.crashing.pods)$(inputs.namespace)" }, "exactly one"},
	}
	for _, tc := range cases {
		uc := foreachUseCase()
		tc.mutate(uc)
		errs := validateFE(uc)
		if len(errs) == 0 {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		if !strings.Contains(strings.Join(errs, "; "), tc.wantSub) {
			t.Errorf("%s: %v does not contain %q", tc.name, errs, tc.wantSub)
		}
	}
}

func TestValidateForEachRequiresWith(t *testing.T) {
	uc := foreachUseCase()
	uc.Spec.Steps[1].With = nil // forEach step with no with
	errs := validateFE(uc)
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, "; "), "must declare with") {
		t.Fatalf("expected 'must declare with' error, got %v", errs)
	}
}
