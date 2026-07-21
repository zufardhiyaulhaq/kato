package v1alpha1

import (
	"encoding/json"
	"testing"
)

// A UseCase input can declare a default; it round-trips through JSON intact,
// and an omitted default marshals away (omitempty) so "empty = no default".
func TestInputDeclDefaultRoundTrip(t *testing.T) {
	in := InputDecl{Name: "tls", Required: true, Default: "false"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got InputDecl
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Default != "false" {
		t.Errorf("Default = %q, want %q", got.Default, "false")
	}

	// Empty default is omitted from JSON entirely.
	b2, err := json.Marshal(InputDecl{Name: "pod"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if s := string(b2); s != `{"name":"pod"}` {
		t.Errorf("empty-default JSON = %s, want {\"name\":\"pod\"}", s)
	}
}
