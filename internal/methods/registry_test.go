package methods

import (
	"context"
	"testing"
)

type fakeMethod struct{ name string }

func (f fakeMethod) Name() string                { return f.name }
func (f fakeMethod) Description() string         { return "fake" }
func (f fakeMethod) Params() []Param             { return []Param{{Name: "namespace", Required: true}} }
func (f fakeMethod) OutputFields() []OutputField { return []OutputField{{Name: "ok", Type: FieldBool}} }
func (f fakeMethod) Run(_ context.Context, _ Deps, _ map[string]string) (Outputs, error) {
	return Outputs{"ok": true}, nil
}

func TestRegistryGetAndAll(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeMethod{name: "b_method"})
	r.Register(fakeMethod{name: "a_method"})

	if _, ok := r.Get("a_method"); !ok {
		t.Fatal("a_method not found")
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("missing should not be found")
	}
	all := r.All()
	if len(all) != 2 || all[0].Name() != "a_method" || all[1].Name() != "b_method" {
		t.Fatalf("All() not sorted by name: %v", all)
	}
}

func TestRegistryDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	r := NewRegistry()
	r.Register(fakeMethod{name: "dup"})
	r.Register(fakeMethod{name: "dup"})
}

func TestValidateParams(t *testing.T) {
	m := fakeMethod{name: "m"}
	if err := ValidateParams(m, map[string]string{}); err == nil {
		t.Fatal("missing required param should error")
	}
	if err := ValidateParams(m, map[string]string{"namespace": "x", "bogus": "y"}); err == nil {
		t.Fatal("unknown param should error")
	}
	if err := ValidateParams(m, map[string]string{"namespace": "x"}); err != nil {
		t.Fatalf("valid params errored: %v", err)
	}
}

type listy struct{}

func (listy) Name() string                { return "listy" }
func (listy) Description() string         { return "has a list output" }
func (listy) Params() []Param             { return nil }
func (listy) OutputFields() []OutputField { return []OutputField{{Name: "count", Type: FieldInt}} }
func (listy) ListOutputs() []ListOutputField {
	return []ListOutputField{{
		Name: "items",
		ItemFields: []OutputField{
			{Name: "name", Type: FieldString},
			{Name: "n", Type: FieldInt},
		},
	}}
}
func (listy) Run(context.Context, Deps, map[string]string) (Outputs, error) { return nil, nil }

type plain struct{}

func (plain) Name() string                                                  { return "plain" }
func (plain) Description() string                                           { return "" }
func (plain) Params() []Param                                               { return nil }
func (plain) OutputFields() []OutputField                                   { return nil }
func (plain) Run(context.Context, Deps, map[string]string) (Outputs, error) { return nil, nil }

func TestListOutputsOf(t *testing.T) {
	got := ListOutputsOf(listy{})
	if len(got) != 1 || got[0].Name != "items" {
		t.Fatalf("listy list outputs = %v", got)
	}
	if got[0].ItemFields[1].Name != "n" || got[0].ItemFields[1].Type != FieldInt {
		t.Errorf("item field types wrong: %v", got[0].ItemFields)
	}
	// A method that does not implement ListProducer returns nil.
	if got := ListOutputsOf(plain{}); got != nil {
		t.Errorf("plain should have no list outputs, got %v", got)
	}
}
