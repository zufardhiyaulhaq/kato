{% raw %}
# kato Direct Method Run Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `POST /api/v1/methods/{name}/run` — execute one built-in method directly with caller params and return its outputs; stateless (no Run CRD, no LLM), with its own concurrency limiter.

**Architecture:** A thin handler in `internal/server` beside `runUseCase`: resolve the method from the existing registry, validate params with the existing `methods.ValidateParams`, gate on a new dedicated semaphore, execute with a `KATO_STEP_TIMEOUT`-bounded context and the server's `methods.Deps`, and map the result to a new lowercase-JSON view (`outcome`/`outputs`/`error`). A method-level failure is HTTP 200 with `outcome:"failed"` — HTTP errors are reserved for caller mistakes (400/404/429/500).

**Tech Stack:** Go, net/http (Go 1.22 method-prefixed mux), httptest table tests.

Spec: `docs/superpowers/specs/2026-07-22-kato-method-run-design.md`

## Global Constraints

- **Go toolchain:** prefix every `go`/`make` invocation with `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go` (bare `go` on PATH has a mismatched GOROOT).
- **DO NOT COMMIT.** Stage with `git add` only. The user commits their own work. Every "Stage" step means `git add`, never `git commit`.
- **Do NOT create CLAUDE.md.**
- **Stateless (verbatim from spec):** no Run CRD is created, no LLM is called, nothing is persisted. The response carries all of the method's outputs — no output filtering.
- **Method failure is 200:** a method returning an error yields `200 {"outcome":"failed","error":...,"outputs":{}}`. Only caller mistakes get error statuses: 400 invalid JSON / bad params, 404 unknown method, 429 limiter full, 500 internal.
- **Dedicated limiter:** `KATO_METHOD_MAX_CONCURRENT` (default 10), fully independent of `KATO_MAX_CONCURRENT`. Do not touch the usecase semaphore.
- **New view type, lowercase JSON keys** (`outcome`, `outputs`, `error`) — do NOT reuse `engine.StepResult` (its capitalized keys are a documented wart; don't propagate it).
- **Param validation reuses `methods.ValidateParams`** (presence of required params, rejection of unknown keys) — do not invent a second validator.
- **Body optional:** a missing/empty request body means empty params (unlike `runUseCase`, which 400s on an empty body — that asymmetry is intentional here because methods commonly take zero params, e.g. `check_apiserver`, `list_nodes`).

---

## Task 1: Config — `KATO_METHOD_MAX_CONCURRENT`

**Files:**
- Modify: `internal/config/config.go` (struct + `Load`)
- Test: `internal/config/config_test.go` (create if absent; append otherwise)

**Interfaces:**
- Produces: `config.Config.MethodMaxConcurrent int` — read by `cmd/kato/main.go` in Task 3. Default 10 via `getInt("KATO_METHOD_MAX_CONCURRENT", 10)` (existing helper: any unset/unparsable value falls back to the default).

- [ ] **Step 1: Write the failing test**

Check whether `internal/config/config_test.go` exists. If it does, append the test function (keep the existing package clause); if not, create the file:

```go
package config

import "testing"

// KATO_METHOD_MAX_CONCURRENT configures the direct-method-run limiter,
// independent of KATO_MAX_CONCURRENT; unset falls back to 10.
func TestMethodMaxConcurrent(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		if got := Load().MethodMaxConcurrent; got != 10 {
			t.Errorf("MethodMaxConcurrent = %d, want 10", got)
		}
	})
	t.Run("from env", func(t *testing.T) {
		t.Setenv("KATO_METHOD_MAX_CONCURRENT", "3")
		if got := Load().MethodMaxConcurrent; got != 3 {
			t.Errorf("MethodMaxConcurrent = %d, want 3", got)
		}
	})
	t.Run("independent of KATO_MAX_CONCURRENT", func(t *testing.T) {
		t.Setenv("KATO_MAX_CONCURRENT", "99")
		if got := Load().MethodMaxConcurrent; got != 10 {
			t.Errorf("MethodMaxConcurrent = %d, want 10 (must not read KATO_MAX_CONCURRENT)", got)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/config/ -run TestMethodMaxConcurrent -v`
Expected: FAIL — compile error `Load().MethodMaxConcurrent undefined`.

- [ ] **Step 3: Add the field and default**

In `internal/config/config.go`, add to the `Config` struct after `MaxConcurrent int`:

```go
	// MethodMaxConcurrent bounds concurrent direct method runs
	// (POST /api/v1/methods/{name}/run); independent of MaxConcurrent so
	// cheap exploratory method calls and long UseCase runs cannot starve
	// each other.
	MethodMaxConcurrent int
```

And in `Load()`, after the `MaxConcurrent:` line:

```go
		MethodMaxConcurrent: getInt("KATO_METHOD_MAX_CONCURRENT", 10),
```

- [ ] **Step 4: Run test to verify it passes**

Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/config/ -run TestMethodMaxConcurrent -v`
Expected: PASS (all three subtests).

- [ ] **Step 5: Stage**

```bash
git add internal/config/config.go internal/config/config_test.go
```
(Stage only — do not commit.)

---

## Task 2: Server handler — `POST /api/v1/methods/{name}/run`

**Files:**
- Modify: `internal/server/server.go` (Server struct fields, `Handler()` route + init, new request/response types, `runMethod` handler)
- Test: `internal/server/method_run_test.go` (create)

**Interfaces:**
- Consumes: `methods.Registry.Get(name) (Method, bool)`, `methods.ValidateParams(m, given) error`, `methods.Outputs` (`map[string]any`), `methods.Deps`, existing `writeJSON`/`writeErr` helpers.
- Produces (Task 3 wires these): new `Server` fields `Deps methods.Deps`, `StepTimeout time.Duration`, `MethodMaxConcurrent int`; route `POST /api/v1/methods/{name}/run`; wire types `methodRunRequest{Params map[string]string}` / `methodRunResponse{Outcome, Outputs, Error}` with JSON keys `outcome`, `outputs`, `error`.

- [ ] **Step 1: Write the failing tests**

Create `internal/server/method_run_test.go`. It uses purpose-built fake methods registered in a fresh `methods.NewRegistry()` (NOT `methods.Builtin()` — real methods would need a live cluster). The fake implements `methods.Method` and, via `ListOutputs`, `methods.ListProducer`:

```go
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

// fakeMethod is a scriptable methods.Method (and ListProducer when lists is set).
type fakeMethod struct {
	name    string
	params  []methods.Param
	outputs []methods.OutputField
	lists   []methods.ListOutputField
	run     func(ctx context.Context, deps methods.Deps, params map[string]string) (methods.Outputs, error)
}

func (f *fakeMethod) Name() string                          { return f.name }
func (f *fakeMethod) Description() string                   { return "fake " + f.name }
func (f *fakeMethod) Params() []methods.Param               { return f.params }
func (f *fakeMethod) OutputFields() []methods.OutputField   { return f.outputs }
func (f *fakeMethod) ListOutputs() []methods.ListOutputField { return f.lists }
func (f *fakeMethod) Run(ctx context.Context, deps methods.Deps, p map[string]string) (methods.Outputs, error) {
	return f.run(ctx, deps, p)
}

// newMethodServer builds a Server with only the given fakes registered.
func newMethodServer(t *testing.T, maxConcurrent int, fakes ...*fakeMethod) http.Handler {
	t.Helper()
	reg := methods.NewRegistry()
	for _, f := range fakes {
		reg.Register(f)
	}
	s := &Server{
		Registry:            reg,
		MethodMaxConcurrent: maxConcurrent,
		StepTimeout:         time.Second,
	}
	return s.Handler()
}

func postMethodRun(h http.Handler, method, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(http.MethodPost, "/api/v1/methods/"+method+"/run", nil)
	} else {
		r = httptest.NewRequest(http.MethodPost, "/api/v1/methods/"+method+"/run", strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Success: params reach the method; scalar and list outputs come back under
// "outputs" with outcome "completed".
func TestRunMethod_Success(t *testing.T) {
	var gotParams map[string]string
	fake := &fakeMethod{
		name:   "pod_status",
		params: []methods.Param{{Name: "namespace", Required: true}, {Name: "pod", Required: true}},
		run: func(_ context.Context, _ methods.Deps, p map[string]string) (methods.Outputs, error) {
			gotParams = p
			return methods.Outputs{
				"phase":    "Running",
				"restarts": int64(3),
				"pods":     []map[string]any{{"name": "api-0"}},
			}, nil
		},
	}
	w := postMethodRun(newMethodServer(t, 4, fake),
		"pod_status", `{"params":{"namespace":"payments","pod":"api-0"}}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if gotParams["namespace"] != "payments" || gotParams["pod"] != "api-0" {
		t.Errorf("params = %v, want namespace=payments pod=api-0", gotParams)
	}
	var resp struct {
		Outcome string         `json:"outcome"`
		Outputs map[string]any `json:"outputs"`
		Error   string         `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Outcome != "completed" || resp.Error != "" {
		t.Errorf("outcome = %q error = %q, want completed/empty", resp.Outcome, resp.Error)
	}
	if resp.Outputs["phase"] != "Running" {
		t.Errorf("outputs.phase = %v, want Running", resp.Outputs["phase"])
	}
	if _, ok := resp.Outputs["pods"].([]any); !ok {
		t.Errorf("outputs.pods = %T, want a JSON array", resp.Outputs["pods"])
	}
}

// A method-level failure is a finding, not an HTTP error: 200 + outcome failed.
func TestRunMethod_MethodFailureIs200(t *testing.T) {
	fake := &fakeMethod{
		name: "pod_status",
		run: func(context.Context, methods.Deps, map[string]string) (methods.Outputs, error) {
			return nil, context.DeadlineExceeded // any error; also covers nil outputs
		},
	}
	w := postMethodRun(newMethodServer(t, 4, fake), "pod_status", `{"params":{}}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Outcome string         `json:"outcome"`
		Outputs map[string]any `json:"outputs"`
		Error   string         `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Outcome != "failed" || resp.Error == "" {
		t.Errorf("outcome = %q error = %q, want failed + non-empty error", resp.Outcome, resp.Error)
	}
	if resp.Outputs == nil {
		t.Errorf(`outputs missing; want "outputs": {} even on failure`)
	}
}

// Caller mistakes: unknown method 404; missing required / unknown param /
// invalid JSON 400. The method must never execute on a 4xx.
func TestRunMethod_CallerErrors(t *testing.T) {
	executed := false
	fake := &fakeMethod{
		name:   "pod_status",
		params: []methods.Param{{Name: "namespace", Required: true}},
		run: func(context.Context, methods.Deps, map[string]string) (methods.Outputs, error) {
			executed = true
			return methods.Outputs{}, nil
		},
	}
	h := newMethodServer(t, 4, fake)

	cases := []struct {
		name, method, body string
		wantStatus         int
	}{
		{"unknown method", "nope", `{"params":{}}`, http.StatusNotFound},
		{"missing required param", "pod_status", `{"params":{}}`, http.StatusBadRequest},
		{"unknown param", "pod_status", `{"params":{"namespace":"x","bogus":"y"}}`, http.StatusBadRequest},
		{"invalid JSON", "pod_status", `{not json`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postMethodRun(h, tc.method, tc.body)
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", w.Code, tc.wantStatus, w.Body.String())
			}
			var e struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Error == "" {
				t.Errorf(`body = %s, want {"error": "..."}`, w.Body.String())
			}
		})
	}
	if executed {
		t.Error("method executed on a 4xx request; validation must run first")
	}
}

// An empty/missing body means empty params (methods with zero params are common).
func TestRunMethod_EmptyBodyIsEmptyParams(t *testing.T) {
	var gotParams map[string]string
	fake := &fakeMethod{
		name: "check_apiserver",
		run: func(_ context.Context, _ methods.Deps, p map[string]string) (methods.Outputs, error) {
			gotParams = p
			return methods.Outputs{"healthy": true}, nil
		},
	}
	w := postMethodRun(newMethodServer(t, 4, fake), "check_apiserver", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if gotParams == nil || len(gotParams) != 0 {
		t.Errorf("params = %v, want empty non-nil map", gotParams)
	}
}

// The dedicated limiter: cap 1, one in-flight run -> second call 429.
// The usecase semaphore is untouched.
func TestRunMethod_LimiterFull(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	fake := &fakeMethod{
		name: "slow",
		run: func(context.Context, methods.Deps, map[string]string) (methods.Outputs, error) {
			started <- struct{}{}
			<-release
			return methods.Outputs{}, nil
		},
	}
	h := newMethodServer(t, 1, fake)

	firstDone := make(chan *httptest.ResponseRecorder)
	go func() { firstDone <- postMethodRun(h, "slow", "") }()
	<-started // first request is now inside the method, holding the semaphore

	w := postMethodRun(h, "slow", "")
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("second call status = %d, want 429; body: %s", w.Code, w.Body.String())
	}

	close(release)
	if first := <-firstDone; first.Code != http.StatusOK {
		t.Errorf("first call status = %d, want 200", first.Code)
	}
}

// A method exceeding StepTimeout surfaces as outcome failed (deadline error),
// still HTTP 200.
func TestRunMethod_Timeout(t *testing.T) {
	fake := &fakeMethod{
		name: "sleepy",
		run: func(ctx context.Context, _ methods.Deps, _ map[string]string) (methods.Outputs, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return methods.Outputs{}, nil
			}
		},
	}
	reg := methods.NewRegistry()
	reg.Register(fake)
	s := &Server{Registry: reg, MethodMaxConcurrent: 1, StepTimeout: 30 * time.Millisecond}
	w := postMethodRun(s.Handler(), "sleepy", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Outcome string `json:"outcome"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Outcome != "failed" || !strings.Contains(resp.Error, "context deadline exceeded") {
		t.Errorf("outcome = %q error = %q, want failed + deadline error", resp.Outcome, resp.Error)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/server/ -run TestRunMethod -v`
Expected: FAIL — compile error (`Server` has no field `MethodMaxConcurrent` / `StepTimeout`).

- [ ] **Step 3: Implement the handler**

In `internal/server/server.go`:

**3a.** Add imports `"fmt"`, `"io"` to the import block (`context`, `encoding/json`, `errors`, `net/http`, `time` are already there).

**3b.** Add fields to the `Server` struct after `MaxConcurrent int`:

```go
	// Deps and StepTimeout serve direct method runs (POST /api/v1/methods/{name}/run):
	// the same method dependencies and per-call timeout the engine uses for steps.
	Deps        methods.Deps
	StepTimeout time.Duration
	// MethodMaxConcurrent bounds in-flight direct method runs, independent of
	// MaxConcurrent (which caps UseCase runs).
	MethodMaxConcurrent int
```

and after `sem chan struct{}`:

```go
	methodSem chan struct{}
```

**3c.** In `Handler()`, after the `s.sem` init:

```go
	if s.methodSem == nil {
		s.methodSem = make(chan struct{}, max(s.MethodMaxConcurrent, 0))
	}
```

and register the route after the `GET /api/v1/methods` line:

```go
	mux.HandleFunc("POST /api/v1/methods/{name}/run", s.runMethod)
```

**3d.** Add the wire types and handler after `listMethods` (before `runRequest`):

```go
type methodRunRequest struct {
	Params map[string]string `json:"params"`
}

// methodRunResponse is the direct-method-run view. Deliberately lowercase keys
// (unlike engine.StepResult): a new endpoint should not inherit that wart.
type methodRunResponse struct {
	Outcome string          `json:"outcome"` // "completed" | "failed"
	Outputs methods.Outputs `json:"outputs"` // scalars + list outputs as arrays
	Error   string          `json:"error,omitempty"`
}

// runMethod executes one method directly: stateless — no Run CRD, no LLM.
// A method-level failure is a finding (200 + outcome "failed"); HTTP errors
// are reserved for caller mistakes.
func (s *Server) runMethod(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	m, ok := s.Registry.Get(name)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("unknown method %q", name))
		return
	}

	// Body is optional: zero-param methods are common, so EOF means no params.
	var req methodRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Params == nil {
		req.Params = map[string]string{}
	}
	if err := methods.ValidateParams(m, req.Params); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Dedicated limiter (never the usecase sem): non-blocking acquire.
	select {
	case s.methodSem <- struct{}{}:
		defer func() { <-s.methodSem }()
	default:
		writeErr(w, http.StatusTooManyRequests, "too many concurrent method runs")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.StepTimeout)
	defer cancel()
	outputs, err := m.Run(ctx, s.Deps, req.Params)
	if err != nil {
		writeJSON(w, http.StatusOK, methodRunResponse{
			Outcome: "failed", Outputs: methods.Outputs{}, Error: err.Error(),
		})
		return
	}
	if outputs == nil {
		outputs = methods.Outputs{}
	}
	writeJSON(w, http.StatusOK, methodRunResponse{Outcome: "completed", Outputs: outputs})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test ./internal/server/ -run TestRunMethod -v`
Expected: PASS (all six test functions).

- [ ] **Step 5: Run the full server suite (regression guard)**

Run: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test -count=1 ./internal/server/`
Expected: `ok` — existing usecase/run tests unaffected (their `Server` literals gain zero-value new fields; `methodSem` caps at 0 there, which only matters for method-run routes they never hit).

- [ ] **Step 6: Stage**

```bash
git add internal/server/server.go internal/server/method_run_test.go
```
(Stage only — do not commit.)

---

## Task 3: Wiring, OpenAPI, docs, full verification

**Files:**
- Modify: `cmd/kato/main.go` (pass deps/timeouts to the server)
- Modify: `openapi.yaml` (new path + schemas)
- Modify: `docs/METHOD.md` (direct-run section)
- Modify: README env-var table(s) — locate by grep in Step 3

**Interfaces:**
- Consumes: `config.Config.MethodMaxConcurrent` (Task 1); `Server.Deps` / `Server.StepTimeout` / `Server.MethodMaxConcurrent` (Task 2).

- [ ] **Step 1: Wire main.go**

In `cmd/kato/main.go`, the engine construction currently builds deps inline:

```go
	eng := &engine.Engine{
		Deps:      methods.Deps{Kube: kubeClient, Metrics: metricsClient, Prober: methods.LocalProber{}},
		Registry:  reg,
		Summarize: sum.Summarize, StepTimeout: cfg.StepTimeout,
	}
```

Extract the deps so server and engine share one value, and pass the new server fields. Replace with:

```go
	deps := methods.Deps{Kube: kubeClient, Metrics: metricsClient, Prober: methods.LocalProber{}}
	eng := &engine.Engine{
		Deps:      deps,
		Registry:  reg,
		Summarize: sum.Summarize, StepTimeout: cfg.StepTimeout,
	}
```

and change the server literal from:

```go
	srv := &server.Server{
		UseCases: ucCache, Runs: st, Execute: eng.Execute,
		Registry: reg, MaxConcurrent: cfg.MaxConcurrent,
	}
```

to:

```go
	srv := &server.Server{
		UseCases: ucCache, Runs: st, Execute: eng.Execute,
		Registry: reg, MaxConcurrent: cfg.MaxConcurrent,
		Deps: deps, StepTimeout: cfg.StepTimeout,
		MethodMaxConcurrent: cfg.MethodMaxConcurrent,
	}
```

Build: `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go build ./...`
Expected: clean.

- [ ] **Step 2: Update openapi.yaml**

Open `openapi.yaml` and add the new path alongside the existing `/api/v1/methods` entry (match the file's existing indentation and description style):

```yaml
  /api/v1/methods/{name}/run:
    post:
      summary: Execute one built-in method directly
      description: >
        Runs a single method with the given params and returns its outputs.
        Stateless: no Run is persisted and no LLM summary is produced. A
        method-level failure (e.g. pod not found, probe refused) is a
        legitimate finding and returns 200 with outcome "failed"; HTTP error
        statuses are reserved for caller mistakes. Bounded by
        KATO_STEP_TIMEOUT; concurrency-capped by KATO_METHOD_MAX_CONCURRENT
        (429 when full), independent of the UseCase run cap.
      parameters:
        - name: name
          in: path
          required: true
          schema:
            type: string
      requestBody:
        required: false
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/MethodRunRequest'
      responses:
        '200':
          description: Method executed (outcome may be completed or failed)
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/MethodRunResponse'
        '400':
          description: Invalid JSON, missing required param, or unknown param
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Error'
        '404':
          description: Unknown method
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Error'
        '429':
          description: Too many concurrent method runs
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Error'
        '500':
          description: Internal error
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Error'
```

And under `components.schemas`:

```yaml
    MethodRunRequest:
      type: object
      properties:
        params:
          type: object
          additionalProperties:
            type: string
          description: Method params; all values are strings. Omitted = no params.
    MethodRunResponse:
      type: object
      required: [outcome, outputs]
      properties:
        outcome:
          type: string
          enum: [completed, failed]
        outputs:
          type: object
          description: >
            All of the method's outputs: scalars (string/int/bool) plus each
            declared list output as an array of item records. Empty object
            when the method failed. Keys are lowercase (this view does not
            inherit StepResult's capitalized-key wart).
        error:
          type: string
          description: Present only when outcome is "failed".
```

Notes: if the file has no `Error` schema, reuse whatever the existing error responses reference (grep for `error` under `components`); mirror the existing pattern rather than inventing a new one.

Validate YAML parses: `python3 -c "import yaml; yaml.safe_load(open('openapi.yaml'))" && echo OK`
Expected: `OK`.

- [ ] **Step 3: Update docs**

**METHOD.md** — append a short section to `docs/METHOD.md` (match its heading style):

```markdown
## Running a method directly

`POST /api/v1/methods/{name}/run` executes one method ad hoc, outside any
UseCase — body `{"params": {...}}` (all string values; body optional for
zero-param methods), response `{"outcome": "completed|failed", "outputs": {...},
"error": "..."}`. It is stateless: no Run is persisted and no LLM summary is
produced. All declared outputs are returned — scalars plus list outputs as
arrays. A method-level failure (pod not found, probe refused) returns 200 with
`outcome: "failed"`; 400/404/429 are reserved for bad params, unknown methods,
and the `KATO_METHOD_MAX_CONCURRENT` cap (default 10, independent of
`KATO_MAX_CONCURRENT`). Calls are bounded by `KATO_STEP_TIMEOUT`.
```

**README env table(s)** — locate where `KATO_MAX_CONCURRENT` is documented:

Run: `grep -rn "KATO_MAX_CONCURRENT" README.md docs/ charts/ DEVELOPMENT.md 2>/dev/null`

In each prose/table occurrence that documents env vars (skip generated files if a `.gotmpl` source exists — edit the `.gotmpl` and regenerate instead), add a row/line directly after `KATO_MAX_CONCURRENT`:

```
| `KATO_METHOD_MAX_CONCURRENT` | `10` | max in-flight direct method runs (`POST /api/v1/methods/{name}/run`); 429 when full. Independent of `KATO_MAX_CONCURRENT`. |
```

(Adapt the row to the table's actual column layout. If a `charts/kato/README.md.gotmpl` was edited, run `GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go make readme`; if `helm-docs` is unavailable, hand-edit the generated READMEs to match.)

- [ ] **Step 4: Full verification**

Run:
```bash
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go build ./...
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go test -count=1 ./...
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go go vet ./...
GOROOT=/Users/zufardhiyaulhaq/.asdf/installs/golang/1.25.2/go gofmt -l internal/server/ internal/config/ cmd/kato/
```
Expected: build clean; all packages `ok`; vet clean; `gofmt -l` prints nothing.

- [ ] **Step 5: Stage**

```bash
git add cmd/kato/main.go openapi.yaml docs/METHOD.md
# plus whichever README/gotmpl files Step 3's grep led you to edit
```
(Stage only — do not commit. Present the staged change set to the user for them to commit.)

---

## Self-Review notes (for the executor)

- **Spec coverage:** endpoint shape + response view (Task 2), dedicated limiter env (Task 1) + wiring (Task 3), statelessness (no store/summarizer touched anywhere — enforced by the handler simply not calling them), param validation via `ValidateParams` (Task 2), timeout via `StepTimeout` (Tasks 2–3), docs/openapi (Task 3). Non-goals (no filtering, no persistence, no auth) require no code by design.
- **Type consistency:** `Server.Deps` / `Server.StepTimeout` / `Server.MethodMaxConcurrent` are named identically in Task 2 (definition) and Task 3 (wiring); `config.MethodMaxConcurrent` in Task 1 → Task 3. `methodRunResponse` JSON keys `outcome`/`outputs`/`error` match every test's decode struct.
- **Watch items:** (1) `max()` builtin needs Go ≥1.21 — already used at `server.go:51`, so fine. (2) `runUseCase` 400s on an empty body; `runMethod` deliberately does not (EOF tolerated) — do not "unify" them. (3) Existing server tests construct `Server` literals without the new fields; zero values are safe because those tests never hit the method-run route.
{% endraw %}
