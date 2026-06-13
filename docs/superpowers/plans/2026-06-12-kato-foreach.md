# kato `forEach` Fan-out + List Output Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `list_failing_pods` method and a single-step `forEach` fan-out construct so a UseCase can list a workload's failing pods and run a check once per pod, capped.

**Architecture:** A method may declare an optional **list output** (a named list of typed-field records) via a new optional `ListProducer` interface — existing methods are untouched. A `forEach` step references a prior step's list output (`$(steps.x.pods)`), runs its method once per item with `$(item.<field>)` bound into `with`, capped at `maxItems` (default 5, ceiling 20). Scalar `when`/`$(steps.x.y)` matching is unchanged; lists are consumable only by `forEach`.

**Tech Stack:** Go 1.24, controller-runtime, client-go (fake clients in tests), cel-go, controller-gen, envtest. Builds on the existing kato codebase.

**Spec:** `docs/superpowers/specs/2026-06-12-kato-foreach-design.md` — read it before starting.

**Conventions / shared types introduced (use these exact names):**
- `methods.ListOutputField{ Name string; ItemFields []OutputField; Description string }`
- `methods.ListProducer interface { ListOutputs() []ListOutputField }` (optional; only list-producing methods implement it)
- `methods.ListOutputsOf(m Method) []ListOutputField` — returns `m`'s list outputs or nil
- A value in `methods.Outputs` may now be `[]map[string]any` (a list of item records) in addition to `string`/`int64`/`bool`.
- `engine.Ref.Kind` gains `"item"` — `$(item.<field>)` has `Kind:"item"`, `Field:<field>`, empty `Step`.
- `engine.IterationResult{ Item map[string]string; Outcome string; Outputs methods.Outputs; Error string }`
- `engine.StepResult` gains `Iterations []IterationResult` and `Note string`.
- `engine` consts: `defaultMaxItems = 5`, `maxItemsCeiling = 20`.
- `v1alpha1.Step` gains `ForEach string`, `MaxItems int`.
- `v1alpha1.RunStepIteration{ Item map[string]string; Outcome string; Outputs *apiextensionsv1.JSON; Error string }`; `RunStep` gains `Iterations []RunStepIteration` and `Note string`.

**No commits unless the executor is told otherwise.** Every task ends with `go build ./... && go vet ./...` clean and the new tests passing.

---

## File Map

| File | Change |
|---|---|
| `internal/methods/method.go` | add `ListOutputField`, `ListProducer`, `ListOutputsOf` |
| `internal/methods/list_failing_pods.go` (+ `_test.go`) | new method: owner resolution (Deployment/DaemonSet/StatefulSet), failing criteria, worst-first list output |
| `internal/engine/refs.go` (+ test) | parse `$(item.<field>)` (Kind `item`) |
| `api/v1alpha1/usecase_types.go` | `Step.ForEach`, `Step.MaxItems` |
| `api/v1alpha1/run_types.go` | `RunStepIteration`, `RunStep.Iterations`, `RunStep.Note` |
| `internal/engine/engine.go` | `StepResult.Iterations`/`Note`; `forEach` iteration execution + cap + note |
| `internal/engine/validate.go` | `forEach`/list/`item` validation rules |
| `internal/store/store.go` (+ test) | map `StepResult.Iterations`/`Note` into the Run |
| `internal/summarizer/summarizer.go` (+ test) | render `forEach` iterations in evidence |
| `internal/server/server.go` (+ test) | `GET /api/v1/methods` exposes list outputs |
| `charts/kato/templates/rbac.yaml` | add `statefulsets` read |
| `docs/METHOD.md` | document `list_failing_pods` + list outputs |
| `examples/usecases/daemonset-crashloop.yaml` | example forEach UseCase |

---

### Task 1: List-output contract in the methods package

**Files:**
- Modify: `internal/methods/method.go`
- Test: `internal/methods/listoutput_test.go`

- [ ] **Step 1: Write the failing test**

`internal/methods/listoutput_test.go`:

{% raw %}
{% raw %}
```go
package methods

import (
	"context"
	"testing"
)

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
func (plain) Params() []Param                                              { return nil }
func (plain) OutputFields() []OutputField                                  { return nil }
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
```
{% endraw %}
{% endraw %}

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/methods/ -run TestListOutputsOf -v`
Expected: FAIL — `ListOutputField`, `ListOutputsOf` undefined.

- [ ] **Step 3: Add the contract to `internal/methods/method.go`**

Add after the `OutputField` type:

{% raw %}
```go
// ListOutputField declares a list output: a named list whose items are records
// of typed fields. Lists are consumable only by a UseCase `forEach` step, never
// by a `when` condition (spec: matching stays scalar-only).
type ListOutputField struct {
	Name        string
	ItemFields  []OutputField
	Description string
}

// ListProducer is the optional interface a method implements when it returns
// one or more list outputs. Methods without lists simply do not implement it.
type ListProducer interface {
	ListOutputs() []ListOutputField
}

// ListOutputsOf returns m's declared list outputs, or nil if it has none.
func ListOutputsOf(m Method) []ListOutputField {
	if lp, ok := m.(ListProducer); ok {
		return lp.ListOutputs()
	}
	return nil
}
```
{% endraw %}

Also update the `Outputs` doc comment to:

{% raw %}
```go
// Outputs values are string, int64, or bool per the declared OutputField type,
// or []map[string]any for a declared ListOutputField (a list of item records).
type Outputs map[string]any
```
{% endraw %}

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/methods/ -run TestListOutputsOf -v && go build ./... && go vet ./...`
Expected: PASS; build+vet clean.

- [ ] **Step 5: Commit** (skip if executor is in no-commit mode)

```bash
git add internal/methods/method.go internal/methods/listoutput_test.go
git commit -m "feat: list-output contract for methods (ListProducer)"
```

---

### Task 2: `list_failing_pods` method

**Files:**
- Create: `internal/methods/list_failing_pods.go`, `internal/methods/list_failing_pods_test.go`

- [ ] **Step 1: Write the failing test**

`internal/methods/list_failing_pods_test.go`:

{% raw %}
{% raw %}
```go
package methods

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func crashingPod(name, ns string, owner metav1.OwnerReference, restarts int32, waiting string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, OwnerReferences: []metav1.OwnerReference{owner}},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "c", RestartCount: restarts,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: waiting}},
			}},
		},
	}
}

func dsOwner(name string) metav1.OwnerReference {
	return metav1.OwnerReference{Kind: "DaemonSet", Name: name}
}

func TestListFailingPodsDaemonSet(t *testing.T) {
	healthy := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "kube-system", OwnerReferences: []metav1.OwnerReference{dsOwner("nld")}},
		Status: corev1.PodStatus{
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "c", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
		},
	}
	client := fake.NewSimpleClientset(
		healthy,
		crashingPod("nld-a", "kube-system", dsOwner("nld"), 9, "CrashLoopBackOff"),
		crashingPod("nld-b", "kube-system", dsOwner("nld"), 12, "CrashLoopBackOff"),
		crashingPod("other", "kube-system", dsOwner("different-ds"), 5, "CrashLoopBackOff"), // wrong owner
	)
	m, ok := Builtin().Get("list_failing_pods")
	if !ok {
		t.Fatal("list_failing_pods not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "kube-system", "kind": "DaemonSet", "name": "nld"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != int64(2) || out["anyFailing"] != true {
		t.Errorf("count=%v anyFailing=%v", out["count"], out["anyFailing"])
	}
	pods, _ := out["pods"].([]map[string]any)
	if len(pods) != 2 {
		t.Fatalf("pods len = %d, want 2", len(pods))
	}
	// Worst-first: nld-b (12 restarts) before nld-a (9).
	if pods[0]["name"] != "nld-b" || pods[0]["restartCount"] != int64(12) {
		t.Errorf("worst-first wrong: %v", pods)
	}
	if pods[0]["namespace"] != "kube-system" || pods[0]["reason"] != "CrashLoopBackOff" {
		t.Errorf("item fields wrong: %v", pods[0])
	}
}

func TestListFailingPodsDeployment(t *testing.T) {
	// Deployment -> ReplicaSet -> Pod (two hops).
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "api-7d9", Namespace: "payments",
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "api"}},
	}}
	rsOwner := metav1.OwnerReference{Kind: "ReplicaSet", Name: "api-7d9"}
	client := fake.NewSimpleClientset(
		rs,
		crashingPod("api-7d9-x", "payments", rsOwner, 3, "CrashLoopBackOff"),
	)
	m, _ := Builtin().Get("list_failing_pods")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "kind": "Deployment", "name": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != int64(1) {
		t.Fatalf("count = %v, want 1", out["count"])
	}
	pods := out["pods"].([]map[string]any)
	if pods[0]["name"] != "api-7d9-x" {
		t.Errorf("pod = %v", pods[0])
	}
}

func TestListFailingPodsCriteriaAndMinRestarts(t *testing.T) {
	imagePull := crashingPod("img", "default", dsOwner("ds"), 0, "ImagePullBackOff")
	crash := crashingPod("crash", "default", dsOwner("ds"), 7, "CrashLoopBackOff")
	client := fake.NewSimpleClientset(imagePull, crash)
	m, _ := Builtin().Get("list_failing_pods")

	// Default criteria: both image-pull and crashloop match.
	out, _ := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "kind": "DaemonSet", "name": "ds"})
	if out["count"] != int64(2) {
		t.Errorf("default count = %v, want 2", out["count"])
	}

	// Disable image-pull: only the crashloop pod remains.
	out, _ = m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "kind": "DaemonSet", "name": "ds", "includeImagePull": "false"})
	if out["count"] != int64(1) {
		t.Errorf("no-imagepull count = %v, want 1", out["count"])
	}

	// minRestarts=5 drops the image-pull pod (0 restarts), keeps crash (7).
	out, _ = m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "kind": "DaemonSet", "name": "ds", "minRestarts": "5"})
	if out["count"] != int64(1) {
		t.Errorf("minRestarts count = %v, want 1", out["count"])
	}
}

func TestListFailingPodsNoneFailing(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("list_failing_pods")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "kind": "DaemonSet", "name": "ds"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != int64(0) || out["anyFailing"] != false {
		t.Errorf("expected zero/false, got count=%v any=%v", out["count"], out["anyFailing"])
	}
	if pods := out["pods"].([]map[string]any); len(pods) != 0 {
		t.Errorf("pods should be empty, got %v", pods)
	}
}

func TestListFailingPodsDeclaresListOutput(t *testing.T) {
	m, _ := Builtin().Get("list_failing_pods")
	lists := ListOutputsOf(m)
	if len(lists) != 1 || lists[0].Name != "pods" {
		t.Fatalf("list outputs = %v", lists)
	}
	fields := map[string]FieldType{}
	for _, f := range lists[0].ItemFields {
		fields[f.Name] = f.Type
	}
	if fields["namespace"] != FieldString || fields["name"] != FieldString ||
		fields["reason"] != FieldString || fields["restartCount"] != FieldInt {
		t.Errorf("item fields wrong: %v", fields)
	}
}
```
{% endraw %}
{% endraw %}

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/methods/ -run TestListFailingPods -v`
Expected: FAIL — method not registered.

- [ ] **Step 3: Create `internal/methods/list_failing_pods.go`**

{% raw %}
```go
package methods

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type listFailingPods struct{}

func (listFailingPods) Name() string { return "list_failing_pods" }
func (listFailingPods) Description() string {
	return "Failing pods of a workload (Deployment/DaemonSet/StatefulSet), worst-first"
}

func (listFailingPods) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Workload namespace"},
		{Name: "kind", Required: true, Description: "Deployment | DaemonSet | StatefulSet"},
		{Name: "name", Required: true, Description: "Workload name"},
		{Name: "minRestarts", Description: "only include pods with restartCount >= this (default 0)"},
		{Name: "includeCrashLoop", Description: `count CrashLoopBackOff pods (default "true")`},
		{Name: "includeImagePull", Description: `count ImagePullBackOff/ErrImagePull/CreateContainerError pods (default "true")`},
		{Name: "includeOOM", Description: `count OOMKilled / non-zero last-exit pods (default "true")`},
		{Name: "includeNotReady", Description: `count any not-Ready pod (default "false")`},
	}
}

func (listFailingPods) OutputFields() []OutputField {
	return []OutputField{
		{Name: "count", Type: FieldInt, Description: "number of failing pods matched"},
		{Name: "anyFailing", Type: FieldBool, Description: "count > 0"},
	}
}

func (listFailingPods) ListOutputs() []ListOutputField {
	return []ListOutputField{{
		Name:        "pods",
		Description: "failing pods, sorted worst-first by restartCount",
		ItemFields: []OutputField{
			{Name: "namespace", Type: FieldString, Description: "pod namespace"},
			{Name: "name", Type: FieldString, Description: "pod name"},
			{Name: "reason", Type: FieldString, Description: "dominant failure reason"},
			{Name: "restartCount", Type: FieldInt, Description: "max restartCount across containers"},
		},
	}}
}

func (listFailingPods) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	crit, err := parseFailCriteria(params)
	if err != nil {
		return nil, err
	}
	pods, err := ownedPods(ctx, deps.Kube, params["namespace"], params["kind"], params["name"])
	if err != nil {
		return nil, err
	}

	items := []map[string]any{}
	for i := range pods {
		p := &pods[i]
		reason, restarts, failing := classifyPod(p, crit)
		if !failing || int(restarts) < crit.minRestarts {
			continue
		}
		items = append(items, map[string]any{
			"namespace": p.Namespace, "name": p.Name,
			"reason": reason, "restartCount": int64(restarts),
		})
	}
	// Worst-first by restartCount, then name for stability.
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := items[i]["restartCount"].(int64), items[j]["restartCount"].(int64)
		if ri != rj {
			return ri > rj
		}
		return items[i]["name"].(string) < items[j]["name"].(string)
	})

	return Outputs{
		"count":      int64(len(items)),
		"anyFailing": len(items) > 0,
		"pods":       items,
	}, nil
}

type failCriteria struct {
	minRestarts                                          int
	crashLoop, imagePull, oom, notReady                  bool
}

func parseBoolDefault(params map[string]string, key string, def bool) (bool, error) {
	v, ok := params[key]
	if !ok || v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("param %s: %w", key, err)
	}
	return b, nil
}

func parseFailCriteria(params map[string]string) (failCriteria, error) {
	c := failCriteria{}
	if v := params["minRestarts"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("param minRestarts: %w", err)
		}
		c.minRestarts = n
	}
	var err error
	if c.crashLoop, err = parseBoolDefault(params, "includeCrashLoop", true); err != nil {
		return c, err
	}
	if c.imagePull, err = parseBoolDefault(params, "includeImagePull", true); err != nil {
		return c, err
	}
	if c.oom, err = parseBoolDefault(params, "includeOOM", true); err != nil {
		return c, err
	}
	if c.notReady, err = parseBoolDefault(params, "includeNotReady", false); err != nil {
		return c, err
	}
	return c, nil
}

var imagePullReasons = map[string]bool{
	"ImagePullBackOff": true, "ErrImagePull": true, "CreateContainerError": true,
}

// classifyPod returns the dominant failure reason, the max restart count, and
// whether the pod is failing under the given criteria.
func classifyPod(p *corev1.Pod, c failCriteria) (reason string, restarts int32, failing bool) {
	ready := false
	for _, cond := range p.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.RestartCount > restarts {
			restarts = cs.RestartCount
		}
		if w := cs.State.Waiting; w != nil {
			if c.crashLoop && w.Reason == "CrashLoopBackOff" {
				return "CrashLoopBackOff", maxRestart(p), true
			}
			if c.imagePull && imagePullReasons[w.Reason] {
				return w.Reason, maxRestart(p), true
			}
		}
		if t := cs.LastTerminationState.Terminated; t != nil && c.oom {
			if t.Reason == "OOMKilled" || t.ExitCode != 0 {
				r := t.Reason
				if r == "" {
					r = "Error"
				}
				return r, maxRestart(p), true
			}
		}
	}
	if c.notReady && !ready {
		return "NotReady", restarts, true
	}
	return "", restarts, false
}

func maxRestart(p *corev1.Pod) int32 {
	var m int32
	for _, cs := range p.Status.ContainerStatuses {
		if cs.RestartCount > m {
			m = cs.RestartCount
		}
	}
	return m
}

// ownedPods returns the pods owned by the named workload in the namespace.
func ownedPods(ctx context.Context, client kubernetes.Interface, ns, kind, name string) ([]corev1.Pod, error) {
	all, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", ns, err)
	}
	switch kind {
	case "DaemonSet", "StatefulSet":
		return filterByOwner(all.Items, kind, name), nil
	case "Deployment":
		rsList, err := client.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list replicasets in %s: %w", ns, err)
		}
		rsNames := map[string]bool{}
		for _, rs := range rsList.Items {
			if ownedBy(rs.OwnerReferences, "Deployment", name) {
				rsNames[rs.Name] = true
			}
		}
		var out []corev1.Pod
		for _, p := range all.Items {
			for _, ref := range p.OwnerReferences {
				if ref.Kind == "ReplicaSet" && rsNames[ref.Name] {
					out = append(out, p)
					break
				}
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported kind %q (want Deployment|DaemonSet|StatefulSet)", kind)
	}
}

func filterByOwner(pods []corev1.Pod, kind, name string) []corev1.Pod {
	var out []corev1.Pod
	for _, p := range pods {
		if ownedBy(p.OwnerReferences, kind, name) {
			out = append(out, p)
		}
	}
	return out
}

func ownedBy(refs []metav1.OwnerReference, kind, name string) bool {
	for _, r := range refs {
		if r.Kind == kind && r.Name == name {
			return true
		}
	}
	return false
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(listFailingPods{}) })
}
```
{% endraw %}

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/methods/ -run TestListFailingPods -v && go build ./... && go vet ./...`
Expected: PASS (5 tests); build+vet clean.

- [ ] **Step 5: Commit** (skip in no-commit mode)

```bash
git add internal/methods/list_failing_pods.go internal/methods/list_failing_pods_test.go
git commit -m "feat: list_failing_pods method with owner resolution and list output"
```

---

### Task 3: `$(item.<field>)` reference kind

**Files:**
- Modify: `internal/engine/refs.go`
- Test: `internal/engine/refs_item_test.go`

- [ ] **Step 1: Write the failing test**

`internal/engine/refs_item_test.go`:

```go
package engine

import "testing"

func TestExtractItemRef(t *testing.T) {
	refs, err := ExtractRefs(`pod $(item.name) in $(item.namespace)`)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("got %d refs", len(refs))
	}
	if refs[0].Kind != "item" || refs[0].Field != "name" || refs[0].Step != "" {
		t.Errorf("ref0 = %+v", refs[0])
	}
	if refs[1].Kind != "item" || refs[1].Field != "namespace" {
		t.Errorf("ref1 = %+v", refs[1])
	}
}

func TestItemRefRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"$(item)", "$(item.)", "$(item.a.b)"} {
		if _, err := ExtractRefs(bad); err == nil {
			t.Errorf("ExtractRefs(%q): expected error", bad)
		}
	}
}

func TestSubstituteItemRef(t *testing.T) {
	got, err := Substitute("ns=$(item.namespace) name=$(item.name)", func(r Ref) (string, bool) {
		switch {
		case r.Kind == "item" && r.Field == "namespace":
			return "kube-system", true
		case r.Kind == "item" && r.Field == "name":
			return "nld-abc", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got != "ns=kube-system name=nld-abc" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/engine/ -run 'TestExtractItemRef|TestItemRefRejects|TestSubstituteItemRef' -v`
Expected: FAIL — `item.name` parsed as invalid reference.

- [ ] **Step 3: Extend `parseRef` in `internal/engine/refs.go`**

Replace the `parseRef` function with:

```go
func parseRef(raw string) (Ref, error) {
	parts := strings.Split(raw, ".")
	switch {
	case len(parts) == 2 && parts[0] == "inputs" && parts[1] != "":
		return Ref{Raw: raw, Kind: "inputs", Field: parts[1]}, nil
	case len(parts) == 2 && parts[0] == "item" && parts[1] != "":
		return Ref{Raw: raw, Kind: "item", Field: parts[1]}, nil
	case len(parts) == 3 && parts[0] == "steps" && parts[1] != "" && parts[2] != "":
		return Ref{Raw: raw, Kind: "steps", Step: parts[1], Field: parts[2]}, nil
	default:
		return Ref{}, fmt.Errorf("invalid reference $(%s): want $(inputs.<name>), $(item.<field>) or $(steps.<step>.<field>)", raw)
	}
}
```

Also update `CELVar` so an `item` ref has a stable name (item refs never reach CEL — they are rejected from `when` by validation — but keep the method total):

```go
func (r Ref) CELVar() string {
	switch r.Kind {
	case "inputs":
		return "inputs_" + r.Field
	case "item":
		return "item_" + r.Field
	default:
		return "steps_" + strings.ReplaceAll(r.Step, "-", "_") + "__" + r.Field
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/engine/... && go build ./... && go vet ./...`
Expected: PASS (item tests + all existing engine tests); build+vet clean.

- [ ] **Step 5: Commit** (skip in no-commit mode)

```bash
git add internal/engine/refs.go internal/engine/refs_item_test.go
git commit -m "feat: parse \$(item.<field>) references"
```

---

### Task 4: API types — `Step.ForEach`/`MaxItems`, `RunStepIteration`

**Files:**
- Modify: `api/v1alpha1/usecase_types.go`, `api/v1alpha1/run_types.go`
- Test: `api/v1alpha1/foreach_types_test.go`
- Regenerate: `api/v1alpha1/zz_generated.deepcopy.go`, `config/crd/bases/*`

- [ ] **Step 1: Write the failing test**

`api/v1alpha1/foreach_types_test.go`:

{% raw %}
{% raw %}
```go
package v1alpha1

import "testing"

func TestForEachFieldsAndDeepCopy(t *testing.T) {
	uc := &UseCase{
		Spec: UseCaseSpec{
			Steps: []Step{{
				Name: "logs", Method: "check_pod_logs",
				ForEach: "$(steps.crashing.pods)", MaxItems: 3,
				With: map[string]string{"name": "$(item.name)"},
			}},
		},
	}
	cp := uc.DeepCopy()
	if cp.Spec.Steps[0].ForEach != "$(steps.crashing.pods)" || cp.Spec.Steps[0].MaxItems != 3 {
		t.Fatalf("forEach fields not copied: %+v", cp.Spec.Steps[0])
	}

	run := &Run{Status: RunStatus{Steps: []RunStep{{
		Name: "logs", Outcome: "completed",
		Note:       "matched 12, checked 3 (worst-first); 9 not examined",
		Iterations: []RunStepIteration{{Item: map[string]string{"name": "nld-a"}, Outcome: "completed"}},
	}}}}
	rc := run.DeepCopy()
	if rc.Status.Steps[0].Note == "" || len(rc.Status.Steps[0].Iterations) != 1 {
		t.Fatalf("run iteration fields not copied: %+v", rc.Status.Steps[0])
	}
}
```
{% endraw %}
{% endraw %}

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./api/... -run TestForEach -v`
Expected: FAIL — `ForEach`, `MaxItems`, `RunStepIteration`, `Note` undefined.

- [ ] **Step 3: Add fields to `api/v1alpha1/usecase_types.go`**

In the `Step` struct, after the `SummaryFilter` field, add:

```go
	// ForEach references a list output of an earlier step, e.g.
	// "$(steps.crashing.pods)". When set, Method runs once per item and
	// $(item.<field>) is available in With (not in When).
	ForEach string `json:"forEach,omitempty"`
	// MaxItems caps forEach iterations. Default 5; hard ceiling 20.
	MaxItems int `json:"maxItems,omitempty"`
```

- [ ] **Step 4: Add types to `api/v1alpha1/run_types.go`**

Add a new type and two `RunStep` fields. After the `RunStep` struct, add:

```go
// RunStepIteration records one forEach iteration's result.
type RunStepIteration struct {
	Item map[string]string `json:"item,omitempty"`
	// +kubebuilder:validation:Enum=completed;failed
	Outcome string `json:"outcome"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Outputs *apiextensionsv1.JSON `json:"outputs,omitempty"`
	Error   string                `json:"error,omitempty"`
}
```

And inside the `RunStep` struct, after the `Error` field, add:

```go
	// Iterations holds per-item results for a forEach step.
	Iterations []RunStepIteration `json:"iterations,omitempty"`
	// Note records forEach truncation, e.g. "matched 12, checked 3".
	Note string `json:"note,omitempty"`
```

- [ ] **Step 5: Regenerate deepcopy + manifests, run test**

```bash
make generate
make manifests
go test ./api/... -run TestForEach -v
```
Expected: PASS. `git status` shows updated `zz_generated.deepcopy.go` and `config/crd/bases/kato.zufardhiyaulhaq.com_usecases.yaml` + `..._runs.yaml`.

- [ ] **Step 6: Copy regenerated CRDs into the Helm chart**

```bash
cp config/crd/bases/*.yaml charts/kato/crds/
go build ./... && go vet ./...
```
Expected: build+vet clean.

- [ ] **Step 7: Commit** (skip in no-commit mode)

```bash
git add api/v1alpha1 config/crd charts/kato/crds
git commit -m "feat: forEach/maxItems Step fields and Run iteration records"
```

---

### Task 5: Watch-time validation for `forEach` / list / `item`

**Files:**
- Modify: `internal/engine/validate.go`, `internal/engine/cel.go`
- Test: `internal/engine/validate_foreach_test.go`

- [ ] **Step 1: Write the failing test**

`internal/engine/validate_foreach_test.go`:

{% raw %}
```go
package engine

import (
	"strings"
	"testing"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

func foreachUseCase() *v1alpha1.UseCase {
	return &v1alpha1.UseCase{
		Spec: v1alpha1.UseCaseSpec{
			Inputs: []v1alpha1.InputDecl{
				{Name: "namespace", Required: true}, {Name: "workload", Required: true},
			},
			Steps: []v1alpha1.Step{
				{Name: "crashing", Method: "list_failing_pods",
					With: map[string]string{"namespace": "$(inputs.namespace)", "kind": "DaemonSet", "name": "$(inputs.workload)"}},
				{Name: "logs", Method: "check_pod_logs",
					ForEach:  "$(steps.crashing.pods)",
					MaxItems: 3,
					When:     "$(steps.crashing.anyFailing)",
					With:     map[string]string{"namespace": "$(item.namespace)", "name": "$(item.name)"}},
			},
			Summary: v1alpha1.SummarySpec{Prompt: "x"},
		},
	}
}

func validateFE(uc *v1alpha1.UseCase) []string {
	return ValidateUseCase(uc, methods.Builtin(), func(string) bool { return true })
}

func TestValidateForEachOK(t *testing.T) {
	if errs := validateFE(foreachUseCase()); len(errs) != 0 {
		t.Fatalf("valid forEach use case rejected: %v", errs)
	}
}

func TestValidateForEachErrors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*v1alpha1.UseCase)
		wantSub string
	}{
		{"forEach not a list", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].ForEach = "$(steps.crashing.count)" }, "not a list output"},
		{"forEach unknown step", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].ForEach = "$(steps.nope.pods)" }, "unknown or not before"},
		{"forEach forward ref", func(u *v1alpha1.UseCase) {
			u.Spec.Steps[0].ForEach = "$(steps.logs.pods)"
			u.Spec.Steps[0].With = map[string]string{"name": "$(item.name)"}
		}, "unknown or not before"},
		{"item field unknown", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].With["name"] = "$(item.bogus)" }, "no item field"},
		{"item without forEach", func(u *v1alpha1.UseCase) { u.Spec.Steps[0].With["name"] = "$(item.name)" }, "only valid in a forEach"},
		{"item in when", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].When = `$(item.name) != ""` }, "not allowed in a when"},
		{"negative maxItems", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].MaxItems = -1 }, "maxItems"},
		{"forEach two refs", func(u *v1alpha1.UseCase) { u.Spec.Steps[1].ForEach = "$(steps.crashing.pods)$(inputs.namespace)" }, "exactly one"},
	}
	for _, tc := range cases {
		uc := foreachUseCase()
		tc.mutate(uc)
		errs := validateFE(uc)
		if len(errs) == 0 {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		if !strings.Contains(strings.Join(errs, "; "), tc.wantSub) {
			t.Errorf("%s: %v does not contain %q", tc.name, errs, tc.wantSub)
		}
	}
}
```
{% endraw %}

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/engine/ -run TestValidateForEach -v`
Expected: FAIL — current validation does not understand `forEach`/`item`.

- [ ] **Step 3: Reject `item` refs in `when` — edit `internal/engine/cel.go`**

In `Scope.typeOf`, add an `item` case at the very top of the function (before the `inputs` check):

```go
func (s Scope) typeOf(r Ref) (*cel.Type, error) {
	if r.Kind == "item" {
		return nil, fmt.Errorf("$(%s): item references are not allowed in a when condition (only in with)", r.Raw)
	}
	if r.Kind == "inputs" {
		// ... unchanged ...
```

(Leave the rest of `typeOf` unchanged.)

- [ ] **Step 4: Rewrite `ValidateUseCase` in `internal/engine/validate.go`**

Replace the whole function body with the version below. It adds: per-step list-output tracking, `forEach` reference validation, `item`-ref handling in `with`, and the `forEach`-only / `maxItems` rules.

{% raw %}
```go
func ValidateUseCase(uc *v1alpha1.UseCase, reg *methods.Registry, modelConfigExists func(string) bool) []string {
	var errs []string
	addf := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	inputNames := make([]string, 0, len(uc.Spec.Inputs))
	for _, in := range uc.Spec.Inputs {
		inputNames = append(inputNames, in.Name)
	}

	scope := Scope{InputNames: inputNames, StepOutputs: map[string]map[string]methods.FieldType{}}
	// stepLists[step][listName][itemField] = type
	stepLists := map[string]map[string]map[string]methods.FieldType{}
	seenSteps := map[string]bool{}
	seenSanitized := map[string]string{}

	for i, step := range uc.Spec.Steps {
		where := fmt.Sprintf("steps[%d] (%s)", i, step.Name)

		if seenSteps[step.Name] {
			addf("%s: duplicate step name %q", where, step.Name)
		}
		seenSteps[step.Name] = true
		sanitized := strings.ReplaceAll(step.Name, "-", "_")
		if prev, clash := seenSanitized[sanitized]; clash && prev != step.Name {
			addf("%s: name collides with step %q after hyphen sanitization", where, prev)
		}
		seenSanitized[sanitized] = step.Name

		m, ok := reg.Get(step.Method)
		if !ok {
			addf("%s: unknown method %q", where, step.Method)
			continue
		}

		isForEach := step.ForEach != ""
		// itemFields are the fields $(item.X) may reference inside this step.
		var itemFields map[string]methods.FieldType

		if isForEach {
			if step.MaxItems < 0 {
				addf("%s: maxItems must be >= 0", where)
			}
			refs, err := ExtractRefs(step.ForEach)
			if err != nil {
				addf("%s forEach: %v", where, err)
			} else if len(refs) != 1 || refs[0].Kind != "steps" {
				addf("%s forEach: must be exactly one $(steps.<step>.<listOutput>) reference", where)
			} else {
				r := refs[0]
				lists, known := stepLists[r.Step]
				if !known {
					addf("%s forEach: step %q is unknown or not before this step", where, r.Step)
				} else if fields, isList := lists[r.Field]; !isList {
					addf("%s forEach: $(steps.%s.%s) is not a list output", where, r.Step, r.Field)
				} else {
					itemFields = fields
				}
			}
		}

		if err := methods.ValidateParams(m, step.With); err != nil {
			addf("%s: %v", where, err)
		}

		// with-value references.
		for param, val := range step.With {
			refs, err := ExtractRefs(val)
			if err != nil {
				addf("%s with.%s: %v", where, param, err)
				continue
			}
			for _, r := range refs {
				switch r.Kind {
				case "item":
					if !isForEach {
						addf("%s with.%s: $(item.%s) is only valid in a forEach step", where, param, r.Field)
					} else if itemFields != nil {
						if _, ok := itemFields[r.Field]; !ok {
							valid := make([]string, 0, len(itemFields))
							for f := range itemFields {
								valid = append(valid, f)
							}
							addf("%s with.%s: list has no item field %q (valid: %s)", where, param, r.Field, strings.Join(valid, ", "))
						}
					}
				default:
					if _, err := scope.typeOf(r); err != nil {
						addf("%s with.%s: %v", where, param, err)
					}
				}
			}
		}

		if step.When != "" {
			if _, err := CompileWhen(step.When, scope); err != nil {
				addf("%s when: %v", where, err)
			}
		}

		for _, f := range step.SummaryFilter {
			if _, ok := methods.OutputType(m, f); !ok {
				addf("%s: summaryFilter field %q not declared by method %q", where, f, step.Method)
			}
		}

		// Register outputs for LATER steps. A forEach step exposes nothing
		// referenceable (its results are per-item, not aggregated).
		if !isForEach {
			fields := map[string]methods.FieldType{}
			for _, of := range m.OutputFields() {
				fields[of.Name] = of.Type
			}
			scope.StepOutputs[step.Name] = fields

			lm := map[string]map[string]methods.FieldType{}
			for _, lo := range methods.ListOutputsOf(m) {
				fm := map[string]methods.FieldType{}
				for _, itf := range lo.ItemFields {
					fm[itf.Name] = itf.Type
				}
				lm[lo.Name] = fm
			}
			stepLists[step.Name] = lm
		}
	}

	if ref := uc.Spec.Summary.ModelConfigRef; ref != "" && !modelConfigExists(ref) {
		addf("summary.modelConfigRef: ModelConfig %q not found", ref)
	}
	return errs
}
```
{% endraw %}

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/engine/... && go build ./... && go vet ./...`
Expected: PASS (forEach validation + all existing engine tests); build+vet clean.

Note on the "forEach not a list" case: `$(steps.crashing.count)` references a scalar, so `lists["count"]` is absent → "is not a list output". The "forward ref" case references `logs` (a forEach step that registered no lists) → "unknown or not before".

- [ ] **Step 6: Commit** (skip in no-commit mode)

```bash
git add internal/engine/validate.go internal/engine/cel.go internal/engine/validate_foreach_test.go
git commit -m "feat: watch-time validation for forEach, list refs, and item refs"
```

---

### Task 6: Engine `forEach` iteration execution + Run mapping

**Files:**
- Modify: `internal/engine/engine.go`, `internal/store/store.go`
- Test: `internal/engine/engine_foreach_test.go`, `internal/store/store_test.go` (add a case)

- [ ] **Step 1: Write the failing engine test**

`internal/engine/engine_foreach_test.go`:

{% raw %}
{% raw %}
```go
package engine

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

func feUseCase() *v1alpha1.UseCase {
	return &v1alpha1.UseCase{
		ObjectMeta: metav1.ObjectMeta{Name: "fe"},
		Spec: v1alpha1.UseCaseSpec{
			Inputs: []v1alpha1.InputDecl{{Name: "namespace", Required: true}, {Name: "workload", Required: true}},
			Steps: []v1alpha1.Step{
				{Name: "crashing", Method: "list_failing_pods",
					With: map[string]string{"namespace": "$(inputs.namespace)", "kind": "DaemonSet", "name": "$(inputs.workload)"}},
				{Name: "check", Method: "check_pod_status", ForEach: "$(steps.crashing.pods)", MaxItems: 2,
					With: map[string]string{"namespace": "$(item.namespace)", "name": "$(item.name)"}},
			},
			Summary: v1alpha1.SummarySpec{Prompt: "x"},
		},
	}
}

func fePod(name string, restarts int32) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system",
			OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: "nld"}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "c", RestartCount: restarts,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
		},
	}
}

func feEngine(client *fake.Clientset) *Engine {
	return &Engine{Deps: methods.Deps{Kube: client}, Registry: methods.Builtin(),
		Summarize: okSummarizer("s"), StepTimeout: 5 * time.Second}
}

func TestExecuteForEachCapsAndOrders(t *testing.T) {
	client := fake.NewSimpleClientset(fePod("a", 3), fePod("b", 9), fePod("c", 1))
	res, err := feEngine(client).Execute(context.Background(), feUseCase(),
		map[string]string{"namespace": "kube-system", "workload": "nld"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Steps[0].Outcome != "completed" || res.Steps[0].Outputs["count"] != int64(3) {
		t.Fatalf("crashing step = %+v", res.Steps[0])
	}
	fe := res.Steps[1]
	if fe.Outcome != "completed" {
		t.Fatalf("forEach step outcome = %q", fe.Outcome)
	}
	if len(fe.Iterations) != 2 {
		t.Fatalf("iterations = %d, want 2 (capped)", len(fe.Iterations))
	}
	if fe.Note == "" {
		t.Error("expected truncation note")
	}
	// Worst-first: b (9 restarts) then a (3).
	if fe.Iterations[0].Item["name"] != "b" || fe.Iterations[1].Item["name"] != "a" {
		t.Errorf("order wrong: %v, %v", fe.Iterations[0].Item, fe.Iterations[1].Item)
	}
	if fe.Iterations[0].Outcome != "completed" || fe.Iterations[0].Outputs["restartCount"] != int64(9) {
		t.Errorf("iteration0 outputs = %+v", fe.Iterations[0])
	}
	if res.Phase != "Succeeded" {
		t.Errorf("phase = %q", res.Phase)
	}
}

func TestExecuteForEachZeroItemsSkips(t *testing.T) {
	client := fake.NewSimpleClientset() // no pods
	res, err := feEngine(client).Execute(context.Background(), feUseCase(),
		map[string]string{"namespace": "kube-system", "workload": "nld"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Steps[1].Outcome != "skipped" || res.Steps[1].Reason == "" {
		t.Errorf("forEach over empty list should skip: %+v", res.Steps[1])
	}
	if res.Phase != "Succeeded" {
		t.Errorf("phase = %q", res.Phase)
	}
}
```
{% endraw %}
{% endraw %}

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/engine/ -run TestExecuteForEach -v`
Expected: FAIL — `StepResult.Iterations`/`Note` undefined and forEach not executed.

- [ ] **Step 3: Add iteration types + consts to `internal/engine/engine.go`**

Add the consts to the existing `const (...)` block (after `PhaseFailed`):

```go
	defaultMaxItems = 5
	maxItemsCeiling = 20
```

Add the `IterationResult` type and extend `StepResult` (replace the `StepResult` struct):

```go
type IterationResult struct {
	Item    map[string]string
	Outcome string // completed | failed
	Outputs methods.Outputs
	Error   string
}

type StepResult struct {
	Name       string
	Outcome    string // completed | skipped | failed
	Reason     string // why skipped/failed
	Outputs    methods.Outputs
	Error      string
	Iterations []IterationResult // populated for forEach steps
	Note       string            // forEach truncation note
}
```

- [ ] **Step 4: Branch `runStep` and add the forEach executor in `internal/engine/engine.go`**

At the very start of `runStep` (before `sr := StepResult{Name: step.Name}`), add:

```go
	if step.ForEach != "" {
		return e.runForEachStep(ctx, uc, step, inputs, state)
	}
```

Then add these two functions (e.g. after `runStep`):

```go
func (e *Engine) runForEachStep(ctx context.Context, uc *v1alpha1.UseCase, step v1alpha1.Step,
	inputs map[string]string, state map[string]*StepResult) StepResult {

	sr := StepResult{Name: step.Name}

	m, ok := e.Registry.Get(step.Method)
	if !ok {
		sr.Outcome, sr.Error = OutcomeFailed, fmt.Sprintf("unknown method %q", step.Method)
		return sr
	}

	refs, _ := ExtractRefs(step.ForEach)
	if len(refs) != 1 || refs[0].Kind != "steps" {
		sr.Outcome, sr.Error = OutcomeFailed, "invalid forEach reference"
		return sr
	}
	listRef := refs[0]
	dep := state[listRef.Step]
	if dep == nil || dep.Outcome != OutcomeCompleted {
		sr.Outcome = OutcomeSkipped
		sr.Reason = fmt.Sprintf("depends on step %q which did not complete", listRef.Step)
		return sr
	}
	raw, ok := dep.Outputs[listRef.Field]
	if !ok {
		sr.Outcome, sr.Error = OutcomeFailed, fmt.Sprintf("step %q has no output %q", listRef.Step, listRef.Field)
		return sr
	}
	items, ok := raw.([]map[string]any)
	if !ok {
		sr.Outcome, sr.Error = OutcomeFailed, fmt.Sprintf("$(steps.%s.%s) is not a list output", listRef.Step, listRef.Field)
		return sr
	}

	if step.When != "" {
		scope := scopeBefore(uc, step.Name, e.Registry)
		w, err := CompileWhen(step.When, scope)
		if err != nil {
			sr.Outcome, sr.Error = OutcomeFailed, fmt.Sprintf("when: %v", err)
			return sr
		}
		match, err := w.Eval(func(r Ref) (any, bool) {
			if r.Kind == "inputs" {
				v, ok := inputs[r.Field]
				return v, ok
			}
			d := state[r.Step]
			if d == nil || d.Outputs == nil {
				return nil, false
			}
			v, ok := d.Outputs[r.Field]
			return v, ok
		})
		if err != nil {
			sr.Outcome, sr.Error = OutcomeFailed, fmt.Sprintf("when: %v", err)
			return sr
		}
		if !match {
			sr.Outcome, sr.Reason = OutcomeSkipped, "when evaluated to false"
			return sr
		}
	}

	if len(items) == 0 {
		sr.Outcome, sr.Reason = OutcomeSkipped, "no items matched"
		return sr
	}

	limit := step.MaxItems
	if limit == 0 {
		limit = defaultMaxItems
	}
	if limit > maxItemsCeiling {
		limit = maxItemsCeiling
	}
	n := limit
	if n > len(items) {
		n = len(items)
	}
	if n < len(items) {
		sr.Note = fmt.Sprintf("matched %d, checked %d (worst-first); %d not examined", len(items), n, len(items)-n)
	}

	for _, item := range items[:n] {
		sr.Iterations = append(sr.Iterations, e.runIteration(ctx, m, step, inputs, state, item))
	}
	sr.Outcome = OutcomeCompleted
	return sr
}

func (e *Engine) runIteration(ctx context.Context, m methods.Method, step v1alpha1.Step,
	inputs map[string]string, state map[string]*StepResult, item map[string]any) IterationResult {

	itemStr := map[string]string{}
	for k, v := range item {
		itemStr[k] = fmt.Sprintf("%v", v)
	}
	ir := IterationResult{Item: itemStr}

	lookup := func(r Ref) (string, bool) {
		switch r.Kind {
		case "item":
			v, ok := item[r.Field]
			if !ok {
				return "", false
			}
			return fmt.Sprintf("%v", v), true
		case "inputs":
			v, ok := inputs[r.Field]
			return v, ok
		case "steps":
			d := state[r.Step]
			if d == nil || d.Outputs == nil {
				return "", false
			}
			v, ok := d.Outputs[r.Field]
			if !ok {
				return "", false
			}
			return fmt.Sprintf("%v", v), true
		}
		return "", false
	}

	params := map[string]string{}
	for k, v := range step.With {
		resolved, err := Substitute(v, lookup)
		if err != nil {
			ir.Outcome, ir.Error = OutcomeFailed, fmt.Sprintf("with.%s: %v", k, err)
			return ir
		}
		params[k] = resolved
	}
	if err := methods.ValidateParams(m, params); err != nil {
		ir.Outcome, ir.Error = OutcomeFailed, err.Error()
		return ir
	}

	stepCtx, cancel := context.WithTimeout(ctx, e.StepTimeout)
	defer cancel()
	outputs, err := m.Run(stepCtx, e.Deps, params)
	if err != nil {
		ir.Outcome, ir.Error = OutcomeFailed, err.Error()
		return ir
	}
	ir.Outcome, ir.Outputs = OutcomeCompleted, outputs
	return ir
}
```

- [ ] **Step 5: Run engine test to verify it passes**

Run: `go test ./internal/engine/... && go build ./... && go vet ./...`
Expected: PASS (forEach + all existing engine tests); build+vet clean.

- [ ] **Step 6: Map iterations into the Run — edit `internal/store/store.go`**

In `SaveRun`, replace the step-building loop with:

```go
	steps := make([]v1alpha1.RunStep, 0, len(res.Steps))
	for _, sr := range res.Steps {
		rs := v1alpha1.RunStep{Name: sr.Name, Outcome: sr.Outcome, Reason: sr.Reason, Error: sr.Error, Note: sr.Note}
		if len(sr.Outputs) > 0 {
			raw, err := json.Marshal(sr.Outputs)
			if err != nil {
				return nil, fmt.Errorf("marshal outputs for step %s: %w", sr.Name, err)
			}
			rs.Outputs = &apiextensionsv1.JSON{Raw: raw}
		}
		for _, it := range sr.Iterations {
			ri := v1alpha1.RunStepIteration{Item: it.Item, Outcome: it.Outcome, Error: it.Error}
			if len(it.Outputs) > 0 {
				raw, err := json.Marshal(it.Outputs)
				if err != nil {
					return nil, fmt.Errorf("marshal iteration outputs for step %s: %w", sr.Name, err)
				}
				ri.Outputs = &apiextensionsv1.JSON{Raw: raw}
			}
			rs.Iterations = append(rs.Iterations, ri)
		}
		steps = append(steps, rs)
	}
```

- [ ] **Step 7: Add a store test case**

Append to `internal/store/store_test.go`:

{% raw %}
{% raw %}
```go
func TestSaveRunPersistsIterations(t *testing.T) {
	c := newFakeClient(t)
	s := &Store{Client: c, Namespace: "kato"}
	res := engine.Result{
		Phase: "Succeeded",
		Steps: []engine.StepResult{{
			Name: "check", Outcome: "completed",
			Note: "matched 3, checked 2 (worst-first); 1 not examined",
			Iterations: []engine.IterationResult{
				{Item: map[string]string{"name": "b"}, Outcome: "completed", Outputs: map[string]any{"restartCount": int64(9)}},
				{Item: map[string]string{"name": "a"}, Outcome: "failed", Error: "boom"},
			},
		}},
	}
	run, err := s.SaveRun(context.Background(), "fe", map[string]string{"workload": "nld"}, res,
		time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC), time.Date(2026, 6, 12, 10, 0, 1, 0, time.UTC))
	if err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	var got v1alpha1.Run
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(run), &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	st := got.Status.Steps[0]
	if st.Note == "" || len(st.Iterations) != 2 {
		t.Fatalf("iterations not persisted: %+v", st)
	}
	if st.Iterations[1].Outcome != "failed" || st.Iterations[1].Error != "boom" {
		t.Errorf("iteration1 = %+v", st.Iterations[1])
	}
	if st.Iterations[0].Outputs == nil {
		t.Error("iteration0 outputs not persisted")
	}
}
```
{% endraw %}
{% endraw %}

- [ ] **Step 8: Run store test to verify it passes**

Run: `go test ./internal/store/... && go build ./... && go vet ./...`
Expected: PASS; build+vet clean.

- [ ] **Step 9: Commit** (skip in no-commit mode)

```bash
git add internal/engine internal/store
git commit -m "feat: forEach iteration execution and Run iteration persistence"
```

---

### Task 7: Render `forEach` iterations in the LLM evidence

**Files:**
- Modify: `internal/summarizer/summarizer.go`
- Test: `internal/summarizer/summarizer_foreach_test.go`

- [ ] **Step 1: Write the failing test**

`internal/summarizer/summarizer_foreach_test.go`:

{% raw %}
{% raw %}
```go
package summarizer

import (
	"strings"
	"testing"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/engine"
	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

func TestBuildEvidenceRendersIterations(t *testing.T) {
	uc := &v1alpha1.UseCase{Spec: v1alpha1.UseCaseSpec{
		Steps: []v1alpha1.Step{
			{Name: "check", Method: "check_pod_status", ForEach: "$(steps.crashing.pods)",
				SummaryFilter: []string{"restartCount"}}, // only restartCount per iteration
		},
		Summary: v1alpha1.SummarySpec{Prompt: "x"},
	}}
	steps := []engine.StepResult{{
		Name: "check", Outcome: "completed",
		Note: "matched 3, checked 2 (worst-first); 1 not examined",
		Iterations: []engine.IterationResult{
			{Item: map[string]string{"name": "b", "namespace": "kube-system"}, Outcome: "completed",
				Outputs: methods.Outputs{"restartCount": int64(9), "phase": "Running"}},
			{Item: map[string]string{"name": "a"}, Outcome: "failed", Error: "logs unavailable"},
		},
	}}
	ev := BuildEvidence(uc, steps)
	if !strings.Contains(ev, "not examined") {
		t.Error("note missing")
	}
	if !strings.Contains(ev, `"b"`) && !strings.Contains(ev, "b") {
		t.Error("item identity missing")
	}
	if !strings.Contains(ev, "restartCount") || !strings.Contains(ev, "9") {
		t.Error("filtered iteration output missing")
	}
	if strings.Contains(ev, "phase") { // filtered out by summaryFilter
		t.Error("non-filtered field leaked into iteration evidence")
	}
	if !strings.Contains(ev, "logs unavailable") {
		t.Error("failed iteration error missing")
	}
}
```
{% endraw %}
{% endraw %}

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/summarizer/ -run TestBuildEvidenceRendersIterations -v`
Expected: FAIL — iterations not rendered.

- [ ] **Step 3: Render iterations in `BuildEvidence` (`internal/summarizer/summarizer.go`)**

Inside the `for _, sr := range steps` loop, after the existing scalar-outputs block (the `if len(sr.Outputs) > 0 { ... }` block) and before the trailing `b.WriteByte('\n')`, add:

```go
		if len(sr.Iterations) > 0 {
			if sr.Note != "" {
				fmt.Fprintf(&b, "note: %s\n", sr.Note)
			}
			allow, hasFilter := filters[sr.Name]
			for _, it := range sr.Iterations {
				fmt.Fprintf(&b, "- item %v (%s)\n", it.Item, it.Outcome)
				if it.Error != "" {
					fmt.Fprintf(&b, "  error: %s\n", it.Error)
				}
				if len(it.Outputs) > 0 {
					shown := map[string]any(it.Outputs)
					if hasFilter {
						shown = map[string]any{}
						for _, f := range allow {
							if v, present := it.Outputs[f]; present {
								shown[f] = v
							}
						}
					}
					j, _ := json.MarshalIndent(shown, "  ", "  ")
					b.WriteString("  ")
					b.Write(j)
					b.WriteByte('\n')
				}
			}
		}
```

(`excluded` already skips the whole step earlier in the loop, so excluded forEach steps render nothing — consistent with scalar steps.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/summarizer/... && go build ./... && go vet ./...`
Expected: PASS; build+vet clean.

- [ ] **Step 5: Commit** (skip in no-commit mode)

```bash
git add internal/summarizer
git commit -m "feat: render forEach iterations in LLM evidence"
```

---

### Task 8: API surface, RBAC, docs, example, and full verification

**Files:**
- Modify: `internal/server/server.go`, `charts/kato/templates/rbac.yaml`, `docs/METHOD.md`
- Create: `examples/usecases/daemonset-crashloop.yaml`
- Test: `internal/server/server_test.go` (add a case)

- [ ] **Step 1: Write the failing server test**

Append to `internal/server/server_test.go`:

```go
func TestListMethodsIncludesListOutputs(t *testing.T) {
	s := testServer(sampleUseCase(), true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/methods", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(body, "list_failing_pods") {
		t.Error("list_failing_pods missing from methods")
	}
	// The list output and its item fields must be exposed.
	if !strings.Contains(body, `"pods"`) || !strings.Contains(body, "restartCount") {
		t.Errorf("list output / item fields missing: %s", body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestListMethodsIncludesListOutputs -v`
Expected: FAIL — list outputs not in the methods response.

- [ ] **Step 3: Expose list outputs in `internal/server/server.go`**

Extend the `methodView` struct and `listMethods` handler. Replace the `methodView` type and the `listMethods` function with:

```go
type itemFieldView struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

type listOutputView struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	ItemFields  []itemFieldView `json:"itemFields"`
}

type methodView struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Params      []methods.Param       `json:"params"`
	Outputs     []methods.OutputField `json:"outputs"`
	Lists       []listOutputView      `json:"lists,omitempty"`
}

func (s *Server) listMethods(w http.ResponseWriter, _ *http.Request) {
	views := []methodView{}
	for _, m := range s.Registry.All() {
		mv := methodView{
			Name: m.Name(), Description: m.Description(),
			Params: m.Params(), Outputs: m.OutputFields(),
		}
		for _, lo := range methods.ListOutputsOf(m) {
			lv := listOutputView{Name: lo.Name, Description: lo.Description}
			for _, f := range lo.ItemFields {
				lv.ItemFields = append(lv.ItemFields, itemFieldView{
					Name: f.Name, Type: string(f.Type), Description: f.Description,
				})
			}
			mv.Lists = append(mv.Lists, lv)
		}
		views = append(views, mv)
	}
	writeJSON(w, 200, map[string]any{"methods": views})
}
```

- [ ] **Step 4: Run server test to verify it passes**

Run: `go test ./internal/server/... -run TestListMethods -v`
Expected: PASS.

- [ ] **Step 5: Add `statefulsets` read to RBAC**

In `charts/kato/templates/rbac.yaml`, change the apps rule resources to include `statefulsets`:

```yaml
  - apiGroups: ["apps"]
    resources: [deployments, replicasets, daemonsets, statefulsets]
    verbs: [get, list, watch]
```

- [ ] **Step 6: Create the example UseCase `examples/usecases/daemonset-crashloop.yaml`**

```yaml
apiVersion: kato.zufardhiyaulhaq.com/v1alpha1
kind: UseCase
metadata:
  name: workload-crashloop
spec:
  description: "List a workload's failing pods and fetch logs for the worst few"
  inputs:
    - name: namespace
      required: true
    - name: kind
      required: true
    - name: workload
      required: true
  steps:
    - name: crashing
      method: list_failing_pods
      with:
        namespace: $(inputs.namespace)
        kind: $(inputs.kind)
        name: $(inputs.workload)
    - name: logs
      forEach: $(steps.crashing.pods)
      maxItems: 3
      when: $(steps.crashing.anyFailing)
      method: check_pod_logs
      with:
        namespace: $(item.namespace)
        name: $(item.name)
        previous: "true"
      summaryFilter: [logs]
  summary:
    prompt: |
      Several pods of this workload are failing. Using each pod's reason and
      logs, explain the common root cause and suggest a fix.
```

- [ ] **Step 7: Document `list_failing_pods` in `docs/METHOD.md`**

Add `list_failing_pods` to the index table (under a new "Discovery" grouping or alongside Pod), and add a section. Also add a short note to the "Conventions" section that some methods declare a **list output** consumable only by a `forEach` step. The method section:

```markdown
### `list_failing_pods`

Failing pods of a workload (Deployment / DaemonSet / StatefulSet), worst-first.
Produces a **list output** (`pods`) consumable only by a `forEach` step.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | workload namespace |
| `kind` | yes | `Deployment` \| `DaemonSet` \| `StatefulSet` |
| `name` | yes | workload name |
| `minRestarts` | no | only include pods with restartCount ≥ this (default 0) |
| `includeCrashLoop` | no | count `CrashLoopBackOff` pods (default `true`) |
| `includeImagePull` | no | count `ImagePullBackOff`/`ErrImagePull`/`CreateContainerError` (default `true`) |
| `includeOOM` | no | count `OOMKilled` / non-zero last-exit pods (default `true`) |
| `includeNotReady` | no | count any not-Ready pod (default `false`) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `count` | int | number of failing pods matched |
| `anyFailing` | bool | `count > 0` |

**List output `pods`** (items sorted worst-first by `restartCount`)

| Item field | Type | Description |
|---|---|---|
| `namespace` | string | pod namespace |
| `name` | string | pod name |
| `reason` | string | dominant failure reason (e.g. `CrashLoopBackOff`, `OOMKilled`) |
| `restartCount` | int | max restartCount across the pod's containers |

Reference the list from a `forEach` step: `forEach: $(steps.<step>.pods)`, then
bind `$(item.namespace)` / `$(item.name)` in the step's `with`.
```

- [ ] **Step 8: Full verification**

```bash
go build ./... && go vet ./...
go test ./... -count=1
helm lint charts/kato
make test-integration
```
Expected: build+vet clean; all unit packages pass; `helm lint` reports 0 failed;
envtest controller suite passes.

- [ ] **Step 9: Commit** (skip in no-commit mode)

```bash
git add internal/server charts docs/METHOD.md examples
git commit -m "feat: expose list outputs in API, RBAC statefulsets, docs, example"
```

---

## Spec Coverage Self-Review

| Spec § | Requirement | Task(s) |
|---|---|---|
| §3 | `list_failing_pods`: inputs, criteria, owner resolution (3 kinds), worst-first, scalar+list outputs | Task 2 |
| §4 | list-output model; lists consumable only by forEach; scalar matching preserved | Tasks 1, 5 |
| §5 | `Step.ForEach`/`MaxItems`; `$(item.*)`; forEach references list of earlier step; when scalar-only | Tasks 3, 4, 5 |
| §6 | iteration execution, cap default 5/ceiling 20, worst-first, note, zero-items skip, per-iteration failure; Run iteration records; evidence | Tasks 4, 6, 7 |
| §7 | validation rules (forEach/list/item/maxItems) | Task 5 |
| §8 | files touched | all |
| §9 | testing (methods owner resolution + criteria; engine refs/validation/iteration; summarizer; envtest) | Tasks 2,3,5,6,7,8 |
| §10 | out of scope (nested forEach, block fan-out, item-in-when) — not built; item-in-when explicitly rejected | Tasks 5 (rejects item-in-when) |

No gaps. Notes: the `defaultMaxItems`/`maxItemsCeiling` consts and `IterationResult`/`StepResult.Iterations` names are consistent across Tasks 4, 6, 7. The `methods.ListOutputsOf` helper is used by Tasks 5 (validation), 8 (server) exactly as defined in Task 1.
