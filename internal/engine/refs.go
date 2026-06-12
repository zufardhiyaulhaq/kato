// Package engine executes UseCase flows: reference resolution, CEL `when`
// conditions, ordered step execution, and outcome semantics (spec §6).
package engine

import (
	"fmt"
	"regexp"
	"strings"
)

// refPattern matches $(...) occurrences; contents validated by parseRef.
var refPattern = regexp.MustCompile(`\$\(([^)]*)\)`)

// Ref is one $(...) reference: $(inputs.<field>) or $(steps.<step>.<field>).
type Ref struct {
	Raw   string // e.g. "steps.previous-logs.logs"
	Kind  string // "inputs" | "steps"
	Step  string // step name; empty for inputs
	Field string
}

// CELVar is the sanitized CEL identifier for this ref. Hyphens in step names
// become underscores; step and field are joined with "__".
func (r Ref) CELVar() string {
	if r.Kind == "inputs" {
		return "inputs_" + r.Field
	}
	return "steps_" + strings.ReplaceAll(r.Step, "-", "_") + "__" + r.Field
}

func parseRef(raw string) (Ref, error) {
	parts := strings.Split(raw, ".")
	switch {
	case len(parts) == 2 && parts[0] == "inputs" && parts[1] != "":
		return Ref{Raw: raw, Kind: "inputs", Field: parts[1]}, nil
	case len(parts) == 3 && parts[0] == "steps" && parts[1] != "" && parts[2] != "":
		return Ref{Raw: raw, Kind: "steps", Step: parts[1], Field: parts[2]}, nil
	default:
		return Ref{}, fmt.Errorf("invalid reference $(%s): want $(inputs.<name>) or $(steps.<step>.<field>)", raw)
	}
}

// ExtractRefs returns every $(...) reference in s, in order of appearance.
func ExtractRefs(s string) ([]Ref, error) {
	var refs []Ref
	for _, m := range refPattern.FindAllStringSubmatch(s, -1) {
		r, err := parseRef(m[1])
		if err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	return refs, nil
}

// Substitute replaces every $(...) in s with the value from lookup.
// An unresolvable ref is an error (callers validate refs beforehand;
// this guards engine bugs, not user mistakes).
func Substitute(s string, lookup func(Ref) (string, bool)) (string, error) {
	var firstErr error
	out := refPattern.ReplaceAllStringFunc(s, func(match string) string {
		raw := match[2 : len(match)-1]
		r, err := parseRef(raw)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return match
		}
		v, ok := lookup(r)
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("unresolvable reference $(%s)", raw)
			}
			return match
		}
		return v
	})
	return out, firstErr
}
