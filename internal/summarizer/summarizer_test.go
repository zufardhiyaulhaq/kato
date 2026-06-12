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
