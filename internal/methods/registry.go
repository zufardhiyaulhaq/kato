package methods

import (
	"fmt"
	"sort"
)

type Registry struct {
	byName map[string]Method
}

func NewRegistry() *Registry {
	return &Registry{byName: map[string]Method{}}
}

func (r *Registry) Register(m Method) {
	if _, exists := r.byName[m.Name()]; exists {
		panic(fmt.Sprintf("method %q registered twice", m.Name()))
	}
	r.byName[m.Name()] = m
}

func (r *Registry) Get(name string) (Method, bool) {
	m, ok := r.byName[name]
	return m, ok
}

func (r *Registry) All() []Method {
	out := make([]Method, 0, len(r.byName))
	for _, m := range r.byName {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// builtinFns is appended to by each method file's init().
var builtinFns []func(*Registry)

// Builtin returns a registry with every built-in method registered.
func Builtin() *Registry {
	r := NewRegistry()
	for _, fn := range builtinFns {
		fn(r)
	}
	return r
}
