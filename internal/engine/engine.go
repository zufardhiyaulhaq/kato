package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

const (
	OutcomeCompleted = "completed"
	OutcomeSkipped   = "skipped"
	OutcomeFailed    = "failed"

	PhaseSucceeded          = "Succeeded"
	PhasePartiallySucceeded = "PartiallySucceeded"
	PhaseFailed             = "Failed"
)

type StepResult struct {
	Name    string
	Outcome string // completed | skipped | failed
	Reason  string // why skipped/failed
	Outputs methods.Outputs
	Error   string
}

type Result struct {
	Phase       string
	Steps       []StepResult
	Summary     string
	Warning     string // set when summary could not be produced
	ModelConfig string
}

// SummarizeFn produces (summary, modelConfigName, error) from completed step
// results. The summarizer applies each step's summaryFilter itself.
type SummarizeFn func(ctx context.Context, uc *v1alpha1.UseCase, steps []StepResult) (string, string, error)

type Engine struct {
	Deps        methods.Deps
	Registry    *methods.Registry
	Summarize   SummarizeFn
	StepTimeout time.Duration
}

// Execute runs the flow per spec §6. The returned error is ONLY for invalid
// inputs (callers map it to HTTP 400); step failures live in Result.
func (e *Engine) Execute(ctx context.Context, uc *v1alpha1.UseCase, inputs map[string]string) (Result, error) {
	if err := validateInputs(uc, inputs); err != nil {
		return Result{}, err
	}

	res := Result{}
	state := map[string]*StepResult{}

	for _, step := range uc.Spec.Steps {
		sr := e.runStep(ctx, uc, step, inputs, state)
		state[step.Name] = &sr
		res.Steps = append(res.Steps, sr)
	}

	res.Phase = phaseOf(res.Steps)

	if e.Summarize == nil {
		res.Warning = "no summarizer configured"
		return res, nil
	}
	summary, model, err := e.Summarize(ctx, uc, res.Steps)
	if err != nil {
		// Spec §6.6: deterministic value never depends on AI availability.
		res.Warning = fmt.Sprintf("summary unavailable: %v", err)
		return res, nil
	}
	res.Summary, res.ModelConfig = summary, model
	return res, nil
}

func (e *Engine) runStep(ctx context.Context, uc *v1alpha1.UseCase, step v1alpha1.Step,
	inputs map[string]string, state map[string]*StepResult) StepResult {

	sr := StepResult{Name: step.Name}

	m, ok := e.Registry.Get(step.Method)
	if !ok { // unreachable for Ready UseCases; defensive for direct callers
		sr.Outcome, sr.Error = OutcomeFailed, fmt.Sprintf("unknown method %q", step.Method)
		return sr
	}

	// Auto-skip if any referenced step did not complete (spec §6.3).
	for _, raw := range append([]string{step.When}, mapValues(step.With)...) {
		refs, _ := ExtractRefs(raw)
		for _, r := range refs {
			if r.Kind != "steps" {
				continue
			}
			if dep := state[r.Step]; dep == nil || dep.Outcome != OutcomeCompleted {
				sr.Outcome = OutcomeSkipped
				sr.Reason = fmt.Sprintf("depends on step %q which did not complete", r.Step)
				return sr
			}
		}
	}

	lookupAny := func(r Ref) (any, bool) {
		if r.Kind == "inputs" {
			v, ok := inputs[r.Field]
			return v, ok
		}
		dep := state[r.Step]
		if dep == nil || dep.Outputs == nil {
			return nil, false
		}
		v, ok := dep.Outputs[r.Field]
		return v, ok
	}

	if step.When != "" {
		scope := scopeBefore(uc, step.Name, e.Registry)
		w, err := CompileWhen(step.When, scope)
		if err != nil { // unreachable for Ready UseCases
			sr.Outcome, sr.Error = OutcomeFailed, fmt.Sprintf("when: %v", err)
			return sr
		}
		match, err := w.Eval(lookupAny)
		if err != nil {
			sr.Outcome, sr.Error = OutcomeFailed, fmt.Sprintf("when: %v", err)
			return sr
		}
		if !match {
			sr.Outcome, sr.Reason = OutcomeSkipped, "when evaluated to false"
			return sr
		}
	}

	params := map[string]string{}
	for k, v := range step.With {
		resolved, err := Substitute(v, func(r Ref) (string, bool) {
			av, ok := lookupAny(r)
			if !ok {
				return "", false
			}
			return fmt.Sprintf("%v", av), true
		})
		if err != nil {
			sr.Outcome, sr.Error = OutcomeFailed, fmt.Sprintf("with.%s: %v", k, err)
			return sr
		}
		params[k] = resolved
	}
	if err := methods.ValidateParams(m, params); err != nil {
		sr.Outcome, sr.Error = OutcomeFailed, err.Error()
		return sr
	}

	stepCtx, cancel := context.WithTimeout(ctx, e.StepTimeout)
	defer cancel()
	outputs, err := m.Run(stepCtx, e.Deps, params)
	if err != nil {
		// Failure is itself a finding (spec §6.3); recorded, flow continues.
		sr.Outcome, sr.Error = OutcomeFailed, err.Error()
		return sr
	}
	sr.Outcome, sr.Outputs = OutcomeCompleted, outputs
	return sr
}

// InputError marks invalid caller inputs (mapped to HTTP 400 by the server).
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

func validateInputs(uc *v1alpha1.UseCase, given map[string]string) error {
	declared := map[string]bool{}
	for _, in := range uc.Spec.Inputs {
		declared[in.Name] = true
		if in.Required {
			if _, ok := given[in.Name]; !ok {
				return &InputError{Msg: fmt.Sprintf("missing required input %q", in.Name)}
			}
		}
	}
	for name := range given {
		if !declared[name] {
			return &InputError{Msg: fmt.Sprintf("unknown input %q", name)}
		}
	}
	return nil
}

// phaseOf: all-failed (or skipped-due-to-failure) => Failed; any failure with
// at least one completed => PartiallySucceeded; otherwise Succeeded.
func phaseOf(steps []StepResult) string {
	completed, failed := 0, 0
	for _, s := range steps {
		switch s.Outcome {
		case OutcomeCompleted:
			completed++
		case OutcomeFailed:
			failed++
		}
	}
	switch {
	case failed > 0 && completed == 0:
		return PhaseFailed
	case failed > 0:
		return PhasePartiallySucceeded
	default:
		return PhaseSucceeded
	}
}

// scopeBefore builds the typed scope visible to the named step.
func scopeBefore(uc *v1alpha1.UseCase, stepName string, reg *methods.Registry) Scope {
	scope := Scope{StepOutputs: map[string]map[string]methods.FieldType{}}
	for _, in := range uc.Spec.Inputs {
		scope.InputNames = append(scope.InputNames, in.Name)
	}
	for _, s := range uc.Spec.Steps {
		if s.Name == stepName {
			break
		}
		if m, ok := reg.Get(s.Method); ok {
			fields := map[string]methods.FieldType{}
			for _, of := range m.OutputFields() {
				fields[of.Name] = of.Type
			}
			scope.StepOutputs[s.Name] = fields
		}
	}
	return scope
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
