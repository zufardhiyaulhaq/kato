package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gopaytech/kato/api/v1alpha1"
	"github.com/gopaytech/kato/internal/methods"
)

func uc(inputs ...v1alpha1.InputDecl) *v1alpha1.UseCase {
	return &v1alpha1.UseCase{Spec: v1alpha1.UseCaseSpec{Inputs: inputs}}
}

// Caller-supplied value always wins over a default.
func TestResolveInputs_CallerValueWins(t *testing.T) {
	got, err := resolveInputs(
		uc(v1alpha1.InputDecl{Name: "tls", Default: "false"}),
		map[string]string{"tls": "true"},
	)
	if err != nil {
		t.Fatalf("resolveInputs: %v", err)
	}
	if got["tls"] != "true" {
		t.Errorf("tls = %q, want %q", got["tls"], "true")
	}
}

// An omitted input with a default is filled in.
func TestResolveInputs_DefaultFillsOmission(t *testing.T) {
	got, err := resolveInputs(
		uc(v1alpha1.InputDecl{Name: "tls", Default: "false"}),
		map[string]string{},
	)
	if err != nil {
		t.Fatalf("resolveInputs: %v", err)
	}
	if got["tls"] != "false" {
		t.Errorf("tls = %q, want default %q", got["tls"], "false")
	}
}

// A default satisfies required: omitting a required-with-default is not an error.
func TestResolveInputs_DefaultSatisfiesRequired(t *testing.T) {
	got, err := resolveInputs(
		uc(v1alpha1.InputDecl{Name: "tls", Required: true, Default: "false"}),
		map[string]string{},
	)
	if err != nil {
		t.Fatalf("required+default omitted must not error: %v", err)
	}
	if got["tls"] != "false" {
		t.Errorf("tls = %q, want %q", got["tls"], "false")
	}
}

// Required with no default still errors when omitted (regression guard).
func TestResolveInputs_RequiredNoDefaultErrors(t *testing.T) {
	_, err := resolveInputs(
		uc(v1alpha1.InputDecl{Name: "pod", Required: true}),
		map[string]string{},
	)
	var ie *InputError
	if !errors.As(err, &ie) {
		t.Fatalf("want *InputError, got %v", err)
	}
}

// Not required, no default, omitted: stays absent (no key materialized).
func TestResolveInputs_OptionalOmittedStaysAbsent(t *testing.T) {
	got, err := resolveInputs(
		uc(v1alpha1.InputDecl{Name: "opt"}),
		map[string]string{},
	)
	if err != nil {
		t.Fatalf("resolveInputs: %v", err)
	}
	if _, ok := got["opt"]; ok {
		t.Errorf("opt should be absent, got %q", got["opt"])
	}
}

// An unknown caller key is still rejected.
func TestResolveInputs_UnknownInputRejected(t *testing.T) {
	_, err := resolveInputs(
		uc(v1alpha1.InputDecl{Name: "pod"}),
		map[string]string{"nope": "x"},
	)
	var ie *InputError
	if !errors.As(err, &ie) {
		t.Fatalf("want *InputError for unknown input, got %v", err)
	}
}

// A nil caller map works: defaults are written into a fresh map.
func TestResolveInputs_NilGivenMap(t *testing.T) {
	got, err := resolveInputs(
		uc(v1alpha1.InputDecl{Name: "tls", Default: "false"}),
		nil,
	)
	if err != nil {
		t.Fatalf("resolveInputs: %v", err)
	}
	if got["tls"] != "false" {
		t.Errorf("tls = %q, want %q", got["tls"], "false")
	}
}

// A caller-supplied empty string is a provided value, not an omission: it wins
// over the default and satisfies required (presence-based, matching the pre-
// feature validateInputs). "Empty = no default" constrains the Default field,
// not caller values.
func TestResolveInputs_CallerEmptyStringWinsOverDefault(t *testing.T) {
	got, err := resolveInputs(
		uc(v1alpha1.InputDecl{Name: "svc", Required: true, Default: "cart"}),
		map[string]string{"svc": ""},
	)
	if err != nil {
		t.Fatalf("caller-provided empty value must satisfy required: %v", err)
	}
	if v, ok := got["svc"]; !ok || v != "" {
		t.Errorf("svc = %q (present=%v), want empty caller value to win over default", v, ok)
	}
}

// recordingProber implements methods.Prober by embedding it (nil) and overriding
// only ProbeDNS, which records the resolved request. methods.fakeProber lives in
// an internal _test.go file in package methods and is not importable here, so
// this is a local, minimal stand-in. Any probe call other than ProbeDNS would
// nil-panic, but TestExecute_DefaultReachesWithSubstitution only exercises DNS.
type recordingProber struct {
	methods.Prober
	gotDNS methods.DNSProbeRequest
}

func (r *recordingProber) ProbeDNS(_ context.Context, req methods.DNSProbeRequest) methods.DNSResult {
	r.gotDNS = req
	return methods.DNSResult{Resolved: true, Addresses: []string{"10.0.0.1"}}
}

// End-to-end proof: an input omitted by the caller is filled from its Default by
// resolveInputs, then flows through $(inputs.host) substitution in a step's With
// and drives the real probe_dns method via methods.Builtin().
func TestExecute_DefaultReachesWithSubstitution(t *testing.T) {
	reg := methods.Builtin()
	rp := &recordingProber{}
	e := &Engine{Deps: methods.Deps{Prober: rp}, Registry: reg, StepTimeout: time.Second}

	u := &v1alpha1.UseCase{Spec: v1alpha1.UseCaseSpec{
		Inputs: []v1alpha1.InputDecl{{Name: "host", Default: "kubernetes.default"}},
		Steps: []v1alpha1.Step{{
			Name:   "dns",
			Method: "probe_dns",
			With:   map[string]string{"name": "$(inputs.host)"},
		}},
		Summary: v1alpha1.SummarySpec{Prompt: "x"},
	}}

	res, err := e.Execute(context.Background(), u, map[string]string{}) // omit host
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Steps) != 1 || res.Steps[0].Outcome != OutcomeCompleted {
		t.Fatalf("step outcome = %+v, want completed", res.Steps)
	}
	if rp.gotDNS.Name != "kubernetes.default" {
		t.Errorf("probe_dns called with name %q, want default %q", rp.gotDNS.Name, "kubernetes.default")
	}
}
