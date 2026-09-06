package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/gopaytech/kato/api/v1alpha1"
	"github.com/gopaytech/kato/internal/methods"
)

const (
	OutcomeCompleted = "completed"
	OutcomeSkipped   = "skipped"
	OutcomeFailed    = "failed"

	PhaseRunning            = "Running"
	PhaseSucceeded          = "Succeeded"
	PhasePartiallySucceeded = "PartiallySucceeded"
	PhaseFailed             = "Failed"

	defaultMaxItems = 5
	maxItemsCeiling = 20
)

type IterationResult struct {
	Item    map[string]string
	Outcome string // completed | failed
	Outputs methods.Outputs
	Error   string
}

type StepResult struct {
	Name       string
	Outcome    string // completed | skipped | failed
	Reason     string // why skipped/failed
	Outputs    methods.Outputs
	Error      string
	Iterations []IterationResult // populated for forEach steps
	Note       string            // forEach truncation note
	// SummaryFilter is the step's spec.summaryFilter, carried so persistence can
	// limit the recorded Run outputs to the same fields exposed to the LLM. nil =
	// no filter (record all outputs); non-nil (incl. empty) = record only the
	// listed keys. Does not affect when/$(steps.x.y), which read full Outputs.
	SummaryFilter []string
}

type Result struct {
	Phase       string
	Steps       []StepResult
	Summary     string
	Healthy     *bool  // health verdict; nil = unknown. Advisory, never affects Phase.
	Headline    string // one-line reason accompanying Healthy; empty when unknown.
	Warning     string // set when summary could not be produced
	ModelConfig string
}

// SummaryOutput is what a summarizer returns: the prose plus the structured
// health verdict parsed from it.
type SummaryOutput struct {
	Summary     string
	Healthy     *bool
	Headline    string
	ModelConfig string
}

// SummarizeFn produces a SummaryOutput from completed step results. The
// summarizer applies each step's summaryFilter itself.
type SummarizeFn func(ctx context.Context, uc *v1alpha1.UseCase, steps []StepResult) (SummaryOutput, error)

type Engine struct {
	Deps        methods.Deps
	Registry    *methods.Registry
	Summarize   SummarizeFn
	StepTimeout time.Duration
}

// Execute runs the flow per spec §6. The returned error is ONLY for invalid
// inputs (callers map it to HTTP 400); step failures live in Result.
func (e *Engine) Execute(ctx context.Context, uc *v1alpha1.UseCase, inputs map[string]string) (Result, error) {
	inputs, err := resolveInputs(uc, inputs)
	if err != nil {
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
	out, err := e.Summarize(ctx, uc, res.Steps)
	if err != nil {
		// Spec §6.6: deterministic value never depends on AI availability.
		res.Warning = fmt.Sprintf("summary unavailable: %v", err)
		return res, nil
	}
	res.Summary = out.Summary
	res.Healthy = out.Healthy
	res.Headline = out.Headline
	res.ModelConfig = out.ModelConfig
	return res, nil
}

func (e *Engine) runStep(ctx context.Context, uc *v1alpha1.UseCase, step v1alpha1.Step,
	inputs map[string]string, state map[string]*StepResult) StepResult {

	if step.ForEach != "" {
		return e.runForEachStep(ctx, uc, step, inputs, state)
	}

	sr := StepResult{Name: step.Name, SummaryFilter: step.SummaryFilter}

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

func (e *Engine) runForEachStep(ctx context.Context, uc *v1alpha1.UseCase, step v1alpha1.Step,
	inputs map[string]string, state map[string]*StepResult) StepResult {

	sr := StepResult{Name: step.Name, SummaryFilter: step.SummaryFilter}

	m, ok := e.Registry.Get(step.Method)
	if !ok {
		sr.Outcome, sr.Error = OutcomeFailed, fmt.Sprintf("unknown method %q", step.Method)
		return sr
	}

	// Auto-skip if any referenced step (the forEach source, or a step named in
	// when/with) did not complete — same dependency rule as a normal step
	// (spec §6.2). Skipping, not failing, on an upstream non-completion.
	for _, raw := range append([]string{step.ForEach, step.When}, mapValues(step.With)...) {
		refs, _ := ExtractRefs(raw)
		for _, r := range refs {
			if r.Kind != "steps" {
				continue
			}
			if d := state[r.Step]; d == nil || d.Outcome != OutcomeCompleted {
				sr.Outcome = OutcomeSkipped
				sr.Reason = fmt.Sprintf("depends on step %q which did not complete", r.Step)
				return sr
			}
		}
	}

	refs, _ := ExtractRefs(step.ForEach)
	if len(refs) != 1 || refs[0].Kind != "steps" {
		sr.Outcome, sr.Error = OutcomeFailed, "invalid forEach reference"
		return sr
	}
	listRef := refs[0]
	dep := state[listRef.Step] // guaranteed completed by the dependency check above
	raw, ok := dep.Outputs[listRef.Field]
	if !ok {
		sr.Outcome, sr.Error = OutcomeFailed, fmt.Sprintf("step %q has no output %q", listRef.Step, listRef.Field)
		return sr
	}
	items, ok := raw.([]map[string]any)
	if !ok {
		sr.Outcome, sr.Error = OutcomeFailed, fmt.Sprintf("$(steps.%s.%s) is not a list output", listRef.Step, listRef.Field)
		return sr
	}

	if step.When != "" {
		scope := scopeBefore(uc, step.Name, e.Registry)
		w, err := CompileWhen(step.When, scope)
		if err != nil {
			sr.Outcome, sr.Error = OutcomeFailed, fmt.Sprintf("when: %v", err)
			return sr
		}
		match, err := w.Eval(func(r Ref) (any, bool) {
			if r.Kind == "inputs" {
				v, ok := inputs[r.Field]
				return v, ok
			}
			d := state[r.Step]
			if d == nil || d.Outputs == nil {
				return nil, false
			}
			v, ok := d.Outputs[r.Field]
			return v, ok
		})
		if err != nil {
			sr.Outcome, sr.Error = OutcomeFailed, fmt.Sprintf("when: %v", err)
			return sr
		}
		if !match {
			sr.Outcome, sr.Reason = OutcomeSkipped, "when evaluated to false"
			return sr
		}
	}

	if len(items) == 0 {
		sr.Outcome, sr.Reason = OutcomeSkipped, "no items matched"
		return sr
	}

	limit := step.MaxItems
	if limit == 0 {
		limit = defaultMaxItems
	}
	if limit > maxItemsCeiling {
		limit = maxItemsCeiling
	}
	n := limit
	if n > len(items) {
		n = len(items)
	}
	if n < len(items) {
		sr.Note = fmt.Sprintf("matched %d, checked %d (worst-first); %d not examined", len(items), n, len(items)-n)
	}

	for _, item := range items[:n] {
		sr.Iterations = append(sr.Iterations, e.runIteration(ctx, m, step, inputs, state, item))
	}
	// Surface the all-failed case at the step level so the run is readable
	// without drilling into every iteration (the step is still "completed":
	// each failed check is a recorded finding, spec §6.3).
	failed := 0
	for _, it := range sr.Iterations {
		if it.Outcome == OutcomeFailed {
			failed++
		}
	}
	if failed > 0 && failed == len(sr.Iterations) {
		if sr.Note != "" {
			sr.Note += "; "
		}
		sr.Note += "all iterations failed"
	}
	sr.Outcome = OutcomeCompleted
	return sr
}

func (e *Engine) runIteration(ctx context.Context, m methods.Method, step v1alpha1.Step,
	inputs map[string]string, state map[string]*StepResult, item map[string]any) IterationResult {

	itemStr := map[string]string{}
	for k, v := range item {
		itemStr[k] = fmt.Sprintf("%v", v)
	}
	ir := IterationResult{Item: itemStr}

	lookup := func(r Ref) (string, bool) {
		switch r.Kind {
		case "item":
			v, ok := item[r.Field]
			if !ok {
				return "", false
			}
			return fmt.Sprintf("%v", v), true
		case "inputs":
			v, ok := inputs[r.Field]
			return v, ok
		case "steps":
			d := state[r.Step]
			if d == nil || d.Outputs == nil {
				return "", false
			}
			v, ok := d.Outputs[r.Field]
			if !ok {
				return "", false
			}
			return fmt.Sprintf("%v", v), true
		}
		return "", false
	}

	params := map[string]string{}
	for k, v := range step.With {
		resolved, err := Substitute(v, lookup)
		if err != nil {
			ir.Outcome, ir.Error = OutcomeFailed, fmt.Sprintf("with.%s: %v", k, err)
			return ir
		}
		params[k] = resolved
	}
	if err := methods.ValidateParams(m, params); err != nil {
		ir.Outcome, ir.Error = OutcomeFailed, err.Error()
		return ir
	}

	stepCtx, cancel := context.WithTimeout(ctx, e.StepTimeout)
	defer cancel()
	outputs, err := m.Run(stepCtx, e.Deps, params)
	if err != nil {
		ir.Outcome, ir.Error = OutcomeFailed, err.Error()
		return ir
	}
	ir.Outcome, ir.Outputs = OutcomeCompleted, outputs
	return ir
}

// InputError marks invalid caller inputs (mapped to HTTP 400 by the server).
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// resolveInputs validates caller inputs against the UseCase declarations and
// returns the effective input map: caller values, with each omitted input filled
// from its Default when non-empty. The returned error is a non-nil *InputError
// only for an unknown input key, or a required input that has neither a caller
// value nor a default. A default satisfies required (empty default = none).
func resolveInputs(uc *v1alpha1.UseCase, given map[string]string) (map[string]string, error) {
	effective := map[string]string{}
	for name, v := range given {
		effective[name] = v
	}
	declared := map[string]bool{}
	for _, in := range uc.Spec.Inputs {
		declared[in.Name] = true
		if _, ok := effective[in.Name]; ok {
			continue // caller value wins
		}
		if in.Default != "" {
			effective[in.Name] = in.Default
			continue // default fills the omission (also satisfies Required)
		}
		if in.Required {
			return nil, &InputError{Msg: fmt.Sprintf("missing required input %q", in.Name)}
		}
	}
	// Iterating effective (not given) is safe: every default-injected key names a
	// declared input, so injected defaults can never trip this unknown-key check.
	for name := range effective {
		if !declared[name] {
			return nil, &InputError{Msg: fmt.Sprintf("unknown input %q", name)}
		}
	}
	return effective, nil
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
