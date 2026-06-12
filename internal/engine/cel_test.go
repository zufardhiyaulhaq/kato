package engine

import (
	"testing"

	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

func testScope() Scope {
	return Scope{
		InputNames: []string{"namespace", "pod"},
		StepOutputs: map[string]map[string]methods.FieldType{
			"status": {
				"phase": methods.FieldString, "restartCount": methods.FieldInt,
				"ready": methods.FieldBool, "nodeName": methods.FieldString,
			},
			"previous-logs": {"logs": methods.FieldString},
		},
	}
}

func TestCompileAndEvalWhen(t *testing.T) {
	cases := []struct {
		expr   string
		values map[string]any
		want   bool
	}{
		{`$(steps.status.restartCount) > 0`, map[string]any{"steps.status.restartCount": int64(17)}, true},
		{`$(steps.status.restartCount) > 0`, map[string]any{"steps.status.restartCount": int64(0)}, false},
		{`$(steps.status.nodeName) != ""`, map[string]any{"steps.status.nodeName": ""}, false},
		{`$(steps.status.ready) && $(steps.status.phase) == "Running"`,
			map[string]any{"steps.status.ready": true, "steps.status.phase": "Running"}, true},
		{`$(steps.previous-logs.logs).contains("OOM")`,
			map[string]any{"steps.previous-logs.logs": "... OOMKilled ..."}, true},
		{`$(inputs.namespace) == "payments"`, map[string]any{"inputs.namespace": "payments"}, true},
	}
	for _, tc := range cases {
		w, err := CompileWhen(tc.expr, testScope())
		if err != nil {
			t.Errorf("CompileWhen(%q): %v", tc.expr, err)
			continue
		}
		got, err := w.Eval(func(r Ref) (any, bool) {
			v, ok := tc.values[r.Raw]
			return v, ok
		})
		if err != nil {
			t.Errorf("Eval(%q): %v", tc.expr, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Eval(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}
}

func TestCompileWhenRejects(t *testing.T) {
	cases := []struct{ name, expr string }{
		{"unknown field", `$(steps.status.restartCnt) > 0`},
		{"unknown step", `$(steps.nope.x) > 0`},
		{"type error", `$(steps.status.phase) > 3`},
		{"not boolean", `$(steps.status.restartCount) + 1`},
		{"unknown input", `$(inputs.bogus) == "x"`},
	}
	for _, tc := range cases {
		if _, err := CompileWhen(tc.expr, testScope()); err == nil {
			t.Errorf("%s: CompileWhen(%q) should fail", tc.name, tc.expr)
		}
	}
}
