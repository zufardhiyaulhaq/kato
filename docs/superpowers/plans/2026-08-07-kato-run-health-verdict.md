# kato Run Health Verdict Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a structured health verdict — `healthy` (true/false/unknown) plus a short `headline` — to every kato run, derived by the summarizer from a `VERDICT:` first line in the model's text output, surfaced on the run API response and the `Run` CR status.

**Architecture:** The summarizer appends a fixed format instruction (with a one-shot example) to the system prompt, then parses the model's first line into `(healthy *bool, headline string, prose string)` with a pure, total function. The verdict rides the existing plain-text completion — no JSON mode. It flows through a new `engine.SummaryOutput` return type onto `engine.Result`, into `RunStatus` (nullable `*bool` + string), and out through the run response. It is advisory everywhere: it never affects step outcomes, phase, `when`, or `$(steps.x.y)`.

**Tech Stack:** Go 1.25, controller-runtime, kubebuilder CRDs, standard `regexp`/`strings`/`unicode/utf8`, `go test`.

## Global Constraints

- **Go toolchain:** `go.mod` says `go 1.25.0`; asdf `.tool-versions` pins `golang 1.25.2`. Use bare `go ...` / `make ...` commands — there is **no** `env GOROOT=` prefix in this repo's Makefile.
- **Verdict is advisory:** it MUST NOT influence `res.Phase`, step outcomes, `when` evaluation, or `$(steps.x.y)` references. A missing/malformed verdict is non-fatal — the run still succeeds exactly as today.
- **No JSON mode:** the verdict rides a first-line text convention parsed by regex, tolerant of omission (portability across OpenAI-compatible endpoints incl. Ollama).
- **`headline` hard cap: 120 characters**, rune-safe, enforced by the parser (never by trusting the model). Single line — the capture stops at the first newline.
- **CRD sync is manual:** `make manifests` writes only `config/crd/bases/`. The `charts/kato/crds/` copy MUST be updated by hand (no Makefile rule syncs it).
- **Branching:** work on a dedicated branch (e.g. `feat/run-health-verdict`); do not commit to `main`. Another agent may share this tree — coordinate or use a git worktree before committing.

---

## File Structure

- `internal/summarizer/verdict.go` (**create**) — `parseVerdict` pure function + the `verdictInstruction` const. One responsibility: turn raw model text into `(prose, healthy, headline)`.
- `internal/summarizer/verdict_test.go` (**create**) — table tests for the parser.
- `internal/engine/engine.go` (**modify**) — new `SummaryOutput` struct, changed `SummarizeFn` signature, `Result` gains `Healthy`/`Headline`, updated `Execute` call site.
- `internal/summarizer/summarizer.go` (**modify**) — append `verdictInstruction` to the system prompt, call `parseVerdict`, return `engine.SummaryOutput`.
- `api/v1alpha1/run_types.go` (**modify**) — `RunStatus` gains `Healthy *bool` + `Headline string`.
- `api/v1alpha1/zz_generated.deepcopy.go` (**regenerate** via `make generate`).
- `config/crd/bases/kato.zufardhiyaulhaq.com_runs.yaml` (**regenerate** via `make manifests`) + `charts/kato/crds/kato.zufardhiyaulhaq.com_runs.yaml` (**manual copy**).
- `internal/store/store.go` (**modify**) — `BuildRunStatus` maps the verdict.
- `internal/server/server.go` (**modify**) — `runResponse` gains `healthy`/`headline`.
- `openapi.yaml` (**modify**) — `RunResponse` + `RunStatus` schemas.
- `ARCHITECTURE.md` (**modify**) — a short note that the verdict is LLM-derived and advisory.
- Existing tests to update where the `SummarizeFn` signature changes: `internal/engine/*_test.go`, `internal/summarizer/summarizer_test.go`, `internal/store/store_test.go`, `internal/server/server_test.go`.

---

## Task 1: Verdict parser (pure function)

**Files:**
- Create: `internal/summarizer/verdict.go`
- Test: `internal/summarizer/verdict_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func parseVerdict(raw string) (summary string, healthy *bool, headline string)` and `const verdictInstruction string`, both in package `summarizer`.

- [ ] **Step 1: Write the failing test**

```go
// internal/summarizer/verdict_test.go
package summarizer

import "testing"

func boolPtr(b bool) *bool { return &b }

func TestParseVerdict(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantHealthy  *bool
		wantHeadline string
		wantSummary  string
	}{
		{
			name:         "healthy",
			raw:          "VERDICT: healthy — all replicas ready\n\nThe deployment is fine.",
			wantHealthy:  boolPtr(true),
			wantHeadline: "all replicas ready",
			wantSummary:  "The deployment is fine.",
		},
		{
			name:         "unhealthy with em dash",
			raw:          "VERDICT: unhealthy — CrashLoopBackOff, bad image tag\n\nPods are crashing.",
			wantHealthy:  boolPtr(false),
			wantHeadline: "CrashLoopBackOff, bad image tag",
			wantSummary:  "Pods are crashing.",
		},
		{
			name:         "unhealthy with hyphen separator",
			raw:          "VERDICT: unhealthy - OOMKilled\n\nRaise the memory limit.",
			wantHealthy:  boolPtr(false),
			wantHeadline: "OOMKilled",
			wantSummary:  "Raise the memory limit.",
		},
		{
			name:         "unknown keyword yields nil healthy",
			raw:          "VERDICT: unknown — evidence inconclusive\n\nNot enough data.",
			wantHealthy:  nil,
			wantHeadline: "evidence inconclusive",
			wantSummary:  "Not enough data.",
		},
		{
			name:         "case insensitive",
			raw:          "verdict: HEALTHY — ok\n\nprose",
			wantHealthy:  boolPtr(true),
			wantHeadline: "ok",
			wantSummary:  "prose",
		},
		{
			name:         "leading blank lines before verdict",
			raw:          "\n\nVERDICT: healthy — ok\n\nprose",
			wantHealthy:  boolPtr(true),
			wantHeadline: "ok",
			wantSummary:  "prose",
		},
		{
			name:         "no verdict line leaves prose untouched",
			raw:          "The deployment is fine, nothing to report.",
			wantHealthy:  nil,
			wantHeadline: "",
			wantSummary:  "The deployment is fine, nothing to report.",
		},
		{
			name:         "malformed verdict value treated as no match",
			raw:          "VERDICT: maybe — who knows\n\nprose",
			wantHealthy:  nil,
			wantHeadline: "",
			wantSummary:  "VERDICT: maybe — who knows\n\nprose",
		},
		{
			name:         "missing headline",
			raw:          "VERDICT: healthy\n\nprose",
			wantHealthy:  boolPtr(true),
			wantHeadline: "",
			wantSummary:  "prose",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, healthy, headline := parseVerdict(tt.raw)
			if !eqBoolPtr(healthy, tt.wantHealthy) {
				t.Errorf("healthy = %v, want %v", healthy, tt.wantHealthy)
			}
			if headline != tt.wantHeadline {
				t.Errorf("headline = %q, want %q", headline, tt.wantHeadline)
			}
			if summary != tt.wantSummary {
				t.Errorf("summary = %q, want %q", summary, tt.wantSummary)
			}
		})
	}
}

func TestParseVerdictTruncatesHeadline(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	_, _, headline := parseVerdict("VERDICT: unhealthy — " + long + "\n\nprose")
	// 120 runes + the ellipsis rune.
	if n := len([]rune(headline)); n != 121 {
		t.Fatalf("headline rune count = %d, want 121 (120 + ellipsis)", n)
	}
	if headline[len(headline)-len("…"):] != "…" {
		t.Errorf("headline should end with ellipsis, got %q", headline)
	}
}

func eqBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/summarizer/ -run TestParseVerdict -v`
Expected: FAIL — `undefined: parseVerdict`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/summarizer/verdict.go
package summarizer

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// verdictInstruction is appended to the system prompt so every summary begins
// with a machine-readable verdict line. A one-shot example raises format
// compliance on weaker/local models. \p{Pd} matches any dash punctuation
// (hyphen, en/em dash) as the optional separator.
const verdictInstruction = `Begin your reply with EXACTLY one line in this format, then a blank line, then your analysis:
VERDICT: <healthy|unhealthy|unknown> — <one short reason>
Use "healthy" only if the evidence shows the subject is working, "unhealthy" if it shows a problem, and "unknown" if the evidence is insufficient to decide. Example:

VERDICT: unhealthy — CrashLoopBackOff, image tag :v2 not found

The deployment has 0/3 ready replicas because ...`

// verdictLineRE matches the verdict on a single line: the keyword, then an
// optional dash/colon separator, then the rest of the line as the headline.
var verdictLineRE = regexp.MustCompile(`(?i)^\s*VERDICT:\s*(healthy|unhealthy|unknown)\b[\s\p{Pd}:]*(.*)$`)

const headlineMaxRunes = 120

// parseVerdict extracts a health verdict from the model's first non-empty line
// and returns the prose with that line removed. It is total: a missing or
// malformed line yields healthy=nil, headline="", and the ORIGINAL text
// unchanged. "healthy"->true, "unhealthy"->false, "unknown"/no-match->nil.
func parseVerdict(raw string) (summary string, healthy *bool, headline string) {
	trimmed := strings.TrimLeft(raw, " \t\r\n")
	parts := strings.SplitN(trimmed, "\n", 2)
	first := parts[0]
	m := verdictLineRE.FindStringSubmatch(first)
	if m == nil {
		return raw, nil, ""
	}
	switch strings.ToLower(m[1]) {
	case "healthy":
		t := true
		healthy = &t
	case "unhealthy":
		f := false
		healthy = &f
	}
	headline = strings.TrimSpace(m[2])
	if utf8.RuneCountInString(headline) > headlineMaxRunes {
		r := []rune(headline)
		headline = strings.TrimSpace(string(r[:headlineMaxRunes])) + "…"
	}
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}
	return strings.TrimLeft(rest, "\r\n"), healthy, headline
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/summarizer/ -run TestParseVerdict -v`
Expected: PASS (both `TestParseVerdict` and `TestParseVerdictTruncatesHeadline`).

- [ ] **Step 5: Commit**

```bash
git add internal/summarizer/verdict.go internal/summarizer/verdict_test.go
git commit -m "feat(summarizer): add VERDICT first-line parser"
```

---

## Task 2: Thread the verdict through engine + summarizer

**Files:**
- Modify: `internal/engine/engine.go` (`Result` struct lines 48-54, `SummarizeFn` + `Engine` lines 56-65, `Execute` call site lines 84-97)
- Modify: `internal/summarizer/summarizer.go` (`Summarize`, lines 96-141)
- Test: `internal/summarizer/summarizer_test.go` (update), `internal/engine/*_test.go` (update fakes)

**Interfaces:**
- Consumes: `parseVerdict`, `verdictInstruction` (Task 1).
- Produces:
  - `type engine.SummaryOutput struct { Summary string; Healthy *bool; Headline string; ModelConfig string }`
  - `type engine.SummarizeFn func(ctx context.Context, uc *v1alpha1.UseCase, steps []StepResult) (SummaryOutput, error)`
  - `engine.Result` gains `Healthy *bool` and `Headline string`.

- [ ] **Step 1: Write the failing test (summarizer emits a parsed verdict)**

Add to `internal/summarizer/summarizer_test.go` (reuse the package's existing fake `Completer`; if none exists, this inline fake works):

```go
type fakeCompleter struct{ out string }

func (f fakeCompleter) Complete(ctx context.Context, system, user string) (string, error) {
	return f.out, nil
}

func TestSummarizeReturnsVerdict(t *testing.T) {
	s := &Summarizer{
		Resolve: func(uc *v1alpha1.UseCase) (Completer, string, error) {
			return fakeCompleter{out: "VERDICT: unhealthy — pods crashlooping\n\nThe pods are crashing."}, "gpt-test", nil
		},
	}
	uc := &v1alpha1.UseCase{}
	uc.Name = "demo"
	uc.Spec.Summary.Prompt = "diagnose it"

	out, err := s.Summarize(context.Background(), uc, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Healthy == nil || *out.Healthy != false {
		t.Errorf("Healthy = %v, want false", out.Healthy)
	}
	if out.Headline != "pods crashlooping" {
		t.Errorf("Headline = %q, want %q", out.Headline, "pods crashlooping")
	}
	if out.Summary != "The pods are crashing." {
		t.Errorf("Summary = %q, want prose without the verdict line", out.Summary)
	}
	if out.ModelConfig != "gpt-test" {
		t.Errorf("ModelConfig = %q, want gpt-test", out.ModelConfig)
	}
}

func TestSummarizeAppendsVerdictInstruction(t *testing.T) {
	var gotSystem string
	s := &Summarizer{
		Resolve: func(uc *v1alpha1.UseCase) (Completer, string, error) {
			return systemCapturingCompleter{capture: &gotSystem}, "m", nil
		},
	}
	uc := &v1alpha1.UseCase{}
	uc.Spec.Summary.Prompt = "x"
	if _, err := s.Summarize(context.Background(), uc, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotSystem, "VERDICT:") {
		t.Errorf("system prompt should carry the verdict instruction, got %q", gotSystem)
	}
}

type systemCapturingCompleter struct{ capture *string }

func (c systemCapturingCompleter) Complete(ctx context.Context, system, user string) (string, error) {
	*c.capture = system
	return "VERDICT: healthy — ok\n\nfine", nil
}
```

(Imports needed in the test file: `context`, `strings`, `testing`, and the `v1alpha1` package `github.com/zufardhiyaulhaq/kato/api/v1alpha1`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/summarizer/ -run TestSummarize -v`
Expected: FAIL — `out.Healthy undefined` / `Summarize` returns `(string, string, error)` not a struct.

- [ ] **Step 3a: Add `SummaryOutput`, change `SummarizeFn`, extend `Result` in engine.go**

Replace the `Result` struct (lines 48-54) with:

```go
type Result struct {
	Phase       string
	Steps       []StepResult
	Summary     string
	Healthy     *bool  // health verdict; nil = unknown. Advisory, never affects Phase.
	Headline    string // one-line reason accompanying Healthy; empty when unknown.
	Warning     string // set when summary could not be produced
	ModelConfig string
}
```

Replace the `SummarizeFn` type (lines 56-59) with:

```go
// SummaryOutput is what a summarizer returns: the prose plus the structured
// health verdict parsed from it.
type SummaryOutput struct {
	Summary     string
	Healthy     *bool
	Headline    string
	ModelConfig string
}

// SummarizeFn produces a SummaryOutput from completed step results. The
// summarizer applies each step's summaryFilter itself.
type SummarizeFn func(ctx context.Context, uc *v1alpha1.UseCase, steps []StepResult) (SummaryOutput, error)
```

Replace the `Execute` call site (lines 88-96, the `if e.Summarize == nil { ... }` block onward) with:

```go
	if e.Summarize == nil {
		res.Warning = "no summarizer configured"
		return res, nil
	}
	out, err := e.Summarize(ctx, uc, res.Steps)
	if err != nil {
		// Spec §6.6: deterministic value never depends on AI availability.
		res.Warning = fmt.Sprintf("summary unavailable: %v", err)
		return res, nil
	}
	res.Summary = out.Summary
	res.Healthy = out.Healthy
	res.Headline = out.Headline
	res.ModelConfig = out.ModelConfig
	return res, nil
```

- [ ] **Step 3b: Update `Summarize` in summarizer.go**

In `internal/summarizer/summarizer.go`, change the signature and body of `Summarize` (lines 96-141). The two changes: append `verdictInstruction` to the system prompt, and parse the result. Replace the return type `(string, string, error)` with `(engine.SummaryOutput, error)`, and:

- Where the system prompt is used, use `system := systemPrompt + "\n\n" + verdictInstruction` and pass `system` to both the `DebugLog` messages and `completer.Complete`.
- Replace the final block:

```go
	out, err := completer.Complete(ctx, system, user)
	if err != nil {
		return engine.SummaryOutput{}, err
	}
	summary, healthy, headline := parseVerdict(out)
	return engine.SummaryOutput{
		Summary:     summary,
		Healthy:     healthy,
		Headline:    headline,
		ModelConfig: model,
	}, nil
```

- The two early `return "", "", err` in `Summarize` (the `Resolve` error path) become `return engine.SummaryOutput{}, err`.

- [ ] **Step 3c: Fix any other `SummarizeFn` implementations / fakes**

Search and update every fake summarizer in the engine tests to the new signature:

Run: `grep -rn "func(ctx context.Context, uc \*v1alpha1.UseCase, steps \[\]" internal/ ; grep -rn "SummarizeFn\|\.Summarize(" internal/engine`

For each engine test fake that returned `(string, string, error)`, change it to return `(engine.SummaryOutput{Summary: ..., ModelConfig: ...}, nil)`. (These are the tests that assert `res.Summary`; keep their expected summary strings, just wrap them.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/summarizer/ ./internal/engine/ -v`
Expected: PASS. If the build fails, it will name any un-migrated `SummarizeFn` call site — fix it the same way (wrap in `SummaryOutput`).

- [ ] **Step 5: Commit**

```bash
git add internal/engine/engine.go internal/summarizer/summarizer.go internal/summarizer/summarizer_test.go internal/engine
git commit -m "feat(engine,summarizer): carry health verdict through SummarizeFn"
```

---

## Task 3: Persist the verdict on RunStatus

**Files:**
- Modify: `api/v1alpha1/run_types.go` (`RunStatus`, lines 48-63)
- Regenerate: `api/v1alpha1/zz_generated.deepcopy.go`, `config/crd/bases/kato.zufardhiyaulhaq.com_runs.yaml`
- Manual copy: `charts/kato/crds/kato.zufardhiyaulhaq.com_runs.yaml`
- Modify: `internal/store/store.go` (`BuildRunStatus`, lines 57-97)
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: `engine.Result.Healthy`, `engine.Result.Headline` (Task 2).
- Produces: `RunStatus.Healthy *bool` (`json:"healthy,omitempty"`), `RunStatus.Headline string` (`json:"headline,omitempty"`), both set by `BuildRunStatus`.

- [ ] **Step 1: Write the failing test**

Add to `internal/store/store_test.go`:

```go
func TestBuildRunStatusCarriesVerdict(t *testing.T) {
	healthy := false
	res := engine.Result{
		Phase:    engine.PhaseSucceeded,
		Summary:  "pods crashing",
		Healthy:  &healthy,
		Headline: "CrashLoopBackOff",
	}
	now := time.Now()
	st, err := store.BuildRunStatus(res, now, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.Healthy == nil || *st.Healthy != false {
		t.Errorf("Healthy = %v, want false", st.Healthy)
	}
	if st.Headline != "CrashLoopBackOff" {
		t.Errorf("Headline = %q, want CrashLoopBackOff", st.Headline)
	}
}
```

(Match the test file's existing package name and imports — it may be `package store` or `package store_test`; mirror the neighbours. `engine` is `github.com/zufardhiyaulhaq/kato/internal/engine`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestBuildRunStatusCarriesVerdict -v`
Expected: FAIL — `res.Healthy undefined` is already resolved (Task 2), so this fails on `st.Healthy undefined` (field not yet on `RunStatus`).

- [ ] **Step 3a: Add the fields to RunStatus**

In `api/v1alpha1/run_types.go`, inside `RunStatus` (after `Summary`, before `Note`), add:

```go
	// Healthy is the summarizer's verdict on the subject of the run: true =
	// healthy, false = unhealthy, nil = unknown (no verdict emitted, or the
	// summary itself failed). Advisory — it never affects Phase.
	Healthy *bool `json:"healthy,omitempty"`
	// Headline is a short reason accompanying Healthy (e.g.
	// "CrashLoopBackOff — bad image tag :v2"). Single line, never longer than
	// 120 characters (kato truncates). Empty when unknown.
	Headline string `json:"headline,omitempty"`
```

- [ ] **Step 3b: Map them in BuildRunStatus**

In `internal/store/store.go`, in the returned `v1alpha1.RunStatus{...}` literal (lines 88-96), add after `Summary: res.Summary,`:

```go
		Healthy:     res.Healthy,
		Headline:    res.Headline,
```

- [ ] **Step 3c: Regenerate deepcopy + CRD, then copy the CRD into the chart**

```bash
make generate
make manifests
cp config/crd/bases/kato.zufardhiyaulhaq.com_runs.yaml charts/kato/crds/kato.zufardhiyaulhaq.com_runs.yaml
```

Verify `zz_generated.deepcopy.go`'s `RunStatus.DeepCopyInto` now copies the `Healthy *bool`, and that both CRD YAMLs show `healthy` (`type: boolean`) and `headline` (`type: string`) under `status.properties`.

Run: `git diff --stat api/v1alpha1/zz_generated.deepcopy.go config/crd/bases charts/kato/crds`
Expected: all three show changes.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/store/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add api/v1alpha1/run_types.go api/v1alpha1/zz_generated.deepcopy.go config/crd/bases/kato.zufardhiyaulhaq.com_runs.yaml charts/kato/crds/kato.zufardhiyaulhaq.com_runs.yaml internal/store/store.go internal/store/store_test.go
git commit -m "feat(api,store): persist health verdict on RunStatus"
```

---

## Task 4: Surface the verdict on the run API + openapi

**Files:**
- Modify: `internal/server/server.go` (`runResponse` lines 239-245, `runUseCase` build lines 294-297)
- Test: `internal/server/server_test.go`
- Modify: `openapi.yaml` (`RunResponse` lines 673-696, `RunStatus` lines 782-813)

**Interfaces:**
- Consumes: `engine.Result.Healthy`, `engine.Result.Headline` (Task 2). `getRun`/`listRuns` already return the raw `Run` CR, so `status.healthy`/`status.headline` (Task 3) flow through them with no code change.
- Produces: `runResponse.Healthy *bool` (`json:"healthy,omitempty"`), `runResponse.Headline string` (`json:"headline,omitempty"`).

- [ ] **Step 1: Write the failing test**

Add to `internal/server/server_test.go` (mirror an existing `runUseCase` test's harness — the fake `Execute` and `RunSource`; set the fake `Execute` to return an `engine.Result` with a verdict):

```go
func TestRunUseCaseResponseIncludesVerdict(t *testing.T) {
	healthy := false
	// Configure the test server's Execute to return this result. Reuse the
	// existing harness helper; the key is the returned engine.Result:
	//   engine.Result{Phase: engine.PhaseSucceeded, Summary: "s",
	//                 Healthy: &healthy, Headline: "CrashLoopBackOff"}
	// POST /api/v1/usecases/{name}/run and decode the body into:
	var body struct {
		Phase    string `json:"phase"`
		Healthy  *bool  `json:"healthy"`
		Headline string `json:"headline"`
	}
	// ... perform the request against the harness, decode into &body ...
	if body.Healthy == nil || *body.Healthy != false {
		t.Errorf("healthy = %v, want false", body.Healthy)
	}
	if body.Headline != "CrashLoopBackOff" {
		t.Errorf("headline = %q, want CrashLoopBackOff", body.Headline)
	}
	_ = healthy
}
```

(Fill the `...` from the neighbouring `runUseCase` test in this file — copy its request/harness setup verbatim, only changing the fake `engine.Result` to carry `Healthy`/`Headline`. The plan cannot see that harness; the implementer copies the sibling test's scaffolding.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestRunUseCaseResponseIncludesVerdict -v`
Expected: FAIL — decoded `healthy`/`headline` are zero because `runResponse` has no such fields.

- [ ] **Step 3a: Add fields to runResponse**

In `internal/server/server.go`, extend `runResponse` (lines 239-245):

```go
type runResponse struct {
	Run      string              `json:"run"`
	Phase    string              `json:"phase"`
	Summary  string              `json:"summary,omitempty"`
	Healthy  *bool               `json:"healthy,omitempty"`
	Headline string              `json:"headline,omitempty"`
	Warning  string              `json:"warning,omitempty"`
	Steps    []engine.StepResult `json:"steps,omitempty"`
}
```

- [ ] **Step 3b: Populate them in runUseCase**

Replace the `resp := runResponse{...}` literal (lines 294-296) with:

```go
	resp := runResponse{
		Run: run.Name, Phase: res.Phase, Summary: res.Summary,
		Healthy: res.Healthy, Headline: res.Headline, Warning: res.Warning,
	}
```

- [ ] **Step 3c: Update openapi.yaml**

In `RunResponse.properties` (after `summary:`, before `warning:`), add:

```yaml
        healthy:
          type: boolean
          nullable: true
          description: >
            Health verdict from the summarizer: true = healthy, false =
            unhealthy, absent/null = unknown. Advisory; never affects phase.
        headline:
          type: string
          description: One-line reason for healthy (<=120 chars). Omitted when unknown.
```

In `RunStatus.properties` (after `summary:`, before `note:`), add the identical `healthy:` and `headline:` blocks.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/server/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go openapi.yaml
git commit -m "feat(server): expose health verdict on run response + openapi"
```

---

## Task 5: Docs + full verification

**Files:**
- Modify: `ARCHITECTURE.md`

- [ ] **Step 1: Document the verdict**

Add a short paragraph to `ARCHITECTURE.md` where the summary flow is described:

```markdown
### Health verdict

Every run carries an optional structured verdict alongside the prose summary:
`healthy` (true / false / unknown) and a one-line `headline`. The summarizer
appends a fixed instruction to the prompt asking the model to begin its reply
with a `VERDICT:` line; kato parses that line (regex, no JSON mode) into the
fields and strips it from the stored summary. The verdict is **advisory** — it
is derived from the LLM only and never affects a run's phase, step outcomes, or
`when`/`$(steps.x.y)` evaluation. A missing or malformed line degrades to
`unknown` with the run still succeeding.
```

- [ ] **Step 2: Full build, vet, and test**

Run: `go build ./... && go vet ./... && make test`
Expected: all PASS.

- [ ] **Step 3: Sanity-check the CRD + openapi are in sync**

Run: `grep -n "healthy" config/crd/bases/kato.zufardhiyaulhaq.com_runs.yaml charts/kato/crds/kato.zufardhiyaulhaq.com_runs.yaml openapi.yaml`
Expected: `healthy` present in all three.

- [ ] **Step 4: Commit**

```bash
git add ARCHITECTURE.md
git commit -m "docs: describe the run health verdict"
```

---

## Self-Review

- **Spec coverage:** verdict fields (Task 3), first-line `VERDICT:` convention + one-shot example + parser (Tasks 1-2), non-JSON/portable (Task 1), non-fatal degrade-to-unknown (Task 2 `Execute` unchanged failure path + parser total-ness), 120-char rune-safe cap (Task 1), API response + `GET /runs` (Task 4, latter free via raw Run), CRD persistence + deepcopy + chart copy (Task 3), openapi (Task 4), docs (Task 5). All spec sections map to a task.
- **Placeholder scan:** the only prose-not-code steps are Task 4 Step 1 (test harness copied from a sibling test — explicitly called out because the plan can't see that file) and Task 2 Step 3c (a `grep`-driven migration of unknown-count fakes). Both give exact instructions and the exact wrapping to apply. No `TBD`/`add error handling`/`similar to`.
- **Type consistency:** `SummaryOutput{Summary, Healthy *bool, Headline, ModelConfig}` defined in Task 2 and consumed identically in `Execute`, `BuildRunStatus` (Task 3), `runResponse` (Task 4). `parseVerdict` signature identical across Tasks 1-2. `RunStatus.Healthy *bool` / `Headline string` identical in Tasks 3-4 and openapi.
