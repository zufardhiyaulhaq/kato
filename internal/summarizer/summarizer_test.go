package summarizer

import (
	"strings"
	"testing"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/engine"
	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

func TestBuildEvidenceAppliesSummaryFilter(t *testing.T) {
	uc := &v1alpha1.UseCase{
		Spec: v1alpha1.UseCaseSpec{
			Steps: []v1alpha1.Step{
				{Name: "status", Method: "check_pod_status",
					SummaryFilter: []string{"phase", "restartCount"}}, // only these two
				{Name: "secret-step", Method: "check_pod_logs",
					SummaryFilter: []string{}}, // empty -> excluded entirely
				{Name: "events", Method: "check_events"}, // nil -> all outputs
			},
			Summary: v1alpha1.SummarySpec{Prompt: "diagnose the crash"},
		},
	}
	steps := []engine.StepResult{
		{Name: "status", Outcome: "completed", Outputs: methods.Outputs{
			"phase": "Running", "restartCount": int64(17), "nodeName": "node-7"}},
		{Name: "secret-step", Outcome: "completed", Outputs: methods.Outputs{"logs": "SENSITIVE"}},
		{Name: "events", Outcome: "completed", Outputs: methods.Outputs{"count": int64(3)}},
	}

	ev := BuildEvidence(uc, steps)
	if !strings.Contains(ev, "phase") || !strings.Contains(ev, "Running") {
		t.Error("filtered-in field missing")
	}
	if strings.Contains(ev, "nodeName") || strings.Contains(ev, "node-7") {
		t.Error("field not in summaryFilter leaked")
	}
	if strings.Contains(ev, "SENSITIVE") || strings.Contains(ev, "secret-step") {
		t.Error("empty summaryFilter step leaked")
	}
	if !strings.Contains(ev, "count") {
		t.Error("nil summaryFilter step should include all outputs")
	}
}

func TestBuildEvidenceIncludesOutcomes(t *testing.T) {
	uc := &v1alpha1.UseCase{Spec: v1alpha1.UseCaseSpec{
		Steps:   []v1alpha1.Step{{Name: "node", Method: "check_node_status"}},
		Summary: v1alpha1.SummarySpec{Prompt: "x"},
	}}
	steps := []engine.StepResult{{Name: "node", Outcome: "failed", Error: "nodes \"node-7\" not found"}}
	ev := BuildEvidence(uc, steps)
	if !strings.Contains(ev, "failed") || !strings.Contains(ev, "not found") {
		t.Errorf("failed step evidence missing: %q", ev)
	}
}

func TestBuildEvidenceRendersIterations(t *testing.T) {
	uc := &v1alpha1.UseCase{Spec: v1alpha1.UseCaseSpec{
		Steps: []v1alpha1.Step{
			{Name: "check", Method: "check_pod_status", ForEach: "$(steps.crashing.pods)",
				SummaryFilter: []string{"restartCount"}}, // only restartCount per iteration
		},
		Summary: v1alpha1.SummarySpec{Prompt: "x"},
	}}
	steps := []engine.StepResult{{
		Name: "check", Outcome: "completed",
		Note: "matched 3, checked 2 (worst-first); 1 not examined",
		Iterations: []engine.IterationResult{
			{Item: map[string]string{"name": "b", "namespace": "kube-system"}, Outcome: "completed",
				Outputs: methods.Outputs{"restartCount": int64(9), "phase": "Running"}},
			{Item: map[string]string{"name": "a"}, Outcome: "failed", Error: "logs unavailable"},
		},
	}}
	ev := BuildEvidence(uc, steps)
	if !strings.Contains(ev, "not examined") {
		t.Error("note missing")
	}
	if !strings.Contains(ev, `"b"`) && !strings.Contains(ev, "b") {
		t.Error("item identity missing")
	}
	if !strings.Contains(ev, "restartCount") || !strings.Contains(ev, "9") {
		t.Error("filtered iteration output missing")
	}
	if strings.Contains(ev, "phase") { // filtered out by summaryFilter
		t.Error("non-filtered field leaked into iteration evidence")
	}
	if !strings.Contains(ev, "logs unavailable") {
		t.Error("failed iteration error missing")
	}
}
