# `check_apiserver` Method Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **⚠️ STANDING INSTRUCTION — DO NOT COMMIT.** Never run `git commit` / `git add`. Leave all changes in the working tree. Every "Commit" step below is replaced by: run `go build ./... && go vet ./...` and confirm clean, then move on. Do not create branches or stage files.

**Goal:** Add a `check_apiserver` method that reports the health of the API server kato is connected to, by reading `/livez` or `/healthz` (verbose) through kato's authenticated REST client and parsing which individual health checks failed.

**Architecture:** A single new method in the `check_` family. It calls `deps.Kube.Discovery().RESTClient().Get().AbsPath("/"+endpoint).Param("verbose","true").Do(ctx)`, reads the HTTP status code and verbose body, and maps them to scalar outputs (`healthy`, `statusCode`, `failedCount`, `error`) plus a `ListProducer` list output (`failedChecks`, items `{name}`). A degraded or unreachable apiserver is a finding (nil Go error); only param mistakes are Go errors. No changes to `Deps` or `cmd/kato` — it reuses the existing `deps.Kube` and the package-internal `parseProbeTimeout` helper.

**Tech Stack:** Go, `k8s.io/client-go` (`kubernetes`, `rest`, discovery REST client), `net/http/httptest` for tests.

**Reference spec:** `docs/superpowers/specs/2026-06-15-kato-check-apiserver-design.md`

---

## File Structure

- **Create** `internal/methods/check_apiserver.go` — the `checkAPIServer` method, plus the helpers `parseEndpoint` and `parseFailedChecks`, and the `init()` registration. One file, one responsibility (API-server health).
- **Create** `internal/methods/check_apiserver_test.go` — helper table tests + method tests (httptest-backed real clientset).
- **Modify** `docs/METHOD.md` — add the index row and the `### check_apiserver` section.
- **No change** to `internal/methods/method.go` (`Deps.Kube` already exists) or `cmd/kato/main.go` (no new dependency).

**Reused existing code (do not redefine):**
- `parseProbeTimeout(s string) (time.Duration, error)` — in `internal/methods/probe_tcp.go`. Unset → `5s`; else a Go duration `> 0`; invalid/`<=0` → error.
- `capItems` / `parseMaxListItems` / `defaultMaxListItems` — in `internal/methods/truncate.go`. **Not used by this method.** List capping is per-method (a method exposes a `maxListItems` param and calls `capItems` itself, as `list_failing_pods`/`list_pods` do) — it is **not** applied automatically by the engine. `check_apiserver` deliberately omits a `maxListItems` param in v1: the failing-check set is naturally small (bounded by the cluster's registered health checks), so it just declares the `failedChecks` list via `ListOutputs()` with no cap.

---

## Task 1: Pure helpers — `parseEndpoint` and `parseFailedChecks`

**Files:**
- Create: `internal/methods/check_apiserver.go`
- Test: `internal/methods/check_apiserver_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/methods/check_apiserver_test.go`:

{% raw %}
```go
package methods

import (
	"reflect"
	"testing"
)

func TestParseEndpoint(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "livez", false},     // unset defaults to livez
		{"livez", "livez", false},
		{"healthz", "healthz", false},
		{"readyz", "", true},     // rejected in v1
		{"bogus", "", true},
	}
	for _, c := range cases {
		got, err := parseEndpoint(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseEndpoint(%q): expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseEndpoint(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("parseEndpoint(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseFailedChecks(t *testing.T) {
	body := "[+]ping ok\n" +
		"[+]log ok\n" +
		"[-]etcd failed: reason withheld\n" +
		"[+]poststarthook/start-service-ca ok\n" +
		"[-]poststarthook/bootstrap-controller failed\n" +
		"livez check failed\n"
	got := parseFailedChecks([]byte(body))
	want := []map[string]any{
		{"name": "etcd"},
		{"name": "poststarthook/bootstrap-controller"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseFailedChecks = %#v, want %#v", got, want)
	}
}

func TestParseFailedChecksAllHealthy(t *testing.T) {
	body := "[+]ping ok\n[+]etcd ok\nlivez check passed\n"
	got := parseFailedChecks([]byte(body))
	if len(got) != 0 {
		t.Errorf("parseFailedChecks on healthy body = %#v, want empty", got)
	}
	// Must be non-nil so it serializes as [] not null.
	if got == nil {
		t.Error("parseFailedChecks returned nil, want non-nil empty slice")
	}
}
```
{% endraw %}

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/methods/ -run 'TestParseEndpoint|TestParseFailedChecks' -v`
Expected: FAIL — `undefined: parseEndpoint`, `undefined: parseFailedChecks`.

- [ ] **Step 3: Write the helpers**

Create `internal/methods/check_apiserver.go`:

{% raw %}
```go
package methods

import (
	"fmt"
	"strings"
)

// parseEndpoint validates the endpoint param: unset -> "livez"; "livez"/"healthz"
// accepted; anything else (including "readyz", out of scope in v1) is a param error.
func parseEndpoint(s string) (string, error) {
	switch s {
	case "":
		return "livez", nil
	case "livez", "healthz":
		return s, nil
	default:
		return "", fmt.Errorf("param endpoint: must be livez or healthz, got %q", s)
	}
}

// parseFailedChecks extracts the names of failing health checks from a verbose
// apiserver health body. Each failing check is a line beginning with "[-]"; the
// name is the text between "[-]" and the first space (e.g. "[-]etcd failed: x" ->
// "etcd"). Returns a non-nil (possibly empty) slice so it serializes as [] not null.
func parseFailedChecks(body []byte) []map[string]any {
	out := []map[string]any{}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "[-]") {
			continue
		}
		rest := line[len("[-]"):]
		name := rest
		if i := strings.IndexByte(rest, ' '); i >= 0 {
			name = rest[:i]
		}
		if name == "" {
			continue
		}
		out = append(out, map[string]any{"name": name})
	}
	return out
}
```
{% endraw %}

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/methods/ -run 'TestParseEndpoint|TestParseFailedChecks' -v`
Expected: PASS (all three tests).

- [ ] **Step 5: Commit** — _SKIP per standing instruction._ Instead run `go build ./... && go vet ./...` and confirm clean.

---

## Task 2: The `check_apiserver` method

**Files:**
- Modify: `internal/methods/check_apiserver.go` (add struct, interface methods, `init`)
- Test: `internal/methods/check_apiserver_test.go` (add method tests)

- [ ] **Step 1: Write the failing tests**

Append to `internal/methods/check_apiserver_test.go`. Add the needed imports to the existing `import` block: `context`, `net/http`, `net/http/httptest`, `k8s.io/client-go/kubernetes`, `k8s.io/client-go/rest`.

{% raw %}
```go
// healthHandler serves /livez and /healthz with a chosen status code and body,
// and records the last requested path so tests can assert routing.
type healthHandler struct {
	code     int
	body     string
	gotPath  string
	gotQuery string
}

func (h *healthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.gotPath = r.URL.Path
	h.gotQuery = r.URL.RawQuery
	w.WriteHeader(h.code)
	_, _ = w.Write([]byte(h.body))
}

// newAPIServerClient builds a real clientset pointed at srv.
func newAPIServerClient(t *testing.T, srv *httptest.Server) kubernetes.Interface {
	t.Helper()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("NewForConfig: %v", err)
	}
	return client
}

func TestCheckAPIServerHealthy(t *testing.T) {
	h := &healthHandler{code: 200, body: "[+]ping ok\n[+]etcd ok\nlivez check passed\n"}
	srv := httptest.NewServer(h)
	defer srv.Close()

	m, ok := Builtin().Get("check_apiserver")
	if !ok {
		t.Fatal("check_apiserver not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: newAPIServerClient(t, srv)}, map[string]string{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["healthy"] != true || out["statusCode"] != int64(200) ||
		out["failedCount"] != int64(0) || out["error"] != "" {
		t.Errorf("outputs = %#v", out)
	}
	fc, _ := out["failedChecks"].([]map[string]any)
	if len(fc) != 0 {
		t.Errorf("failedChecks = %#v, want empty", fc)
	}
	// default endpoint is livez, verbose=true
	if h.gotPath != "/livez" || h.gotQuery != "verbose=true" {
		t.Errorf("requested %q?%q, want /livez?verbose=true", h.gotPath, h.gotQuery)
	}
}

func TestCheckAPIServerDegradedIsFindingNotError(t *testing.T) {
	body := "[+]ping ok\n[-]etcd failed: timeout\n[-]poststarthook/x failed\nlivez check failed\n"
	srv := httptest.NewServer(&healthHandler{code: 500, body: body})
	defer srv.Close()

	m, _ := Builtin().Get("check_apiserver")
	out, err := m.Run(context.Background(), Deps{Kube: newAPIServerClient(t, srv)}, map[string]string{})
	if err != nil {
		t.Fatalf("degraded apiserver must not be a Go error: %v", err)
	}
	if out["healthy"] != false || out["statusCode"] != int64(500) || out["failedCount"] != int64(2) {
		t.Errorf("outputs = %#v", out)
	}
	fc, _ := out["failedChecks"].([]map[string]any)
	if len(fc) != 2 || fc[0]["name"] != "etcd" || fc[1]["name"] != "poststarthook/x" {
		t.Errorf("failedChecks = %#v", fc)
	}
}

func TestCheckAPIServerHealthzEndpoint(t *testing.T) {
	h := &healthHandler{code: 200, body: "[+]ping ok\nhealthz check passed\n"}
	srv := httptest.NewServer(h)
	defer srv.Close()

	m, _ := Builtin().Get("check_apiserver")
	_, err := m.Run(context.Background(), Deps{Kube: newAPIServerClient(t, srv)},
		map[string]string{"endpoint": "healthz"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.gotPath != "/healthz" {
		t.Errorf("requested %q, want /healthz", h.gotPath)
	}
}

func TestCheckAPIServerUnreachableIsFindingNotError(t *testing.T) {
	srv := httptest.NewServer(&healthHandler{code: 200, body: "ok"})
	client := newAPIServerClient(t, srv)
	srv.Close() // now connections are refused

	m, _ := Builtin().Get("check_apiserver")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"timeout": "2s"})
	if err != nil {
		t.Fatalf("unreachable apiserver must not be a Go error: %v", err)
	}
	if out["healthy"] != false || out["statusCode"] != int64(0) || out["error"] == "" {
		t.Errorf("outputs = %#v", out)
	}
	fc, _ := out["failedChecks"].([]map[string]any)
	if fc == nil || len(fc) != 0 {
		t.Errorf("failedChecks = %#v, want non-nil empty", fc)
	}
}

func TestCheckAPIServerParamErrors(t *testing.T) {
	srv := httptest.NewServer(&healthHandler{code: 200, body: "ok"})
	defer srv.Close()
	client := newAPIServerClient(t, srv)

	m, _ := Builtin().Get("check_apiserver")
	cases := map[string]map[string]string{
		"bad endpoint":  {"endpoint": "readyz"},
		"zero timeout":  {"timeout": "0s"},
		"bad timeout":   {"timeout": "soon"},
	}
	for name, params := range cases {
		if _, err := m.Run(context.Background(), Deps{Kube: client}, params); err == nil {
			t.Errorf("%s: expected param error, got nil", name)
		}
	}
}
```
{% endraw %}

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/methods/ -run TestCheckAPIServer -v`
Expected: FAIL — `check_apiserver not registered` (method type and `init` do not exist yet).

- [ ] **Step 3: Implement the method**

Add to `internal/methods/check_apiserver.go`. Extend the existing `import` block to include `context` (keep `fmt`, `strings`). Do **not** import `time`: `parseProbeTimeout` returns a `time.Duration` but it is bound with `:=` and never referenced by name here, so a `time` import would be unused and fail the build.

{% raw %}
```go
import (
	"context"
	"fmt"
	"strings"
)
```
{% endraw %}

Then append the method below the helpers:

{% raw %}
```go
type checkAPIServer struct{}

func (checkAPIServer) Name() string { return "check_apiserver" }
func (checkAPIServer) Description() string {
	return "Health of the connected API server via /livez or /healthz (verbose), with failing-check names"
}

func (checkAPIServer) Params() []Param {
	return []Param{
		{Name: "endpoint", Description: `which health path: "livez" (default) | "healthz"`},
		{Name: "timeout", Description: `request timeout as a Go duration (default "5s")`},
	}
}

func (checkAPIServer) OutputFields() []OutputField {
	return []OutputField{
		{Name: "healthy", Type: FieldBool, Description: "the endpoint reported healthy (HTTP 200)"},
		{Name: "statusCode", Type: FieldInt, Description: "HTTP status code; 0 if the API server was unreachable"},
		{Name: "failedCount", Type: FieldInt, Description: "number of failing health checks"},
		{Name: "error", Type: FieldString, Description: `unreachable reason (refused/timeout/DNS); "" otherwise`},
	}
}

func (checkAPIServer) ListOutputs() []ListOutputField {
	return []ListOutputField{{
		Name:        "failedChecks",
		Description: "names of the individual failing health checks, as named on that cluster",
		ItemFields: []OutputField{
			{Name: "name", Type: FieldString, Description: "failing health check name (e.g. etcd)"},
		},
	}}
}

func (checkAPIServer) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	endpoint, err := parseEndpoint(params["endpoint"])
	if err != nil {
		return nil, err
	}
	timeout, err := parseProbeTimeout(params["timeout"])
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result := deps.Kube.Discovery().RESTClient().
		Get().AbsPath("/" + endpoint).Param("verbose", "true").Do(ctx)

	var code int
	result.StatusCode(&code)

	// statusCode 0 means no HTTP response was received: the API server is
	// unreachable (refused/timeout/DNS). That is a finding, not a Go error.
	if code == 0 {
		msg := "api server unreachable"
		if e := result.Error(); e != nil {
			msg = e.Error()
		}
		return Outputs{
			"healthy":      false,
			"statusCode":   int64(0),
			"failedCount":  int64(0),
			"error":        msg,
			"failedChecks": []map[string]any{},
		}, nil
	}

	// An HTTP response (200 healthy / 500 degraded) was received. The verbose body
	// is present on both; the status code is authoritative, so Raw's error is ignored.
	body, _ := result.Raw()
	failed := parseFailedChecks(body)
	return Outputs{
		"healthy":      code == 200,
		"statusCode":   int64(code),
		"failedCount":  int64(len(failed)),
		"error":        "",
		"failedChecks": failed,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkAPIServer{}) }) }
```
{% endraw %}

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/methods/ -run TestCheckAPIServer -v`
Expected: PASS (all five method tests).

- [ ] **Step 5: Run the full methods suite + build/vet**

Run: `go test ./internal/methods/ && go build ./... && go vet ./...`
Expected: all PASS, no build/vet output.

- [ ] **Step 6: Commit** — _SKIP per standing instruction._ Confirm `go build ./... && go vet ./...` clean and move on.

---

## Task 3: Documentation in `docs/METHOD.md`

**Files:**
- Modify: `docs/METHOD.md`

- [ ] **Step 1: Add the index row**

In the `## Index` table, immediately **after** the `check_cronjob` row:

```
| [`check_cronjob`](#check_cronjob) | CronJob schedule, suspension, and recent-run times |
```

insert:

```
| [`check_apiserver`](#check_apiserver) | Health of the connected API server (`/livez` or `/healthz`) with failing-check names |
```

- [ ] **Step 2: Add the section**

The `## Batch` section ends with the `check_cronjob` method, immediately before `## Discovery`. Add a new `## Control plane` section between them. Find the `## Discovery` header line and insert this block immediately **before** it:

````markdown
## Control plane

### `check_apiserver`

Health of the API server kato is connected to, read through kato's **authenticated**
REST client (`/livez` or `/healthz` with `?verbose`). Reports a pass/fail signal plus the
**names of the failing health checks** — distro-agnostic, with no hardcoded component
list. An unhealthy (HTTP 500) or unreachable API server is a finding (`healthy: false`),
not a method failure; only invalid params are errors. Use it to gate a UseCase on "is the
control plane degraded?" and hand the failing-check names to the LLM as evidence.

Unlike `probe_http`, this uses kato's ServiceAccount (no `target`/`port`/TLS to wire) and
parses *which* checks failed. `/readyz` is intentionally not offered in v1.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `endpoint` | no | which health path: `livez` (default) \| `healthz` |
| `timeout` | no | request timeout as a Go duration (default `5s`) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `healthy` | bool | the endpoint reported healthy (HTTP 200) |
| `statusCode` | int | HTTP status code; `0` if the API server was unreachable |
| `failedCount` | int | number of failing health checks |
| `error` | string | unreachable reason (refused/timeout/DNS); `""` otherwise |

**List output `failedChecks`** (the individual `[-]…failed` checks)

| Item field | Type | Description |
|---|---|---|
| `name` | string | failing health check name, as named on that cluster (e.g. `etcd`, `poststarthook/…`) |

Gate on the scalars — `when: $(steps.<step>.healthy) == false` or a `failedCount`
threshold. The `failedChecks` list is recorded to the Run and sent to the LLM as part
of the step's outputs (when the step leaves `summaryFilter` unset, which records all
outputs), or fan out over the names with `forEach: $(steps.<step>.failedChecks)`, binding
`$(item.name)` in the step's `with`. Note: `summaryFilter` allowlists scalar outputs
only — a list output's name cannot be added to it.

---
````

- [ ] **Step 3: Verify the doc renders and links resolve**

Run: `grep -n "check_apiserver" docs/METHOD.md`
Expected: three matches — the index row, the section heading, and the index anchor target all present.

- [ ] **Step 4: Commit** — _SKIP per standing instruction._ Leave in working tree.

---

## Final verification (after all tasks)

- [ ] Run: `go test ./... ` — expected: all packages PASS (envtest-gated controller tests skip without `KUBEBUILDER_ASSETS`, which is fine).
- [ ] Run: `go build ./... && go vet ./...` — expected: clean, no output.
- [ ] Confirm `git status` shows only: `internal/methods/check_apiserver.go`, `internal/methods/check_apiserver_test.go`, `docs/METHOD.md`, and the two `docs/superpowers/` spec+plan files — all **unstaged** (not committed).
