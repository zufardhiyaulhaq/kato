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
	// stepLists[step][listName][itemField] = type
	stepLists := map[string]map[string]map[string]methods.FieldType{}
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
			continue
		}

		isForEach := step.ForEach != ""
		// itemFields are the fields $(item.X) may reference inside this step.
		var itemFields map[string]methods.FieldType

		if isForEach {
			if step.MaxItems < 0 {
				addf("%s: maxItems must be >= 0", where)
			}
			if len(step.With) == 0 {
				addf("%s: a forEach step must declare with (to bind $(item.<field>) into the method)", where)
			}
			refs, err := ExtractRefs(step.ForEach)
			if err != nil {
				addf("%s forEach: %v", where, err)
			} else if len(refs) != 1 || refs[0].Kind != "steps" {
				addf("%s forEach: must be exactly one $(steps.<step>.<listOutput>) reference", where)
			} else {
				r := refs[0]
				lists, known := stepLists[r.Step]
				if !known {
					addf("%s forEach: step %q is unknown or not before this step", where, r.Step)
				} else if fields, isList := lists[r.Field]; !isList {
					addf("%s forEach: $(steps.%s.%s) is not a list output", where, r.Step, r.Field)
				} else {
					itemFields = fields
				}
			}
		}

		if err := methods.ValidateParams(m, step.With); err != nil {
			addf("%s: %v", where, err)
		}

		// with-value references.
		for param, val := range step.With {
			refs, err := ExtractRefs(val)
			if err != nil {
				addf("%s with.%s: %v", where, param, err)
				continue
			}
			for _, r := range refs {
				switch r.Kind {
				case "item":
					if !isForEach {
						addf("%s with.%s: $(item.%s) is only valid in a forEach step", where, param, r.Field)
					} else if itemFields != nil {
						if _, ok := itemFields[r.Field]; !ok {
							valid := make([]string, 0, len(itemFields))
							for f := range itemFields {
								valid = append(valid, f)
							}
							addf("%s with.%s: list has no item field %q (valid: %s)", where, param, r.Field, strings.Join(valid, ", "))
						}
					}
				default:
					if _, err := scope.typeOf(r); err != nil {
						addf("%s with.%s: %v", where, param, err)
					}
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

		// Register outputs for LATER steps. A forEach step exposes nothing
		// referenceable (its results are per-item, not aggregated).
		if !isForEach {
			fields := map[string]methods.FieldType{}
			for _, of := range m.OutputFields() {
				fields[of.Name] = of.Type
			}
			scope.StepOutputs[step.Name] = fields

			lm := map[string]map[string]methods.FieldType{}
			for _, lo := range methods.ListOutputsOf(m) {
				fm := map[string]methods.FieldType{}
				for _, itf := range lo.ItemFields {
					fm[itf.Name] = itf.Type
				}
				lm[lo.Name] = fm
			}
			stepLists[step.Name] = lm
		}
	}

	if ref := uc.Spec.Summary.ModelConfigRef; ref != "" && !modelConfigExists(ref) {
		addf("summary.modelConfigRef: ModelConfig %q not found", ref)
	}
	return errs
}
