package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
)

func mc(name string, def bool) *v1alpha1.ModelConfig {
	return &v1alpha1.ModelConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.ModelConfigSpec{Default: def, BaseURL: "http://x/v1", Model: "m", Temperature: "0"},
	}
}

func TestResolveExplicitRef(t *testing.T) {
	c := NewModelConfigCache()
	c.Set(mc("ollama", false))
	c.Set(mc("openai", true))
	uc := &v1alpha1.UseCase{Spec: v1alpha1.UseCaseSpec{Summary: v1alpha1.SummarySpec{ModelConfigRef: "ollama"}}}
	got, name, err := c.Resolve(uc)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if name != "ollama" || got == nil {
		t.Errorf("got %q", name)
	}
}

func TestResolveDefault(t *testing.T) {
	c := NewModelConfigCache()
	c.Set(mc("ollama", false))
	c.Set(mc("openai", true))
	uc := &v1alpha1.UseCase{} // no ref
	_, name, err := c.Resolve(uc)
	if err != nil || name != "openai" {
		t.Errorf("name=%q err=%v", name, err)
	}
}

func TestResolveMultipleDefaultsPicksLexicographic(t *testing.T) {
	c := NewModelConfigCache()
	c.Set(mc("zzz", true))
	c.Set(mc("aaa", true))
	_, name, err := c.Resolve(&v1alpha1.UseCase{})
	if err != nil || name != "aaa" {
		t.Errorf("name=%q err=%v (expected lexicographically-first default)", name, err)
	}
}

func TestResolveNoDefaultErrors(t *testing.T) {
	c := NewModelConfigCache()
	c.Set(mc("ollama", false))
	if _, _, err := c.Resolve(&v1alpha1.UseCase{}); err == nil {
		t.Fatal("expected error when no default and no ref")
	}
}

func TestResolveTemperatureParsed(t *testing.T) {
	c := NewModelConfigCache()
	m := mc("openai", true)
	m.Spec.Temperature = "0.7"
	m.Spec.MaxTokens = 1234
	c.Set(m)
	completer, _, err := c.Resolve(&v1alpha1.UseCase{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	oc, ok := completer.(*summarizerClient)
	if !ok {
		t.Fatalf("unexpected completer type %T", completer)
	}
	if oc.client.Temperature != 0.7 || oc.client.MaxTokens != 1234 {
		t.Errorf("temperature=%v maxTokens=%v", oc.client.Temperature, oc.client.MaxTokens)
	}
}
