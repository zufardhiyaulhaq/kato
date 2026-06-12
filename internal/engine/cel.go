package engine

import (
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"

	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

// Scope is the typed environment a `when` expression compiles against:
// UseCase inputs (always string) and the declared outputs of PRIOR steps.
type Scope struct {
	InputNames  []string
	StepOutputs map[string]map[string]methods.FieldType
}

func (s Scope) typeOf(r Ref) (*cel.Type, error) {
	if r.Kind == "inputs" {
		for _, n := range s.InputNames {
			if n == r.Field {
				return cel.StringType, nil
			}
		}
		return nil, fmt.Errorf("unknown input %q in $(%s)", r.Field, r.Raw)
	}
	outputs, ok := s.StepOutputs[r.Step]
	if !ok {
		return nil, fmt.Errorf("$(%s): step %q is unknown or not before this step", r.Raw, r.Step)
	}
	ft, ok := outputs[r.Field]
	if !ok {
		valid := make([]string, 0, len(outputs))
		for f := range outputs {
			valid = append(valid, f)
		}
		return nil, fmt.Errorf("$(%s): step %q has no output %q (valid: %s)",
			r.Raw, r.Step, r.Field, strings.Join(valid, ", "))
	}
	switch ft {
	case methods.FieldInt:
		return cel.IntType, nil
	case methods.FieldBool:
		return cel.BoolType, nil
	default:
		return cel.StringType, nil
	}
}

// When is a compiled `when` expression.
type When struct {
	prog cel.Program
	refs []Ref
}

// CompileWhen rewrites $(...) refs to sanitized CEL variables, builds a typed
// CEL env from the scope, and compiles. Unknown refs, type errors, and
// non-boolean expressions fail HERE — at UseCase validation time, not mid-run.
func CompileWhen(expr string, scope Scope) (*When, error) {
	refs, err := ExtractRefs(expr)
	if err != nil {
		return nil, err
	}
	opts := []cel.EnvOption{}
	seen := map[string]bool{}
	rewritten := expr
	for _, r := range refs {
		t, err := scope.typeOf(r)
		if err != nil {
			return nil, err
		}
		if !seen[r.CELVar()] {
			opts = append(opts, cel.Variable(r.CELVar(), t))
			seen[r.CELVar()] = true
		}
		rewritten = strings.ReplaceAll(rewritten, "$("+r.Raw+")", r.CELVar())
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("cel env: %w", err)
	}
	ast, issues := env.Compile(rewritten)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("when %q: %w", expr, issues.Err())
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("when %q: must evaluate to bool, got %s", expr, ast.OutputType())
	}
	prog, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("when %q: %w", expr, err)
	}
	return &When{prog: prog, refs: refs}, nil
}

// Eval runs the compiled expression against actual values.
func (w *When) Eval(value func(Ref) (any, bool)) (bool, error) {
	activation := map[string]any{}
	for _, r := range w.refs {
		v, ok := value(r)
		if !ok {
			return false, fmt.Errorf("no value for $(%s)", r.Raw)
		}
		activation[r.CELVar()] = v
	}
	out, _, err := w.prog.Eval(activation)
	if err != nil {
		return false, err
	}
	return out == types.True, nil
}
