{% raw %}
# kato UseCase Input Defaults — Design

## Problem

A `UseCase` declares its inputs as `InputDecl{Name, Type, Required}`. There is no
way to give an input a default value. Today, if a step's `with` references
`$(inputs.X)` and the caller omits `X`, the reference fails to resolve and the
step fails — so in practice every input referenced in a `with` value must be
`required: true`. There is no way to say "use this value unless the caller
overrides it."

## Goal

Let an input declare a **default value** that is used when the caller omits the
input. If the caller provides the input, the caller's value wins. An **empty
default means there is no default** (unchanged behavior).

## Non-goals

- Typed / non-string defaults. v1 inputs are `type: string` only; the default is
  a string like every input value.
- Defaulting *to* the empty string. Empty is the sentinel for "no default"; this
  is an accepted, documented limitation.
- Templated or computed defaults (e.g. a default that references another input or
  a step output). A default is a literal string.
- Per-caller or environment-based defaults. The default lives on the UseCase.

## Resolution model

A default is simply the **fallback used when the caller omits the input**. There
is no conflict between `required` and a default: a default satisfies `required`.

For each declared input, resolved in order:

1. Caller provided the input → use the caller's value.
2. Caller omitted it and `Default != ""` → use the default.
3. Caller omitted it, no default, `Required: true` → error
   `missing required input "X"` (unchanged).
4. Caller omitted it, no default, not required → the input stays absent
   (unchanged).

The "unknown input" rejection (a caller-supplied key that no `InputDecl`
declares) is unchanged.

## Architecture

Inputs flow through the engine as a single `map[string]string` that is threaded
into every `$(inputs.X)` lookup — the `when` evaluator, the `with` substitution,
and the `forEach` iteration path all read `inputs[field]`. This gives one choke
point.

Defaults are **materialized once**, in `Engine.Execute`, immediately after input
validation. The validation step returns the *effective* input map (caller values
with defaults filled in for omitted inputs). The flow then runs on that map, so
`when`, `with`, and `forEach` resolve defaults automatically with no changes at
the individual lookup sites.

Rejected alternative: adding fallback logic at each lookup site
(`inputs[field]` → check default). That scatters the same rule across ~4 places
and is easy to get inconsistent. One seam is simpler and provably uniform.

### Types (`api/v1alpha1/usecase_types.go`)

```go
// InputDecl declares a UseCase input. v1 supports type "string" only.
type InputDecl struct {
    Name string `json:"name"`
    // +kubebuilder:validation:Enum=string
    // +kubebuilder:default=string
    Type     string `json:"type,omitempty"`
    Required bool   `json:"required,omitempty"`
    // Default is used when the caller omits this input. Empty means no default.
    Default  string `json:"default,omitempty"`
}
```

The CRD manifest (`config/crd/...`) and `zz_generated.deepcopy.go` are
regenerated from this change. `Default` is a plain scalar string, so the
generated deepcopy needs no manual edits (value copy).

### Validation / materialization (`internal/engine/engine.go`)

`validateInputs` becomes the place that both validates and produces the effective
map. Its current signature returns only `error`; it changes to also return the
resolved `map[string]string`:

```go
// resolveInputs validates caller inputs against the UseCase declarations and
// returns the effective input map (caller values with defaults filled in for
// omitted inputs). The returned error is non-nil only for invalid inputs
// (missing required with no default, or an unknown input key).
func resolveInputs(uc *v1alpha1.UseCase, given map[string]string) (map[string]string, error)
```

Behavior per the resolution model above. `Execute` calls it and runs the flow on
the returned map:

```go
inputs, err := resolveInputs(uc, given)
if err != nil {
    return Result{}, err
}
// ... flow runs on `inputs`, which already contains defaults
```

Callers that pass a `nil` map still work — defaults are written into a fresh map.

### Audit persistence — decision

There are two entry paths and they persist inputs differently:

- **API path** (`internal/server`): after `Execute`, `SaveRun` creates the `Run`
  with `Spec.Inputs` = the raw caller map.
- **Controller path** (`internal/controller`): the `Run` already exists with
  `Spec.Inputs` authored by the caller (kubectl/GitOps); the reconciler only
  writes `Status`, never rewriting `Spec`.

Given that asymmetry, **`Run.Spec.Inputs` stays the raw caller input on both
paths** — no persistence change. The controller must not rewrite a user-authored
`Spec`, so injecting defaults on only the API path would make the two paths
disagree. Defaults live on the `UseCase` declaration and are discoverable there;
`Spec.Inputs` remains an honest record of exactly what the caller requested. This
is the YAGNI choice: snapshotting the *effective* input map into `Run.Status`
would need a new status field plus a Run CRD regeneration, and is deferred until
there is a concrete need. The engine still *executes* on the effective map — this
decision is only about what the `Run` record stores.

### API contract (`internal/server`)

`GET /api/v1/usecases/{name}` already returns each input's declaration as
`[]v1alpha1.InputDecl` verbatim. Adding the `Default` field (with a
`json:"default,omitempty"` tag) surfaces it in that response automatically — **no
server code change is required**. No behavior change to `POST .../run`.

## Testing strategy

Engine-level (`internal/engine`), one test per resolution branch:

- **caller value wins** — input has a default, caller supplies a different value;
  effective map has the caller's value.
- **default fills omission** — input has a default, caller omits it; effective map
  has the default, and a `with: $(inputs.X)` step resolves and runs (proving the
  materialization reaches the substitution path).
- **default satisfies required** — `required: true` + default, caller omits it;
  no error, default is used.
- **required still errors without a default** — `required: true`, no default,
  caller omits it; `missing required input` error (regression guard).
- **not-required, no default, omitted** — stays absent (unchanged).
- **unknown input still rejected** — unchanged.

CRD round-trip: a `UseCase` YAML with `default:` on an input marshals and
unmarshals with the value intact.

## Docs

- `docs/METHOD.md` / README input rules: soften "any input referenced in `with`
  must be `required`" to "…must be `required` **or have a default**."
- Document the empty-string limitation next to the `default` field description.
{% endraw %}
