// Package methods is kato's built-in library of read-only Kubernetes checks.
// A method declares typed params and a flat set of typed outputs; outputs are
// what conditions match, what the LLM reads, and what Runs record (spec §7).
package methods

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

type FieldType string

const (
	FieldString FieldType = "string"
	FieldInt    FieldType = "int"
	FieldBool   FieldType = "bool"
)

type Param struct {
	Name        string
	Required    bool
	Description string
}

type OutputField struct {
	Name        string
	Type        FieldType
	Description string
}

// Outputs values must be string, int64, or bool per the declared field type.
type Outputs map[string]any

// Deps holds the clients a method may use. Kube is always set. Metrics is set
// only when a metrics-server client is configured; methods that need it must
// handle a nil Metrics gracefully (report metrics as unavailable, not error).
type Deps struct {
	Kube    kubernetes.Interface
	Metrics metricsv.Interface
}

type Method interface {
	Name() string
	Description() string
	Params() []Param
	OutputFields() []OutputField
	Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error)
}

// ValidateParams checks given params against the method's declaration:
// all required present, no unknown names.
func ValidateParams(m Method, given map[string]string) error {
	declared := map[string]Param{}
	for _, p := range m.Params() {
		declared[p.Name] = p
		if p.Required {
			if _, ok := given[p.Name]; !ok {
				return fmt.Errorf("method %s: missing required param %q", m.Name(), p.Name)
			}
		}
	}
	for name := range given {
		if _, ok := declared[name]; !ok {
			return fmt.Errorf("method %s: unknown param %q", m.Name(), name)
		}
	}
	return nil
}

// OutputType returns the declared type of an output field.
func OutputType(m Method, field string) (FieldType, bool) {
	for _, f := range m.OutputFields() {
		if f.Name == field {
			return f.Type, true
		}
	}
	return "", false
}
