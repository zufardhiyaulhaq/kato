package engine

import (
	"fmt"
	"strings"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

// ValidateUseCase performs the full watch-time validation from spec §4.
// Empty result = valid. Each string is one human-readable problem.
func ValidateUseCase(uc *v1alpha1.UseCase, reg *methods.Registry, modelConfigExists func(string) bool) []string {
	var errs []string
	addf := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	inputNames := make([]string, 0, len(uc.Spec.Inputs))
	for _, in := range uc.Spec.Inputs {
		inputNames = append(inputNames, in.Name)
	}

	scope := Scope{InputNames: inputNames, StepOutputs: map[string]map[string]methods.FieldType{}}
	seenSteps := map[string]bool{}
	seenSanitized := map[string]string{}

	for i, step := range uc.Spec.Steps {
		where := fmt.Sprintf("steps[%d] (%s)", i, step.Name)

		if seenSteps[step.Name] {
			addf("%s: duplicate step name %q", where, step.Name)
		}
		seenSteps[step.Name] = true
		sanitized := strings.ReplaceAll(step.Name, "-", "_")
		if prev, clash := seenSanitized[sanitized]; clash && prev != step.Name {
			addf("%s: name collides with step %q after hyphen sanitization", where, prev)
		}
		seenSanitized[sanitized] = step.Name

		m, ok := reg.Get(step.Method)
		if !ok {
			addf("%s: unknown method %q", where, step.Method)
			continue // no contract to check against
		}

		if err := methods.ValidateParams(m, step.With); err != nil {
			addf("%s: %v", where, err)
		}

		// with-value references: must resolve against inputs/prior steps.
		for param, val := range step.With {
			refs, err := ExtractRefs(val)
			if err != nil {
				addf("%s with.%s: %v", where, param, err)
				continue
			}
			for _, r := range refs {
				if _, err := scope.typeOf(r); err != nil {
					addf("%s with.%s: %v", where, param, err)
				}
			}
		}

		if step.When != "" {
			if _, err := CompileWhen(step.When, scope); err != nil {
				addf("%s when: %v", where, err)
			}
		}

		for _, f := range step.SummaryFilter {
			if _, ok := methods.OutputType(m, f); !ok {
				addf("%s: summaryFilter field %q not declared by method %q", where, f, step.Method)
			}
		}

		// This step's outputs become visible to LATER steps.
		fields := map[string]methods.FieldType{}
		for _, of := range m.OutputFields() {
			fields[of.Name] = of.Type
		}
		scope.StepOutputs[step.Name] = fields
	}

	if ref := uc.Spec.Summary.ModelConfigRef; ref != "" && !modelConfigExists(ref) {
		addf("summary.modelConfigRef: ModelConfig %q not found", ref)
	}
	return errs
}
