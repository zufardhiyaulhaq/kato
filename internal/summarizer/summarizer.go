// Package summarizer turns collected step outputs into a single LLM call that
// produces a human-readable diagnosis (spec §8). The LLM has no tools and no
// cluster access; it only reads the (summaryFilter-filtered) outputs.
package summarizer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/engine"
)

const systemPrompt = `You are a Kubernetes troubleshooting assistant. You are given evidence collected by a deterministic set of checks. Diagnose the problem based ONLY on this evidence. Cite the specific step evidence that supports each claim. If the evidence is insufficient, say the diagnosis is inconclusive rather than guessing. Do not invent data that is not present.`

// Completer is the minimal LLM capability the summarizer needs.
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// BuildEvidence renders the user message: per step, its outcome and the
// summaryFilter-selected outputs. nil filter = all outputs; empty = excluded.
func BuildEvidence(uc *v1alpha1.UseCase, steps []engine.StepResult) string {
	filters := map[string][]string{}
	excluded := map[string]bool{}
	for _, s := range uc.Spec.Steps {
		if s.SummaryFilter != nil && len(s.SummaryFilter) == 0 {
			excluded[s.Name] = true
		} else if s.SummaryFilter != nil {
			filters[s.Name] = s.SummaryFilter
		}
	}

	var b strings.Builder
	for _, sr := range steps {
		if excluded[sr.Name] {
			continue
		}
		fmt.Fprintf(&b, "## step %q (%s)\n", sr.Name, sr.Outcome)
		if sr.Error != "" {
			fmt.Fprintf(&b, "error: %s\n", sr.Error)
		}
		if sr.Reason != "" {
			fmt.Fprintf(&b, "reason: %s\n", sr.Reason)
		}
		if len(sr.Outputs) > 0 {
			var shown map[string]any = sr.Outputs
			if allow, ok := filters[sr.Name]; ok {
				shown = map[string]any{}
				for _, f := range allow {
					if v, present := sr.Outputs[f]; present {
						shown[f] = v
					}
				}
			}
			j, _ := json.MarshalIndent(shown, "", "  ")
			b.Write(j)
			b.WriteByte('\n')
		}
		if len(sr.Iterations) > 0 {
			if sr.Note != "" {
				fmt.Fprintf(&b, "note: %s\n", sr.Note)
			}
			allow, hasFilter := filters[sr.Name]
			for _, it := range sr.Iterations {
				fmt.Fprintf(&b, "- item %v (%s)\n", it.Item, it.Outcome)
				if it.Error != "" {
					fmt.Fprintf(&b, "  error: %s\n", it.Error)
				}
				if len(it.Outputs) > 0 {
					shown := map[string]any(it.Outputs)
					if hasFilter {
						shown = map[string]any{}
						for _, f := range allow {
							if v, present := it.Outputs[f]; present {
								shown[f] = v
							}
						}
					}
					j, _ := json.MarshalIndent(shown, "  ", "  ")
					b.WriteString("  ")
					b.Write(j)
					b.WriteByte('\n')
				}
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Summarizer adapts to engine.SummarizeFn. Resolve returns the Completer and
// model name for a UseCase (wired in cmd/kato from ModelConfig CRs).
type Summarizer struct {
	Resolve func(uc *v1alpha1.UseCase) (Completer, string, error)
}

func (s *Summarizer) Summarize(ctx context.Context, uc *v1alpha1.UseCase, steps []engine.StepResult) (string, string, error) {
	completer, model, err := s.Resolve(uc)
	if err != nil {
		return "", "", err
	}
	user := "Use case: " + uc.Spec.Summary.Prompt + "\n\nEvidence:\n" + BuildEvidence(uc, steps)
	out, err := completer.Complete(ctx, systemPrompt, user)
	if err != nil {
		return "", "", err
	}
	return out, model, nil
}
