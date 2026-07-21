{% raw %}
# kato UseCase Input Defaults Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a `UseCase` input declare a default value that is used when the caller omits the input; an empty default means no default.

**Architecture:** Add a `Default string` field to `InputDecl`. Materialize defaults once, in `Engine.Execute`, immediately after input validation — by turning `validateInputs` into `resolveInputs`, which returns the effective input map (caller values with defaults filled in). Every `$(inputs.X)` lookup already reads from that single map, so `when`, `with`, and `forEach` resolve defaults with no further changes.

**Tech Stack:** Go, kubebuilder/controller-gen (CRD + deepcopy codegen), Helm chart CRDs.

## Global Constraints

- **Go toolchain:** prefix every `go`/`make` invocation with `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go` (bare `go` on PATH has a mismatched GOROOT).
- **DO NOT COMMIT.** Stage with `git add` only. The user commits their own work. Every "Commit" step in this plan means **stage only**.
- **Do NOT create CLAUDE.md.**
- **Resolution model (verbatim):** caller value wins; else non-empty default fills; else `required` with no default errors `missing required input "X"`; else the input stays absent. A default satisfies `required` (no contradiction).
- **Empty default = no default.** `Default: ""` means "no default"; you cannot default *to* the empty string. Documented limitation, not a bug.
- **Audit persistence unchanged:** `Run.Spec.Inputs` stays the raw caller input on both the API and controller paths. Do NOT rewrite persistence or mutate `Spec`. The engine executes on the effective map; only the stored `Run` keeps the raw request.
- **Two CRD manifest copies must stay identical:** `config/crd/bases/kato.zufardhiyaulhaq.com_usecases.yaml` (generated) and `charts/kato/crds/kato.zufardhiyaulhaq.com_usecases.yaml` (hand-synced by `cp`). There is no Makefile step linking them.
- **`methods.Builtin()`** returns the fully-populated registry (not `NewRegistry()`) — used by engine tests that need real methods.

---

## Task 1: Add `Default` field to `InputDecl` and regenerate CRD + deepcopy

**Files:**
- Modify: `api/v1alpha1/usecase_types.go:8-14` (`InputDecl` struct)
- Regenerate: `api/v1alpha1/zz_generated.deepcopy.go` (via `make generate`)
- Regenerate: `config/crd/bases/kato.zufardhiyaulhaq.com_usecases.yaml` (via `make manifests`)
- Sync: `charts/kato/crds/kato.zufardhiyaulhaq.com_usecases.yaml` (via `cp`)
- Test: `api/v1alpha1/usecase_types_test.go` (create)

**Interfaces:**
- Produces: `v1alpha1.InputDecl` now has field `Default string` with JSON tag `default,omitempty`. Later tasks read `in.Default` off each `InputDecl`.

- [ ] **Step 1: Write the failing test**

Create `api/v1alpha1/usecase_types_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./api/v1alpha1/ -run TestInputDeclDefaultRoundTrip -v`
Expected: FAIL — `got.Default undefined (type InputDecl has no field or method Default)` (compile error).

- [ ] **Step 3: Add the `Default` field**

In `api/v1alpha1/usecase_types.go`, change the `InputDecl` struct to:

```go
// InputDecl declares a UseCase input. v1 supports type "string" only.
type InputDecl struct {
	Name string `json:"name"`
	// +kubebuilder:validation:Enum=string
	// +kubebuilder:default=string
	Type     string `json:"type,omitempty"`
	Required bool   `json:"required,omitempty"`
	// Default is used when the caller omits this input. Empty means no default.
	Default string `json:"default,omitempty"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./api/v1alpha1/ -run TestInputDeclDefaultRoundTrip -v`
Expected: PASS.

- [ ] **Step 5: Regenerate deepcopy and CRD manifests**

Run:
```bash
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go make generate
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go make manifests
```
Expected: `zz_generated.deepcopy.go` is unchanged (the added field is a plain string, covered by the existing `*out = *in`). `config/crd/bases/kato.zufardhiyaulhaq.com_usecases.yaml` gains a `default` property under `inputs.items.properties`, alphabetically before `name`:

```yaml
                  properties:
                    default:
                      type: string
                    name:
                      type: string
                    required:
                      type: boolean
                    type:
                      default: string
                      enum:
                      - string
                      type: string
```

Verify the new property is present:
Run: `grep -n -A2 "properties:" config/crd/bases/kato.zufardhiyaulhaq.com_usecases.yaml | grep -A2 "default:" | head`
Expected: shows the `default:` property with `type: string`.

- [ ] **Step 6: Sync the chart CRD copy**

Run:
```bash
cp config/crd/bases/kato.zufardhiyaulhaq.com_usecases.yaml charts/kato/crds/kato.zufardhiyaulhaq.com_usecases.yaml
```
Verify identical:
Run: `diff -q config/crd/bases/kato.zufardhiyaulhaq.com_usecases.yaml charts/kato/crds/kato.zufardhiyaulhaq.com_usecases.yaml && echo IDENTICAL`
Expected: `IDENTICAL`.

- [ ] **Step 7: Build and stage**

Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go build ./...`
Expected: clean, no output.

```bash
git add api/v1alpha1/usecase_types.go api/v1alpha1/usecase_types_test.go api/v1alpha1/zz_generated.deepcopy.go config/crd/bases/kato.zufardhiyaulhaq.com_usecases.yaml charts/kato/crds/kato.zufardhiyaulhaq.com_usecases.yaml
```
(Stage only — do not commit.)

---

## Task 2: Materialize defaults in the engine (`resolveInputs`)

**Files:**
- Modify: `internal/engine/engine.go:70` (the call site in `Execute`) and `internal/engine/engine.go:373-389` (`validateInputs` → `resolveInputs`)
- Test: `internal/engine/inputs_test.go` (create)

**Interfaces:**
- Consumes: `v1alpha1.InputDecl.Default` (from Task 1).
- Produces: `func resolveInputs(uc *v1alpha1.UseCase, given map[string]string) (map[string]string, error)` — returns the effective input map (caller values plus defaults for omitted inputs). Returns a non-nil `*engine.InputError` only for an unknown input key or a missing required input that has no default. `Execute` runs the flow on the returned map.

- [ ] **Step 1: Write the failing tests**

Create `internal/engine/inputs_test.go`:

```go
package engine

import (
	"errors"
	"testing"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/engine/ -run TestResolveInputs -v`
Expected: FAIL — `undefined: resolveInputs` (compile error).

- [ ] **Step 3: Replace `validateInputs` with `resolveInputs`**

In `internal/engine/engine.go`, replace the whole `validateInputs` function (currently lines ~373-389):

```go
func validateInputs(uc *v1alpha1.UseCase, given map[string]string) error {
	declared := map[string]bool{}
	for _, in := range uc.Spec.Inputs {
		declared[in.Name] = true
		if in.Required {
			if _, ok := given[in.Name]; !ok {
				return &InputError{Msg: fmt.Sprintf("missing required input %q", in.Name)}
			}
		}
	}
	for name := range given {
		if !declared[name] {
			return &InputError{Msg: fmt.Sprintf("unknown input %q", name)}
		}
	}
	return nil
}
```

with:

```go
// resolveInputs validates caller inputs against the UseCase declarations and
// returns the effective input map: caller values, with each omitted input filled
// from its Default when non-empty. The returned error is a non-nil *InputError
// only for an unknown input key, or a required input that has neither a caller
// value nor a default. A default satisfies required (empty default = none).
func resolveInputs(uc *v1alpha1.UseCase, given map[string]string) (map[string]string, error) {
	effective := map[string]string{}
	for name, v := range given {
		effective[name] = v
	}
	declared := map[string]bool{}
	for _, in := range uc.Spec.Inputs {
		declared[in.Name] = true
		if _, ok := effective[in.Name]; ok {
			continue // caller value wins
		}
		if in.Default != "" {
			effective[in.Name] = in.Default
			continue // default fills the omission (also satisfies Required)
		}
		if in.Required {
			return nil, &InputError{Msg: fmt.Sprintf("missing required input %q", in.Name)}
		}
	}
	for name := range effective {
		if !declared[name] {
			return nil, &InputError{Msg: fmt.Sprintf("unknown input %q", name)}
		}
	}
	return effective, nil
}
```

- [ ] **Step 4: Update the call site in `Execute`**

In `internal/engine/engine.go`, change the top of `Execute` (currently lines ~69-72) from:

```go
func (e *Engine) Execute(ctx context.Context, uc *v1alpha1.UseCase, inputs map[string]string) (Result, error) {
	if err := validateInputs(uc, inputs); err != nil {
		return Result{}, err
	}
```

to:

```go
func (e *Engine) Execute(ctx context.Context, uc *v1alpha1.UseCase, inputs map[string]string) (Result, error) {
	inputs, err := resolveInputs(uc, inputs)
	if err != nil {
		return Result{}, err
	}
```

(The local `inputs` parameter is reassigned to the effective map; the rest of `Execute` — `runStep`, `runForEachStep`, and every `$(inputs.X)` lookup — reads this reassigned map unchanged. Note: `Execute` already declares `err` later via `summary, model, err := ...`; that line uses `:=` with new vars `summary`/`model`, so introducing `err` here with `:=` is fine, but confirm the build. If the compiler reports `err` redeclared with no new variables at the summary line, that line already has new vars and will be fine — do not change it.)

- [ ] **Step 5: Run the new tests to verify they pass**

Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/engine/ -run TestResolveInputs -v`
Expected: PASS (all seven).

- [ ] **Step 6: Run the full engine suite (regression guard)**

Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test -count=1 ./internal/engine/`
Expected: `ok`. Any pre-existing test that relied on `validateInputs`'s old error behavior for missing-required still passes (behavior is unchanged for inputs without defaults).

- [ ] **Step 7: Build, vet, and stage**

Run:
```bash
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go build ./...
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go vet ./internal/engine/
```
Expected: both clean.

```bash
git add internal/engine/engine.go internal/engine/inputs_test.go
```
(Stage only — do not commit.)

---

## Task 3: End-to-end proof + documentation

**Files:**
- Test: `internal/engine/inputs_test.go` (append one execution-level test)
- Modify: `docs/METHOD.md` (input rules)
- Modify: `charts/kato/README.md.gotmpl` (input rules, if the caveat appears there — grep first)

**Interfaces:**
- Consumes: `resolveInputs` (Task 2), `methods.Builtin()` for a registry with real methods, and an existing method with a required param to prove `with` resolves a defaulted input.

- [ ] **Step 1: Write the failing end-to-end test**

Append to `internal/engine/inputs_test.go`. This proves a defaulted input reaches the `with` substitution path and drives a real method. Use `probe_dns` (single required param `name`) via a fake prober so no network is touched.

First confirm the method + param + fake shape:
Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go doc ./internal/methods probeDNS` (or `grep -n "probe_dns\|ProbeDNS\|Name:.*name" internal/methods/probe_dns.go`)
Expected: confirms `probe_dns` takes a required `name` param and calls `deps.Prober.ProbeDNS`.

```go
func TestExecute_DefaultReachesWithSubstitution(t *testing.T) {
	reg := methods.Builtin()
	// A fake prober whose ProbeDNS records the resolved name it was called with.
	fp := &fakeProber{dns: methods.DNSResult{Addresses: []string{"10.0.0.1"}}}
	e := &Engine{Deps: methods.Deps{Prober: fp}, Registry: reg, StepTimeout: time.Second}

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
	if fp.gotDNS.Name != "kubernetes.default" {
		t.Errorf("probe_dns called with name %q, want default %q", fp.gotDNS.Name, "kubernetes.default")
	}
}
```

Note: this test references a `fakeProber` with a `dns` result and a `gotDNS` recorder, and `methods.DNSResult` / the `Name` field on the DNS probe request. Before writing, confirm the exact names:
Run: `grep -n "type fakeProber\|gotDNS\|dns \|DNSResult\|DNSProbeRequest\|func (f \*fakeProber) ProbeDNS" internal/methods/probe_tcp_test.go internal/methods/prober.go`
- If `fakeProber` lives in package `methods` (a `_test.go` file), it is NOT importable from package `engine`. In that case do NOT reuse it. Instead define a local minimal prober in `inputs_test.go` that implements `methods.Prober` and records the DNS request, e.g.:

```go
type recordingProber struct {
	methods.Prober // embed nil; only ProbeDNS is called by this test
	gotName        string
}

func (r *recordingProber) ProbeDNS(_ context.Context, req methods.DNSProbeRequest) methods.DNSResult {
	r.gotName = req.Name
	return methods.DNSResult{Addresses: []string{"10.0.0.1"}}
}
```

and assert `rp.gotName == "kubernetes.default"`. Use the field/type names confirmed by the grep above (adjust `DNSProbeRequest`/`DNSResult`/`Name` to match `internal/methods/prober.go`). Embedding `methods.Prober` lets the struct satisfy the full interface while overriding only `ProbeDNS`; any other probe call would nil-panic, but this flow calls only DNS.

Add imports as needed: `context`, `time`, `github.com/zufardhiyaulhaq/kato/internal/methods`.

- [ ] **Step 2: Run it to verify it fails**

Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/engine/ -run TestExecute_DefaultReachesWithSubstitution -v`
Expected: with Task 2 already in place this may PASS immediately — that is acceptable (it is an integration guard, not a red-first unit). If it FAILS, the failure must be about the recorded name (`want default`), not a compile error; fix names/imports until it compiles, then it should pass on Task 2's logic.

- [ ] **Step 3: Run it to verify it passes**

Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/engine/ -run TestExecute_DefaultReachesWithSubstitution -v`
Expected: PASS.

- [ ] **Step 4: Update the input-rules docs**

Find the current caveat wording:
Run: `grep -rn "must be required\|referenced in .with.\|required: true\|no input defaults\|has no input defaults" docs/METHOD.md charts/kato/README.md.gotmpl examples/usecases/`
For each prose occurrence in `docs/METHOD.md` (and `charts/kato/README.md.gotmpl` if present) that says an input referenced in a `with` value must be `required`, soften it to allow a default. Example replacement:

- Before: `any input referenced in a step's \`with\` must be declared \`required: true\` (kato has no input-level defaults).`
- After: `any input referenced in a step's \`with\` must be declared \`required: true\` **or given a \`default\`** — otherwise an omitted value leaves \`$(inputs.X)\` unresolved and the step fails. A \`default\` is used when the caller omits the input; an empty default means none (you cannot default to the empty string).`

If the exact string differs, preserve the surrounding sentence and only add the "or given a `default`" allowance plus the one-line semantics. If no such caveat exists in a file, skip that file (do not invent one).

- [ ] **Step 5: If `README.md.gotmpl` changed, regenerate the READMEs**

Only if you edited `charts/kato/README.md.gotmpl`:
Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go make readme`
Expected: `README.md` and `charts/kato/README.md` regenerate with the same wording change. If `make readme` requires `helm-docs` and it is unavailable, hand-edit the two generated READMEs to match the `.gotmpl` change and note it.

- [ ] **Step 6: Full verification**

Run:
```bash
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go build ./...
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test -count=1 ./...
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go vet ./...
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go gofmt -l api/v1alpha1/usecase_types.go api/v1alpha1/usecase_types_test.go internal/engine/engine.go internal/engine/inputs_test.go
```
Expected: build clean; all packages `ok`; vet clean; `gofmt -l` prints nothing.

- [ ] **Step 7: Stage**

```bash
git add internal/engine/inputs_test.go docs/METHOD.md
# add only if changed: charts/kato/README.md.gotmpl README.md charts/kato/README.md
```
(Stage only — do not commit. Present the staged change set to the user for them to commit.)

---

## Self-Review notes (for the executor)

- **Spec coverage:** Task 1 = CRD field + regen; Task 2 = resolution model + single-seam materialization; Task 3 = end-to-end proof + docs. API-contract point needs no code (struct-tag surfaces `default` automatically) — no task, by design. Audit-persistence point = "no change" per Global Constraints — no task, by design.
- **Type consistency:** `resolveInputs(uc, given) (map[string]string, error)` is defined in Task 2 and consumed by `Execute` (Task 2) and the integration test (Task 3). `InputDecl.Default` defined in Task 1, read in Task 2.
- **Watch item:** the `fakeProber` in `internal/methods/*_test.go` is package-private to `methods` and NOT importable from `engine` tests — Task 3 Step 1 handles this with a local `recordingProber`. Confirm the DNS request/result type names by grep before writing.
{% endraw %}
