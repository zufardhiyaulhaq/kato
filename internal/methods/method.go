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

// ListOutputField declares a list output: a named list whose items are records
// of typed fields. Lists are consumable only by a UseCase `forEach` step, never
// by a `when` condition (spec: matching stays scalar-only).
type ListOutputField struct {
	Name        string
	ItemFields  []OutputField
	Description string
}

// ListProducer is the optional interface a method implements when it returns
// one or more list outputs. Methods without lists simply do not implement it.
type ListProducer interface {
	ListOutputs() []ListOutputField
}

// ListOutputsOf returns m's declared list outputs, or nil if it has none.
func ListOutputsOf(m Method) []ListOutputField {
	if lp, ok := m.(ListProducer); ok {
		return lp.ListOutputs()
	}
	return nil
}

// Outputs values are string, int64, or bool per the declared OutputField type,
// or []map[string]any for a declared ListOutputField (a list of item records).
type Outputs map[string]any

// Deps holds the clients a method may use. Kube is always set. Metrics is set
// only when a metrics-server client is configured; methods that need it must
// handle a nil Metrics gracefully (report metrics as unavailable, not error).
// Namespace is kato's own namespace — the default location for Secrets a method
// reads (e.g. DB credentials for read_only_check_postgresql) when the caller
// omits secretNamespace. kato's ClusterRole grants cluster-wide `get` on Secrets,
// so a check may point at a Secret in any namespace.
type Deps struct {
	Kube      kubernetes.Interface
	Metrics   metricsv.Interface
	Prober    Prober
	Namespace string
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
