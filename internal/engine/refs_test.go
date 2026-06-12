package engine

import (
	"testing"
)

func TestExtractRefs(t *testing.T) {
	refs, err := ExtractRefs(`$(inputs.namespace) and $(steps.previous-logs.logs)`)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("got %d refs, want 2", len(refs))
	}
	if refs[0].Kind != "inputs" || refs[0].Field != "namespace" {
		t.Errorf("ref0 = %+v", refs[0])
	}
	if refs[1].Kind != "steps" || refs[1].Step != "previous-logs" || refs[1].Field != "logs" {
		t.Errorf("ref1 = %+v", refs[1])
	}
}

func TestExtractRefsRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"$(bogus)", "$(steps.only-step)", "$(other.x.y)", "$()"} {
		if _, err := ExtractRefs(bad); err == nil {
			t.Errorf("ExtractRefs(%q): expected error", bad)
		}
	}
}

func TestCELVarSanitizesHyphens(t *testing.T) {
	refs, _ := ExtractRefs("$(steps.previous-logs.restartCount)")
	if got := refs[0].CELVar(); got != "steps_previous_logs__restartCount" {
		t.Errorf("CELVar = %q", got)
	}
	refs, _ = ExtractRefs("$(inputs.namespace)")
	if got := refs[0].CELVar(); got != "inputs_namespace" {
		t.Errorf("CELVar = %q", got)
	}
}

func TestSubstitute(t *testing.T) {
	got, err := Substitute("pod $(inputs.pod) on $(steps.status.nodeName)", func(r Ref) (string, bool) {
		switch r.Raw {
		case "inputs.pod":
			return "app-1", true
		case "steps.status.nodeName":
			return "node-7", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got != "pod app-1 on node-7" {
		t.Errorf("got %q", got)
	}
}

func TestSubstituteUnknownRefErrors(t *testing.T) {
	if _, err := Substitute("$(inputs.nope)", func(Ref) (string, bool) { return "", false }); err == nil {
		t.Fatal("expected error for unresolvable ref")
	}
}

func TestExtractItemRef(t *testing.T) {
	refs, err := ExtractRefs(`pod $(item.name) in $(item.namespace)`)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("got %d refs", len(refs))
	}
	if refs[0].Kind != "item" || refs[0].Field != "name" || refs[0].Step != "" {
		t.Errorf("ref0 = %+v", refs[0])
	}
	if refs[1].Kind != "item" || refs[1].Field != "namespace" {
		t.Errorf("ref1 = %+v", refs[1])
	}
}

func TestItemRefRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"$(item)", "$(item.)", "$(item.a.b)"} {
		if _, err := ExtractRefs(bad); err == nil {
			t.Errorf("ExtractRefs(%q): expected error", bad)
		}
	}
}

func TestSubstituteItemRef(t *testing.T) {
	got, err := Substitute("ns=$(item.namespace) name=$(item.name)", func(r Ref) (string, bool) {
		switch {
		case r.Kind == "item" && r.Field == "namespace":
			return "kube-system", true
		case r.Kind == "item" && r.Field == "name":
			return "nld-abc", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got != "ns=kube-system name=nld-abc" {
		t.Errorf("got %q", got)
	}
}
