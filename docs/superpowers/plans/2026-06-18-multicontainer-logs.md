# Multi-Container Pod Logs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **DO NOT COMMIT.** Standing user instruction: leave all changes in the working tree. The "Verify" step that ends each task replaces the usual commit step — never run `git add`/`git commit`.

**Goal:** Make `check_pod_logs` work on multi-container pods (auto-loop all containers when none is named) and expose container list outputs on `check_pod_status` and `describe_pod` so UseCases can `forEach` over containers.

**Architecture:** Three additive, independent changes inside `internal/methods/`. No engine changes (the engine already supports `forEach` over list outputs). The crashloop UseCase is fixed purely by the `check_pod_logs` change; no YAML edits. Changes are additive, so `go build ./... && go test ./...` stays green after every task.

**Tech Stack:** Go 1.24.8; `k8s.io/api/core/v1`; `k8s.io/client-go/kubernetes/fake` for tests. Spec: `docs/superpowers/specs/2026-06-18-multicontainer-logs-design.md`.

---

## File Structure

- `internal/methods/pod_logs.go` — auto-loop when `container` is empty; new pure helpers `aggregateContainerLogs`, `podContainerNames`, type `containerLog`.
- `internal/methods/pod_logs_test.go` — pure-aggregation test + Run-level multi/single/not-found tests.
- `internal/methods/pod_status.go` — `ListOutputs()` + populate a `containers` list output.
- `internal/methods/pod_status_test.go` — list-output test.
- `internal/methods/pod_describe.go` — `ListOutputs()` + populate a `containerList` list output; helper `containerItems`.
- `internal/methods/pod_describe_test.go` — list-output test.

**Engine fact (so list values are the right shape):** `internal/methods/method.go:57-59` — a list output's value in the `Outputs` map must be `[]map[string]any` (a list of item records), with each scalar item field stored as `string`, `int64`, or `bool` per its declared `FieldType`. The engine's `forEach` reads it via `raw.([]map[string]any)`. Mirror the proven pattern in `list_failing_pods.go:70-101`.

---

### Task 1: `check_pod_logs` auto-loops all containers when none is named

**Files:**
- Modify: `internal/methods/pod_logs.go`
- Test: `internal/methods/pod_logs_test.go`

**Behavior:** If `container` is set → one `GetLogs`, hard error on failure (unchanged). If `container` is empty → `Get` the pod, enumerate `Spec.Containers` then `Spec.InitContainers`; with 0–1 containers fetch once with no per-container header (preserves today's single-container output); with ≥2 containers fetch each (naming it explicitly, since the logs subresource rejects an empty container on a multi-container pod), tolerate per-container failures as inline notes, and concatenate with headers. Param parsing still happens first so bad-param errors are unchanged.

- [ ] **Step 1: Write the failing test for the pure aggregator**

Add to `internal/methods/pod_logs_test.go`. Also add `"fmt"` and `"strings"` to that file's import block (used here and in Step 5).

```go
func TestAggregateContainerLogs(t *testing.T) {
	got := aggregateContainerLogs([]containerLog{
		{name: "app", text: "line1\nline2"},
		{name: "istio-proxy", err: fmt.Errorf(`previous terminated container "istio-proxy" not found`)},
	})
	want := "=== container: app ===\nline1\nline2\n\n" +
		"=== container: istio-proxy ===\n(no logs: previous terminated container \"istio-proxy\" not found)"
	if got != want {
		t.Fatalf("aggregate =\n%q\nwant\n%q", got, want)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/methods/ -run TestAggregateContainerLogs -v`
Expected: FAIL — compile error `undefined: aggregateContainerLogs` / `undefined: containerLog`.

- [ ] **Step 3: Add the `containerLog` type and `aggregateContainerLogs` helper**

Append to `internal/methods/pod_logs.go` (before the `init()` line). Add `"strings"` to its import block now (it is used here):

```go
// containerLog is one container's log fetch result: text on success, err on failure.
type containerLog struct {
	name string
	text string
	err  error
}

// aggregateContainerLogs renders one labeled block per container. A failed
// fetch becomes an inline "(no logs: …)" note so one container (e.g. a healthy
// sidecar with no previous instance) never fails the whole step.
func aggregateContainerLogs(logs []containerLog) string {
	var b strings.Builder
	for i, cl := range logs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("=== container: ")
		b.WriteString(cl.name)
		b.WriteString(" ===\n")
		if cl.err != nil {
			b.WriteString("(no logs: ")
			b.WriteString(cl.err.Error())
			b.WriteString(")")
		} else {
			b.WriteString(cl.text)
		}
	}
	return b.String()
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/methods/ -run TestAggregateContainerLogs -v`
Expected: PASS.

- [ ] **Step 5: Write the failing Run-level tests**

Add to `internal/methods/pod_logs_test.go`:

```go
func TestCheckPodLogsAllContainers(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "payments"},
		Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "app"}, {Name: "istio-proxy"}},
			InitContainers: []corev1.Container{{Name: "init-db"}},
		},
	}
	client := fake.NewSimpleClientset(pod)
	m, _ := Builtin().Get("check_pod_logs")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1", "previous": "true"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	logs := out["logs"].(string)
	for _, c := range []string{"app", "istio-proxy", "init-db"} {
		if !strings.Contains(logs, "=== container: "+c+" ===") {
			t.Errorf("missing header for %q in:\n%s", c, logs)
		}
	}
	if strings.Count(logs, "fake logs") != 3 {
		t.Errorf("want 3 container log bodies, got:\n%s", logs)
	}
}

func TestCheckPodLogsSingleContainerNoHeader(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "payments"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	client := fake.NewSimpleClientset(pod)
	m, _ := Builtin().Get("check_pod_logs")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["logs"] != "fake logs" {
		t.Errorf("single-container logs = %q, want plain \"fake logs\"", out["logs"])
	}
}

func TestCheckPodLogsPodNotFound(t *testing.T) {
	client := fake.NewSimpleClientset() // no pods registered
	m, _ := Builtin().Get("check_pod_logs")
	if _, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "missing"}); err == nil {
		t.Fatal("expected error when pod does not exist")
	}
}
```

- [ ] **Step 6: Run them to verify they fail**

Run: `go test ./internal/methods/ -run 'TestCheckPodLogsAllContainers|TestCheckPodLogsSingleContainerNoHeader|TestCheckPodLogsPodNotFound' -v`
Expected: FAIL — `TestCheckPodLogsAllContainers` fails (current Run does one empty-container `GetLogs`, so no `=== container:` headers) and `TestCheckPodLogsPodNotFound` fails (current Run never `Get`s the pod, so no error). `TestCheckPodLogsSingleContainerNoHeader` may already pass — that's fine.

- [ ] **Step 7: Rewrite `Run` and add `podContainerNames`; update the container param description and imports**

In `internal/methods/pod_logs.go`:

(a) Replace the import block with:

```go
import (
	"context"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)
```

(b) Update the `container` param description (was `"container name; empty = first container"`):

```go
		{Name: "container", Description: "container name; empty = all containers"},
```

(c) Replace the entire `Run` method (currently lines 81-96) with:

```go
func (checkPodLogs) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	opts, err := buildPodLogOptions(params)
	if err != nil {
		return nil, err
	}
	maxLine, err := parseMaxLineLength(params)
	if err != nil {
		return nil, err
	}
	ns, name := params["namespace"], params["name"]
	pods := deps.Kube.CoreV1().Pods(ns)

	fetch := func(container string) (string, error) {
		opts.Container = container
		raw, err := pods.GetLogs(name, opts).DoRaw(ctx)
		if err != nil {
			return "", err
		}
		return ClampLineLength(string(raw), maxLine), nil
	}

	// Explicit container: single fetch, hard error on failure (unchanged behavior).
	if opts.Container != "" {
		text, err := fetch(opts.Container)
		if err != nil {
			return nil, fmt.Errorf("get logs %s/%s: %w", ns, name, err)
		}
		return Outputs{"logs": Truncate(text, defaultLogBytes)}, nil
	}

	// No container named: enumerate the pod's containers. The logs subresource
	// rejects an empty container on a multi-container pod, so we name each one.
	pod, err := pods.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", ns, name, err)
	}
	names := podContainerNames(pod)

	// 0 or 1 container: one fetch, no per-container header (preserves single-
	// container output). 0 is degenerate (no real pod has zero containers); an
	// empty container name lets the API pick the sole container.
	if len(names) <= 1 {
		c := ""
		if len(names) == 1 {
			c = names[0]
		}
		text, err := fetch(c)
		if err != nil {
			return nil, fmt.Errorf("get logs %s/%s: %w", ns, name, err)
		}
		return Outputs{"logs": Truncate(text, defaultLogBytes)}, nil
	}

	// Multiple containers: fetch each; per-container failures become inline notes.
	logs := make([]containerLog, 0, len(names))
	for _, c := range names {
		text, err := fetch(c)
		if err != nil {
			logs = append(logs, containerLog{name: c, err: err})
			continue
		}
		logs = append(logs, containerLog{name: c, text: text})
	}
	return Outputs{"logs": Truncate(aggregateContainerLogs(logs), defaultLogBytes)}, nil
}

// podContainerNames returns the pod's regular containers followed by its init
// containers (init containers crash loop too). Ephemeral containers are out of scope.
func podContainerNames(pod *corev1.Pod) []string {
	names := make([]string, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	for _, c := range pod.Spec.InitContainers {
		names = append(names, c.Name)
	}
	return names
}
```

- [ ] **Step 8: Run the package tests to verify they pass**

Run: `go test ./internal/methods/ -v`
Expected: PASS — the new tests pass and every pre-existing `check_pod_logs` test (`TestCheckPodLogs`, `TestCheckPodLogsBadParams`, `TestCheckPodLogsBadMaxLineLength`, `TestBuildPodLogOptionsTailLinesDefault`, `TestParseMaxLineLength`) still passes. (`TestCheckPodLogs`'s zero-container pod takes the `len(names) <= 1` branch with an empty container and still returns `"fake logs"`; the bad-param tests error during parsing before any API call.)

- [ ] **Step 9: Verify (no commit)**

Run: `go build ./... && go vet ./... && go test ./internal/methods/`
Expected: builds clean, vet clean, package tests PASS. Do NOT commit.

---

### Task 2: `check_pod_status` exposes a `containers` list output

**Files:**
- Modify: `internal/methods/pod_status.go`
- Test: `internal/methods/pod_status_test.go`

`check_pod_status` has no scalar output named `containers`, so the list name `containers` is free (no collision in the `Outputs` map).

- [ ] **Step 1: Write the failing test**

Add to `internal/methods/pod_status_test.go` (its imports already include `context`, `testing`, `corev1`, `metav1`, `fake`):

```go
func TestCheckPodStatusContainersList(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "payments"},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: false, RestartCount: 17},
				{Name: "istio-proxy", Ready: true, RestartCount: 0},
			},
		},
	}
	client := fake.NewSimpleClientset(pod)
	m, _ := Builtin().Get("check_pod_status")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	cs, ok := out["containers"].([]map[string]any)
	if !ok || len(cs) != 2 {
		t.Fatalf("containers = %#v", out["containers"])
	}
	if cs[0]["name"] != "app" || cs[0]["restartCount"] != int64(17) || cs[0]["ready"] != false {
		t.Errorf("container[0] = %#v", cs[0])
	}
	if cs[1]["name"] != "istio-proxy" || cs[1]["ready"] != true {
		t.Errorf("container[1] = %#v", cs[1])
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/methods/ -run TestCheckPodStatusContainersList -v`
Expected: FAIL — `out["containers"]` is nil, so the type assertion `ok` is false → `t.Fatalf`.

- [ ] **Step 3: Add `ListOutputs` and populate the list in `Run`**

In `internal/methods/pod_status.go`:

(a) Add this method immediately after `OutputFields()` (after its closing brace, before `Run`):

```go
func (checkPodStatus) ListOutputs() []ListOutputField {
	return []ListOutputField{{
		Name:        "containers",
		Description: "per-container status (forEach source for per-container checks)",
		ItemFields: []OutputField{
			{Name: "name", Type: FieldString, Description: "container name"},
			{Name: "restartCount", Type: FieldInt, Description: "container restart count"},
			{Name: "ready", Type: FieldBool, Description: "container Ready"},
		},
	}}
}
```

(b) In `Run`, build the list alongside the existing container-status loop. Add the slice declaration right before the `for _, cs := range pod.Status.ContainerStatuses {` line:

```go
	containers := make([]map[string]any, 0, len(pod.Status.ContainerStatuses))
```

Inside that existing loop, add one append as its first statement:

```go
		containers = append(containers, map[string]any{
			"name": cs.Name, "restartCount": int64(cs.RestartCount), "ready": cs.Ready,
		})
```

After the loop, before `return out, nil`, add:

```go
	out["containers"] = containers
```

- [ ] **Step 4: Run the package tests to verify they pass**

Run: `go test ./internal/methods/ -run 'TestCheckPodStatus' -v`
Expected: PASS — `TestCheckPodStatusContainersList` passes and `TestCheckPodStatusCrashloop` still passes (the scalar outputs it checks are unchanged).

- [ ] **Step 5: Verify (no commit)**

Run: `go build ./... && go test ./internal/methods/`
Expected: builds clean, package tests PASS. Do NOT commit.

---

### Task 3: `describe_pod` exposes a `containerList` list output

**Files:**
- Modify: `internal/methods/pod_describe.go`
- Test: `internal/methods/pod_describe_test.go`

`describe_pod` already has a scalar output named `containers` (comma-joined string). The list output therefore uses a distinct name, `containerList`, to avoid colliding in the `Outputs` map.

- [ ] **Step 1: Write the failing test**

Add to `internal/methods/pod_describe_test.go` (imports already include `context`, `testing`, `corev1`, `metav1`, `fake`):

```go
func TestDescribePodContainerList(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "payments"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app", Image: "app:v1"},
			{Name: "sidecar", Image: "proxy:v2"},
		}},
	}
	client := fake.NewSimpleClientset(pod)
	m, _ := Builtin().Get("describe_pod")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	cl, ok := out["containerList"].([]map[string]any)
	if !ok || len(cl) != 2 {
		t.Fatalf("containerList = %#v", out["containerList"])
	}
	if cl[0]["name"] != "app" || cl[0]["image"] != "app:v1" {
		t.Errorf("containerList[0] = %#v", cl[0])
	}
	if cl[1]["name"] != "sidecar" || cl[1]["image"] != "proxy:v2" {
		t.Errorf("containerList[1] = %#v", cl[1])
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/methods/ -run TestDescribePodContainerList -v`
Expected: FAIL — `out["containerList"]` is nil → type assertion `ok` is false → `t.Fatalf`.

- [ ] **Step 3: Add `ListOutputs`, the `containerItems` helper, and the `containerList` output**

In `internal/methods/pod_describe.go`:

(a) Add this method immediately after `OutputFields()` (after its closing brace, before `Run`):

```go
func (describePod) ListOutputs() []ListOutputField {
	return []ListOutputField{{
		Name:        "containerList",
		Description: "per-container name and image (forEach source; scalar 'containers' is the comma-joined form)",
		ItemFields: []OutputField{
			{Name: "name", Type: FieldString, Description: "container name"},
			{Name: "image", Type: FieldString, Description: "container image"},
		},
	}}
}
```

(b) Add the `containerList` entry to the `Outputs` literal returned by `Run`. Insert it right after the existing `"images":` line:

```go
		"containerList":     containerItems(pod.Spec.Containers),
```

(c) Add the helper next to `containerImages` (e.g. after the `containerImages` function):

```go
func containerItems(cs []corev1.Container) []map[string]any {
	items := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		items = append(items, map[string]any{"name": c.Name, "image": c.Image})
	}
	return items
}
```

- [ ] **Step 4: Run the package tests to verify they pass**

Run: `go test ./internal/methods/ -run 'TestDescribePod' -v`
Expected: PASS — `TestDescribePodContainerList` passes and `TestDescribePodStructuredOutputs` / `TestDescribePodSanitizes` / `TestDescribePodTroubleshootingFields` still pass (the scalar `containers`/`images` outputs are unchanged).

- [ ] **Step 5: Verify the whole repo (no commit)**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: builds clean, vet clean, the full test suite PASSES (methods, engine, server, controller, etc.). Do NOT commit.

---

## Manual verification (optional, after Task 3)

The reported failure was the `pod-crashloop` UseCase's `previous-logs` step on a multi-container pod. With Task 1 in place, that step (which sends no `container`) now enumerates containers and fetches each. The example UseCase `examples/usecases/pod-crashloop.yaml` needs no edit. If you run kato against a live multi-container crashlooping pod, `previous-logs` should now return per-container blocks instead of `the server rejected our request for an unknown reason`.

## Notes / scope

- No engine changes. No new methods. No UseCase YAML changes.
- List outputs (Tasks 2–3) cover each method's existing container scope (regular containers; `check_pod_status` from `Status.ContainerStatuses`, `describe_pod` from `Spec.Containers`). The Task 1 auto-loop additionally includes init containers, since init-container crash loops are exactly the failure being diagnosed.
- Per the standing instruction, nothing is committed — all changes stay in the working tree.
