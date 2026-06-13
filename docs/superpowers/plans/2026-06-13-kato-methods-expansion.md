# kato Methods Expansion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Take kato's built-in read-only method library from 19 → 25 methods plus 4 field-enrichments, covering DaemonSet/StatefulSet/PVC/Job/CronJob and breaking more troubleshooting signal out of the describe methods.

**Architecture:** Each method is a `methods.Method` struct registered via a file-local `init()` appending to `builtinFns`. New per-describe rendering is centralized in one `render.go` helpers file. Typed client-go clients (`AppsV1`/`CoreV1`/`BatchV1`) from `deps.Kube`; no new dependencies. Status methods error on absence; lookup methods return `exists:false`.

**Tech Stack:** Go 1.24.8, `k8s.io/client-go`, `k8s.io/api`, `sigs.k8s.io/yaml`, `k8s.io/client-go/kubernetes/fake` for tests.

**Spec:** `docs/superpowers/specs/2026-06-13-kato-methods-expansion-design.md`

> **⚠️ Standing constraint for THIS session:** the user said **don't commit**. Every task ends with a *verification* step (run the suite), **not** a git commit. Leave all changes in the working tree. Do not run `git add` / `git commit`.

---

## File Structure

```
internal/methods/
  render.go                     (new)  renderKVMap, renderTolerations, renderOwnerRefs,
                                       renderPodConditions, renderNodeConditions,
                                       probeSummary, renderProbes, renderPorts
  render_test.go                (new)
  pod_describe.go               (edit) +8 fields
  deployment_describe.go        (edit) +10 fields
  node_describe.go              (edit) +7 fields
  service_describe.go           (edit) +6 fields
  daemonset_describe.go         (new)  describe_daemonset + test
  statefulset_status.go         (new)  check_statefulset_status + test
  statefulset_describe.go       (new)  describe_statefulset (+renderVolumeClaimTemplates) + test
  pvc.go                        (new)  check_pvc (+renderAccessModes) + test
  job.go                        (new)  check_job + test
  cronjob.go                    (new)  check_cronjob + test
charts/kato/templates/rbac.yaml (edit) +persistentvolumeclaims, +batch group
docs/METHOD.md                  (edit) new sections + index
charts/kato/README.md.gotmpl    (edit) methods table 19→25  → `make readme` regen
```

Run order matters: **Task 1 (render helpers) first** — Tasks 2–8 depend on it.

---

### Task 1: Shared render helpers

**Files:**
- Create: `internal/methods/render.go`
- Test: `internal/methods/render_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/methods/render_test.go`:

```go
package methods

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestRenderKVMap(t *testing.T) {
	if got := renderKVMap(nil); got != "" {
		t.Errorf("empty = %q, want \"\"", got)
	}
	if got := renderKVMap(map[string]string{"tier": "backend", "app": "api"}); got != "app=api, tier=backend" {
		t.Errorf("renderKVMap = %q", got)
	}
}

func TestRenderTolerations(t *testing.T) {
	if got := renderTolerations(nil); got != "" {
		t.Errorf("empty = %q", got)
	}
	got := renderTolerations([]corev1.Toleration{
		{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
		{Operator: corev1.TolerationOpExists}, // bare exists -> tolerates all
	})
	if got != "dedicated=gpu:NoSchedule, <all>" {
		t.Errorf("renderTolerations = %q", got)
	}
}

func TestRenderOwnerRefs(t *testing.T) {
	if got := renderOwnerRefs([]metav1.OwnerReference{{Kind: "ReplicaSet", Name: "api-abc"}}); got != "ReplicaSet/api-abc" {
		t.Errorf("renderOwnerRefs = %q", got)
	}
}

func TestRenderPodConditions(t *testing.T) {
	got := renderPodConditions([]corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"},
		{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, Reason: "ignored-when-true"},
	})
	if got != "Ready=False (ContainersNotReady), PodScheduled=True" {
		t.Errorf("renderPodConditions = %q", got)
	}
}

func TestRenderProbes(t *testing.T) {
	cs := []corev1.Container{
		{Name: "app", LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Port: intstr.FromInt(8080), Path: "/healthz"}}}},
		{Name: "sidecar"}, // no probes -> omitted entirely
	}
	if got := renderProbes(cs); got != "app: liveness=httpGet:8080/healthz readiness=— startup=—" {
		t.Errorf("renderProbes = %q", got)
	}
	if got := renderProbes([]corev1.Container{{Name: "x"}}); got != "" {
		t.Errorf("no-probe = %q, want \"\"", got)
	}
}

func TestRenderPorts(t *testing.T) {
	got := renderPorts([]corev1.ServicePort{
		{Name: "http", Port: 80, TargetPort: intstr.FromInt(8080), Protocol: corev1.ProtocolTCP},
		{Port: 443, TargetPort: intstr.FromString("https")}, // no protocol -> defaults TCP
	})
	if got != "http:80→8080/TCP, 443→https/TCP" {
		t.Errorf("renderPorts = %q", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/methods/ -run 'TestRender' -v`
Expected: FAIL — compile error, `undefined: renderKVMap` (etc.).

- [ ] **Step 3: Write the implementation**

Create `internal/methods/render.go`:

```go
package methods

import (
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// renderKVMap renders a string map as sorted "k=v, k=v"; "" if empty.
func renderKVMap(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return strings.Join(parts, ", ")
}

// renderTolerations renders tolerations as "key=value:Effect" (key omitted-empty
// becomes "<all>", value omitted when empty, effect omitted when empty),
// comma-joined; "" if none.
func renderTolerations(tols []corev1.Toleration) string {
	if len(tols) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tols))
	for _, t := range tols {
		key := t.Key
		if key == "" {
			key = "<all>"
		}
		seg := key
		if t.Value != "" {
			seg += "=" + t.Value
		}
		if t.Effect != "" {
			seg += ":" + string(t.Effect)
		}
		parts = append(parts, seg)
	}
	return strings.Join(parts, ", ")
}

// renderOwnerRefs renders owner references as "Kind/Name", comma-joined; "" if none.
func renderOwnerRefs(refs []metav1.OwnerReference) string {
	if len(refs) == 0 {
		return ""
	}
	parts := make([]string, len(refs))
	for i, r := range refs {
		parts[i] = r.Kind + "/" + r.Name
	}
	return strings.Join(parts, ", ")
}

// condTuple is the common (type,status,reason) shape we render; the typed
// PodCondition / NodeCondition slices are converted into it at the call site so
// rendering stays type-safe and DRY.
type condTuple struct{ Type, Status, Reason string }

// renderConditionTuples renders "Type=Status (Reason)"; the reason is appended
// only when non-empty AND the status is not "True". "" if none.
func renderConditionTuples(cs []condTuple) string {
	if len(cs) == 0 {
		return ""
	}
	parts := make([]string, len(cs))
	for i, c := range cs {
		seg := c.Type + "=" + c.Status
		if c.Reason != "" && c.Status != "True" {
			seg += " (" + c.Reason + ")"
		}
		parts[i] = seg
	}
	return strings.Join(parts, ", ")
}

func renderPodConditions(cs []corev1.PodCondition) string {
	t := make([]condTuple, len(cs))
	for i, c := range cs {
		t[i] = condTuple{string(c.Type), string(c.Status), c.Reason}
	}
	return renderConditionTuples(t)
}

func renderNodeConditions(cs []corev1.NodeCondition) string {
	t := make([]condTuple, len(cs))
	for i, c := range cs {
		t[i] = condTuple{string(c.Type), string(c.Status), c.Reason}
	}
	return renderConditionTuples(t)
}

// probeSummary renders a probe handler compactly, or "—" if the probe is nil.
func probeSummary(p *corev1.Probe) string {
	if p == nil {
		return "—"
	}
	switch {
	case p.HTTPGet != nil:
		return fmt.Sprintf("httpGet:%s%s", p.HTTPGet.Port.String(), p.HTTPGet.Path)
	case p.TCPSocket != nil:
		return "tcp:" + p.TCPSocket.Port.String()
	case p.GRPC != nil:
		return fmt.Sprintf("grpc:%d", p.GRPC.Port)
	case p.Exec != nil:
		return "exec"
	default:
		return "—"
	}
}

// renderProbes renders per-container probe summaries, one entry each:
// "app: liveness=httpGet:8080/healthz readiness=tcp:8080 startup=—".
// Containers with no probes are omitted; "" if none anywhere.
func renderProbes(cs []corev1.Container) string {
	var lines []string
	for _, c := range cs {
		if c.LivenessProbe == nil && c.ReadinessProbe == nil && c.StartupProbe == nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: liveness=%s readiness=%s startup=%s",
			c.Name, probeSummary(c.LivenessProbe), probeSummary(c.ReadinessProbe), probeSummary(c.StartupProbe)))
	}
	return strings.Join(lines, "; ")
}

// renderPorts renders service ports as "name:port→targetPort/Protocol" (name
// omitted when empty, protocol defaulting to TCP), comma-joined; "" if none.
func renderPorts(ports []corev1.ServicePort) string {
	if len(ports) == 0 {
		return ""
	}
	parts := make([]string, len(ports))
	for i, p := range ports {
		seg := ""
		if p.Name != "" {
			seg = p.Name + ":"
		}
		proto := string(p.Protocol)
		if proto == "" {
			proto = "TCP"
		}
		parts[i] = seg + fmt.Sprintf("%d→%s/%s", p.Port, p.TargetPort.String(), proto)
	}
	return strings.Join(parts, ", ")
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/methods/ -run 'TestRender' -v`
Expected: PASS (all 6 tests).

- [ ] **Step 5: Verify the full package still builds/tests**

Run: `go test ./internal/methods/ -count=1`
Expected: PASS.

---

### Task 2: Enrich `describe_pod`

**Files:**
- Modify: `internal/methods/pod_describe.go`
- Test: `internal/methods/pod_describe_test.go`

- [ ] **Step 1: Write the failing test** — append to `internal/methods/pod_describe_test.go`:

```go
func TestDescribePodTroubleshootingFields(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-1", Namespace: "payments",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "app-rs-abc"}},
		},
		Spec: corev1.PodSpec{
			NodeName:          "node-3",
			PriorityClassName: "high",
			HostNetwork:       true,
			NodeSelector:      map[string]string{"disktype": "ssd"},
			Tolerations:       []corev1.Toleration{{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}},
			Containers: []corev1.Container{{
				Name: "app", Image: "app:v1",
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
					TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(8080)}}},
			}},
		},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"},
		}},
	}
	client := fake.NewSimpleClientset(pod)
	m, _ := Builtin().Get("describe_pod")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"nodeName":          "node-3",
		"priorityClassName": "high",
		"hostNetwork":       true,
		"nodeSelector":      "disktype=ssd",
		"tolerations":       "dedicated=gpu:NoSchedule",
		"ownerReferences":   "ReplicaSet/app-rs-abc",
		"conditions":        "Ready=False (ContainersNotReady)",
		"probes":            "app: liveness=— readiness=tcp:8080 startup=—",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}
```

Add `"k8s.io/apimachinery/pkg/util/intstr"` to the test file's imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/methods/ -run TestDescribePodTroubleshootingFields -v`
Expected: FAIL — fields return `<nil>` (not declared/populated).

- [ ] **Step 3: Add the fields** — in `internal/methods/pod_describe.go`, extend `OutputFields()` (append before the closing `}` of the returned slice, after the `manifest` entry):

```go
		{Name: "nodeName", Type: FieldString, Description: `scheduled node, "" if unscheduled`},
		{Name: "conditions", Type: FieldString, Description: "PodScheduled/Initialized/ContainersReady/Ready as Type=Status (Reason)"},
		{Name: "probes", Type: FieldString, Description: "per-container liveness/readiness/startup probe summary"},
		{Name: "ownerReferences", Type: FieldString, Description: `controllers owning the pod, e.g. "ReplicaSet/api-abc"`},
		{Name: "nodeSelector", Type: FieldString, Description: `pod nodeSelector, "" if none`},
		{Name: "tolerations", Type: FieldString, Description: `pod tolerations, "" if none`},
		{Name: "priorityClassName", Type: FieldString, Description: `priority class, "" if none`},
		{Name: "hostNetwork", Type: FieldBool, Description: "pod uses host network"},
```

And extend the `Outputs{...}` returned by `Run` (append after the `manifest` entry):

```go
		"nodeName":          pod.Spec.NodeName,
		"conditions":        renderPodConditions(pod.Status.Conditions),
		"probes":            renderProbes(pod.Spec.Containers),
		"ownerReferences":   renderOwnerRefs(pod.OwnerReferences),
		"nodeSelector":      renderKVMap(pod.Spec.NodeSelector),
		"tolerations":       renderTolerations(pod.Spec.Tolerations),
		"priorityClassName": pod.Spec.PriorityClassName,
		"hostNetwork":       pod.Spec.HostNetwork,
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/methods/ -run 'TestDescribePod' -v`
Expected: PASS (existing `TestDescribePodSanitizes`/`TestDescribePodStructuredOutputs` + new one).

- [ ] **Step 5: Verify the suite**

Run: `go test ./internal/methods/ -count=1`
Expected: PASS.

---

### Task 3: Enrich `describe_deployment`

**Files:**
- Modify: `internal/methods/deployment_describe.go`
- Test: `internal/methods/workloads_test.go` (existing workloads test file)

- [ ] **Step 1: Write the failing test** — append to `internal/methods/workloads_test.go`:

```go
func TestDescribeDeploymentRolloutFields(t *testing.T) {
	ms := intstr.FromString("25%")
	mu := intstr.FromInt(1)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: i32(3),
			Paused:   true,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Strategy: appsv1.DeploymentStrategy{
				Type:          appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{MaxSurge: &ms, MaxUnavailable: &mu},
			},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				NodeSelector: map[string]string{"tier": "be"},
				Containers: []corev1.Container{{
					Name: "api", Image: "api:v1",
					LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{Port: intstr.FromInt(8080), Path: "/live"}}},
				}},
			}},
		},
	}
	client := fake.NewSimpleClientset(d)
	m, _ := Builtin().Get("describe_deployment")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "name": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"replicas":       int64(3),
		"paused":         true,
		"selector":       "app=api",
		"maxSurge":       "25%",
		"maxUnavailable": "1",
		"nodeSelector":   "tier=be",
		"probes":         "api: liveness=httpGet:8080/live readiness=— startup=—",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}
```

Ensure `workloads_test.go` imports `appsv1 "k8s.io/api/apps/v1"`, `corev1`, `metav1`, `"k8s.io/apimachinery/pkg/util/intstr"`, `"k8s.io/client-go/kubernetes/fake"`, `context`, `testing` (add any missing). `i32` is already defined in `hpa_test.go`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/methods/ -run TestDescribeDeploymentRolloutFields -v`
Expected: FAIL — new fields `<nil>`.

- [ ] **Step 3: Add the fields** — in `internal/methods/deployment_describe.go`, append to `OutputFields()` after the `manifest` entry:

```go
		{Name: "replicas", Type: FieldInt, Description: "spec.replicas (1 if unset)"},
		{Name: "selector", Type: FieldString, Description: "spec.selector matchLabels"},
		{Name: "maxSurge", Type: FieldString, Description: `RollingUpdate maxSurge, "" for Recreate`},
		{Name: "maxUnavailable", Type: FieldString, Description: `RollingUpdate maxUnavailable, "" for Recreate`},
		{Name: "minReadySeconds", Type: FieldInt, Description: "spec.minReadySeconds"},
		{Name: "revisionHistoryLimit", Type: FieldInt, Description: "spec.revisionHistoryLimit, -1 if unset"},
		{Name: "paused", Type: FieldBool, Description: "spec.paused"},
		{Name: "probes", Type: FieldString, Description: "per-container probe summary (pod template)"},
		{Name: "nodeSelector", Type: FieldString, Description: "pod template nodeSelector"},
		{Name: "tolerations", Type: FieldString, Description: "pod template tolerations"},
```

In `Run`, after the existing `tmpl := d.Spec.Template.Spec` line, add:

```go
	replicas := int64(1)
	if d.Spec.Replicas != nil {
		replicas = int64(*d.Spec.Replicas)
	}
	revHist := int64(-1)
	if d.Spec.RevisionHistoryLimit != nil {
		revHist = int64(*d.Spec.RevisionHistoryLimit)
	}
	maxSurge, maxUnavailable := "", ""
	if ru := d.Spec.Strategy.RollingUpdate; ru != nil {
		if ru.MaxSurge != nil {
			maxSurge = ru.MaxSurge.String()
		}
		if ru.MaxUnavailable != nil {
			maxUnavailable = ru.MaxUnavailable.String()
		}
	}
	selector := ""
	if d.Spec.Selector != nil {
		selector = renderKVMap(d.Spec.Selector.MatchLabels)
	}
```

And append to the returned `Outputs{...}` (after `manifest`):

```go
		"replicas":             replicas,
		"selector":             selector,
		"maxSurge":             maxSurge,
		"maxUnavailable":       maxUnavailable,
		"minReadySeconds":      int64(d.Spec.MinReadySeconds),
		"revisionHistoryLimit": revHist,
		"paused":               d.Spec.Paused,
		"probes":               renderProbes(tmpl.Containers),
		"nodeSelector":         renderKVMap(tmpl.NodeSelector),
		"tolerations":          renderTolerations(tmpl.Tolerations),
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/methods/ -run TestDescribeDeployment -v`
Expected: PASS.

- [ ] **Step 5: Verify the suite**

Run: `go test ./internal/methods/ -count=1`
Expected: PASS.

---

### Task 4: Enrich `describe_node`

**Files:**
- Modify: `internal/methods/node_describe.go`
- Test: `internal/methods/node_test.go`

- [ ] **Step 1: Write the failing test** — append to `internal/methods/node_test.go`:

```go
func TestDescribeNodeInfoFields(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Spec:       corev1.NodeSpec{Unschedulable: true},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				KubeletVersion:          "v1.30.2",
				OSImage:                 "Ubuntu 22.04",
				KernelVersion:           "5.15.0",
				ContainerRuntimeVersion: "containerd://1.7.2",
			},
			Capacity: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("110")},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
			},
		},
	}
	client := fake.NewSimpleClientset(node)
	m, _ := Builtin().Get("describe_node")
	out, err := m.Run(context.Background(), Deps{Kube: client}, map[string]string{"name": "node-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"kubeletVersion":   "v1.30.2",
		"osImage":          "Ubuntu 22.04",
		"kernelVersion":    "5.15.0",
		"containerRuntime": "containerd://1.7.2",
		"capacityPods":     "110",
		"unschedulable":    true,
		"conditions":       "Ready=True, MemoryPressure=False",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}
```

Ensure `node_test.go` imports `"k8s.io/apimachinery/pkg/api/resource"` (add if missing).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/methods/ -run TestDescribeNodeInfoFields -v`
Expected: FAIL — new fields `<nil>`.

- [ ] **Step 3: Add the fields** — in `internal/methods/node_describe.go`, append to `OutputFields()` after `manifest`:

```go
		{Name: "kubeletVersion", Type: FieldString, Description: "status.nodeInfo.kubeletVersion"},
		{Name: "osImage", Type: FieldString, Description: "status.nodeInfo.osImage"},
		{Name: "kernelVersion", Type: FieldString, Description: "status.nodeInfo.kernelVersion"},
		{Name: "containerRuntime", Type: FieldString, Description: "status.nodeInfo.containerRuntimeVersion"},
		{Name: "capacityPods", Type: FieldString, Description: "status.capacity.pods (scheduling ceiling)"},
		{Name: "unschedulable", Type: FieldBool, Description: "spec.unschedulable (cordoned)"},
		{Name: "conditions", Type: FieldString, Description: "node conditions as Type=Status (Reason)"},
```

In `Run`, append to the returned `Outputs{...}` (after `manifest`). Use the original `node` for structured reads (note `n` already exists as the sanitized deep copy whose `Status.Images` was nilled):

```go
		"kubeletVersion":   node.Status.NodeInfo.KubeletVersion,
		"osImage":          node.Status.NodeInfo.OSImage,
		"kernelVersion":    node.Status.NodeInfo.KernelVersion,
		"containerRuntime": node.Status.NodeInfo.ContainerRuntimeVersion,
		"capacityPods":     node.Status.Capacity.Pods().String(),
		"unschedulable":    node.Spec.Unschedulable,
		"conditions":       renderNodeConditions(node.Status.Conditions),
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/methods/ -run TestDescribeNode -v`
Expected: PASS.

- [ ] **Step 5: Verify the suite**

Run: `go test ./internal/methods/ -count=1`
Expected: PASS.

---

### Task 5: Enrich `describe_service`

**Files:**
- Modify: `internal/methods/service_describe.go`
- Test: `internal/methods/networking_test.go`

- [ ] **Step 1: Write the failing test** — append to `internal/methods/networking_test.go`:

```go
func TestDescribeServiceStructuredFields(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: "10.0.0.5",
			Selector:  map[string]string{"app": "api"},
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, TargetPort: intstr.FromInt(8080), Protocol: corev1.ProtocolTCP},
			},
		},
	}
	client := fake.NewSimpleClientset(svc)
	m, _ := Builtin().Get("describe_service")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "name": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"type":      "ClusterIP",
		"clusterIP": "10.0.0.5",
		"selector":  "app=api",
		"ports":     "http:80→8080/TCP",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
	if _, ok := out["manifest"]; !ok {
		t.Error("manifest output dropped")
	}
}
```

Ensure `networking_test.go` imports `"k8s.io/apimachinery/pkg/util/intstr"` (add if missing).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/methods/ -run TestDescribeServiceStructuredFields -v`
Expected: FAIL — new fields `<nil>`.

- [ ] **Step 3: Replace `OutputFields()` and `Run`** in `internal/methods/service_describe.go`:

```go
func (describeService) OutputFields() []OutputField {
	return []OutputField{
		{Name: "type", Type: FieldString, Description: "ClusterIP|NodePort|LoadBalancer|ExternalName"},
		{Name: "clusterIP", Type: FieldString, Description: `cluster IP, "None" for headless, "" if unset`},
		{Name: "selector", Type: FieldString, Description: `pod selector, "" if selector-less`},
		{Name: "ports", Type: FieldString, Description: "rendered port→targetPort/Protocol list"},
		{Name: "externalName", Type: FieldString, Description: `spec.externalName, "" unless type ExternalName`},
		{Name: "loadBalancerIngress", Type: FieldString, Description: `LB IP/hostname(s), "" if none/pending`},
		{Name: "manifest", Type: FieldString, Description: "YAML manifest, managedFields stripped"},
	}
}

func (describeService) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	svc, err := deps.Kube.CoreV1().Services(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get service %s/%s: %w", params["namespace"], params["name"], err)
	}
	s := svc.DeepCopy()
	sanitizeObjectMeta(&s.ObjectMeta)
	y, err := yaml.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshal service: %w", err)
	}
	lbParts := make([]string, 0, len(svc.Status.LoadBalancer.Ingress))
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			lbParts = append(lbParts, ing.IP)
		} else if ing.Hostname != "" {
			lbParts = append(lbParts, ing.Hostname)
		}
	}
	return Outputs{
		"type":                string(svc.Spec.Type),
		"clusterIP":           svc.Spec.ClusterIP,
		"selector":            renderKVMap(svc.Spec.Selector),
		"ports":               renderPorts(svc.Spec.Ports),
		"externalName":        svc.Spec.ExternalName,
		"loadBalancerIngress": strings.Join(lbParts, ", "),
		"manifest":            Truncate(string(y), defaultLogBytes),
	}, nil
}
```

Update the import block of `service_describe.go` to add `"strings"`:

```go
import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/methods/ -run TestDescribeService -v`
Expected: PASS.

- [ ] **Step 5: Verify the suite**

Run: `go test ./internal/methods/ -count=1`
Expected: PASS.

---

### Task 6: New method `describe_daemonset`

**Files:**
- Create: `internal/methods/daemonset_describe.go`
- Test: `internal/methods/daemonset_describe_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/methods/daemonset_describe_test.go`:

```go
package methods

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDescribeDaemonSet(t *testing.T) {
	mu := intstr.FromInt(1)
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "fluentd", Namespace: "logging"},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "fluentd"}},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type:          appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{MaxUnavailable: &mu},
			},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				ServiceAccountName: "fluentd-sa",
				NodeSelector:       map[string]string{"role": "log"},
				Tolerations:        []corev1.Toleration{{Key: "node-role", Effect: corev1.TaintEffectNoSchedule}},
				Containers:         []corev1.Container{{Name: "fluentd", Image: "fluentd:v1"}},
			}},
		},
	}
	client := fake.NewSimpleClientset(ds)
	m, ok := Builtin().Get("describe_daemonset")
	if !ok {
		t.Fatal("describe_daemonset not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "logging", "name": "fluentd"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"containers":     "fluentd",
		"images":         "fluentd:v1",
		"serviceAccount": "fluentd-sa",
		"selector":       "app=fluentd",
		"updateStrategy": "RollingUpdate",
		"maxUnavailable": "1",
		"nodeSelector":   "role=log",
		"tolerations":    "node-role:NoSchedule",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/methods/ -run TestDescribeDaemonSet -v`
Expected: FAIL — `describe_daemonset not registered`.

- [ ] **Step 3: Write the implementation** — create `internal/methods/daemonset_describe.go`:

```go
package methods

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

type describeDaemonSet struct{}

func (describeDaemonSet) Name() string        { return "describe_daemonset" }
func (describeDaemonSet) Description() string { return "Sanitized daemonset manifest (spec+status)" }

func (describeDaemonSet) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "DaemonSet namespace"},
		{Name: "name", Required: true, Description: "DaemonSet name"},
	}
}

func (describeDaemonSet) OutputFields() []OutputField {
	return []OutputField{
		{Name: "containers", Type: FieldString, Description: "comma-separated container names (pod template)"},
		{Name: "images", Type: FieldString, Description: "comma-separated container images (pod template)"},
		{Name: "resourceRequests", Type: FieldString, Description: `per-container CPU/memory requests; "" if none set`},
		{Name: "resourceLimits", Type: FieldString, Description: `per-container CPU/memory limits; "" if none set`},
		{Name: "serviceAccount", Type: FieldString, Description: `pod template's service account, "" if default`},
		{Name: "selector", Type: FieldString, Description: "spec.selector matchLabels"},
		{Name: "updateStrategy", Type: FieldString, Description: "RollingUpdate|OnDelete"},
		{Name: "maxUnavailable", Type: FieldString, Description: `RollingUpdate maxUnavailable, "" for OnDelete`},
		{Name: "nodeSelector", Type: FieldString, Description: "pod template nodeSelector (which nodes the DS targets)"},
		{Name: "tolerations", Type: FieldString, Description: "pod template tolerations"},
		{Name: "probes", Type: FieldString, Description: "per-container probe summary"},
		{Name: "manifest", Type: FieldString, Description: "full YAML manifest; env values redacted, managedFields stripped"},
	}
}

func (describeDaemonSet) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	ds, err := deps.Kube.AppsV1().DaemonSets(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get daemonset %s/%s: %w", params["namespace"], params["name"], err)
	}
	d := ds.DeepCopy()
	sanitizeObjectMeta(&d.ObjectMeta)
	for i := range d.Spec.Template.Spec.Containers {
		redactEnv(d.Spec.Template.Spec.Containers[i].Env)
	}
	for i := range d.Spec.Template.Spec.InitContainers {
		redactEnv(d.Spec.Template.Spec.InitContainers[i].Env)
	}
	y, err := yaml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("marshal daemonset: %w", err)
	}
	tmpl := ds.Spec.Template.Spec
	selector := ""
	if ds.Spec.Selector != nil {
		selector = renderKVMap(ds.Spec.Selector.MatchLabels)
	}
	strategy := string(ds.Spec.UpdateStrategy.Type)
	if strategy == "" {
		strategy = "RollingUpdate"
	}
	maxUnavailable := ""
	if ru := ds.Spec.UpdateStrategy.RollingUpdate; ru != nil && ru.MaxUnavailable != nil {
		maxUnavailable = ru.MaxUnavailable.String()
	}
	return Outputs{
		"containers":       containerNames(tmpl.Containers),
		"images":           containerImages(tmpl.Containers),
		"resourceRequests": renderResourceList(tmpl.Containers, false),
		"resourceLimits":   renderResourceList(tmpl.Containers, true),
		"serviceAccount":   tmpl.ServiceAccountName,
		"selector":         selector,
		"updateStrategy":   strategy,
		"maxUnavailable":   maxUnavailable,
		"nodeSelector":     renderKVMap(tmpl.NodeSelector),
		"tolerations":      renderTolerations(tmpl.Tolerations),
		"probes":           renderProbes(tmpl.Containers),
		"manifest":         Truncate(string(y), defaultLogBytes),
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(describeDaemonSet{}) }) }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/methods/ -run TestDescribeDaemonSet -v`
Expected: PASS.

- [ ] **Step 5: Verify the suite**

Run: `go test ./internal/methods/ -count=1`
Expected: PASS.

---

### Task 7: New method `check_statefulset_status`

**Files:**
- Create: `internal/methods/statefulset_status.go`
- Test: `internal/methods/statefulset_status_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/methods/statefulset_status_test.go`:

```go
package methods

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckStatefulSetStatus(t *testing.T) {
	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "data"},
		Spec:       appsv1.StatefulSetSpec{Replicas: i32(3)},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: 2, CurrentReplicas: 3, UpdatedReplicas: 1, AvailableReplicas: 2,
			CurrentRevision: "db-1", UpdateRevision: "db-2",
		},
	}
	client := fake.NewSimpleClientset(ss)
	m, ok := Builtin().Get("check_statefulset_status")
	if !ok {
		t.Fatal("check_statefulset_status not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "data", "name": "db"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"desiredReplicas":       int64(3),
		"readyReplicas":         int64(2),
		"currentReplicas":       int64(3),
		"updatedReplicas":       int64(1),
		"availableReplicas":     int64(2),
		"updateRevisionPending": true,
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}

func TestCheckStatefulSetStatusMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_statefulset_status")
	if _, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "data", "name": "db"}); err == nil {
		t.Fatal("expected error for missing statefulset")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/methods/ -run TestCheckStatefulSetStatus -v`
Expected: FAIL — `check_statefulset_status not registered`.

- [ ] **Step 3: Write the implementation** — create `internal/methods/statefulset_status.go`:

```go
package methods

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkStatefulSetStatus struct{}

func (checkStatefulSetStatus) Name() string { return "check_statefulset_status" }
func (checkStatefulSetStatus) Description() string {
	return "StatefulSet replica counts and rollout state"
}

func (checkStatefulSetStatus) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "StatefulSet namespace"},
		{Name: "name", Required: true, Description: "StatefulSet name"},
	}
}

func (checkStatefulSetStatus) OutputFields() []OutputField {
	return []OutputField{
		{Name: "desiredReplicas", Type: FieldInt, Description: "spec.replicas (1 if unset)"},
		{Name: "readyReplicas", Type: FieldInt, Description: "status.readyReplicas"},
		{Name: "currentReplicas", Type: FieldInt, Description: "status.currentReplicas"},
		{Name: "updatedReplicas", Type: FieldInt, Description: "status.updatedReplicas"},
		{Name: "availableReplicas", Type: FieldInt, Description: "status.availableReplicas"},
		{Name: "updateRevisionPending", Type: FieldBool, Description: "currentRevision != updateRevision (rollout in flight)"},
	}
}

func (checkStatefulSetStatus) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	s, err := deps.Kube.AppsV1().StatefulSets(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get statefulset %s/%s: %w", params["namespace"], params["name"], err)
	}
	desired := int64(1)
	if s.Spec.Replicas != nil {
		desired = int64(*s.Spec.Replicas)
	}
	return Outputs{
		"desiredReplicas":       desired,
		"readyReplicas":         int64(s.Status.ReadyReplicas),
		"currentReplicas":       int64(s.Status.CurrentReplicas),
		"updatedReplicas":       int64(s.Status.UpdatedReplicas),
		"availableReplicas":     int64(s.Status.AvailableReplicas),
		"updateRevisionPending": s.Status.UpdateRevision != "" && s.Status.CurrentRevision != s.Status.UpdateRevision,
	}, nil
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkStatefulSetStatus{}) })
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/methods/ -run TestCheckStatefulSetStatus -v`
Expected: PASS (both happy + missing).

- [ ] **Step 5: Verify the suite**

Run: `go test ./internal/methods/ -count=1`
Expected: PASS.

---

### Task 8: New method `describe_statefulset`

**Files:**
- Create: `internal/methods/statefulset_describe.go`
- Test: `internal/methods/statefulset_describe_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/methods/statefulset_describe_test.go`:

```go
package methods

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDescribeStatefulSet(t *testing.T) {
	sc := "gp3"
	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "data"},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         "db-headless",
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}},
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type:          appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: i32(2)},
			},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "db", Image: "postgres:16"}},
			}},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "data"},
				Spec: corev1.PersistentVolumeClaimSpec{
					StorageClassName: &sc,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
					},
				},
			}},
		},
	}
	client := fake.NewSimpleClientset(ss)
	m, ok := Builtin().Get("describe_statefulset")
	if !ok {
		t.Fatal("describe_statefulset not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "data", "name": "db"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"serviceName":          "db-headless",
		"podManagementPolicy":  "Parallel",
		"partition":            int64(2),
		"volumeClaimTemplates": "data: 10Gi (gp3)",
		"selector":             "app=db",
		"updateStrategy":       "RollingUpdate",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}
```

> Note: in this client-go version `PersistentVolumeClaimSpec.Resources` is of type `corev1.VolumeResourceRequirements` (renamed from `ResourceRequirements`). Use it exactly as above.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/methods/ -run TestDescribeStatefulSet -v`
Expected: FAIL — `describe_statefulset not registered`.

- [ ] **Step 3: Write the implementation** — create `internal/methods/statefulset_describe.go`:

```go
package methods

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

type describeStatefulSet struct{}

func (describeStatefulSet) Name() string        { return "describe_statefulset" }
func (describeStatefulSet) Description() string { return "Sanitized statefulset manifest (spec+status)" }

func (describeStatefulSet) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "StatefulSet namespace"},
		{Name: "name", Required: true, Description: "StatefulSet name"},
	}
}

func (describeStatefulSet) OutputFields() []OutputField {
	return []OutputField{
		{Name: "containers", Type: FieldString, Description: "comma-separated container names (pod template)"},
		{Name: "images", Type: FieldString, Description: "comma-separated container images (pod template)"},
		{Name: "resourceRequests", Type: FieldString, Description: `per-container CPU/memory requests; "" if none set`},
		{Name: "resourceLimits", Type: FieldString, Description: `per-container CPU/memory limits; "" if none set`},
		{Name: "serviceAccount", Type: FieldString, Description: `pod template's service account, "" if default`},
		{Name: "selector", Type: FieldString, Description: "spec.selector matchLabels"},
		{Name: "serviceName", Type: FieldString, Description: "governing headless service (spec.serviceName)"},
		{Name: "updateStrategy", Type: FieldString, Description: "RollingUpdate|OnDelete"},
		{Name: "partition", Type: FieldInt, Description: "RollingUpdate partition (canary cutoff), -1 if unset"},
		{Name: "podManagementPolicy", Type: FieldString, Description: "OrderedReady|Parallel"},
		{Name: "volumeClaimTemplates", Type: FieldString, Description: `per template "name: size (storageClass)", "" if none`},
		{Name: "manifest", Type: FieldString, Description: "full YAML manifest; env values redacted, managedFields stripped"},
	}
}

func (describeStatefulSet) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	s, err := deps.Kube.AppsV1().StatefulSets(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get statefulset %s/%s: %w", params["namespace"], params["name"], err)
	}
	ss := s.DeepCopy()
	sanitizeObjectMeta(&ss.ObjectMeta)
	for i := range ss.Spec.Template.Spec.Containers {
		redactEnv(ss.Spec.Template.Spec.Containers[i].Env)
	}
	for i := range ss.Spec.Template.Spec.InitContainers {
		redactEnv(ss.Spec.Template.Spec.InitContainers[i].Env)
	}
	y, err := yaml.Marshal(ss)
	if err != nil {
		return nil, fmt.Errorf("marshal statefulset: %w", err)
	}
	tmpl := s.Spec.Template.Spec
	selector := ""
	if s.Spec.Selector != nil {
		selector = renderKVMap(s.Spec.Selector.MatchLabels)
	}
	strategy := string(s.Spec.UpdateStrategy.Type)
	if strategy == "" {
		strategy = "RollingUpdate"
	}
	partition := int64(-1)
	if ru := s.Spec.UpdateStrategy.RollingUpdate; ru != nil && ru.Partition != nil {
		partition = int64(*ru.Partition)
	}
	policy := string(s.Spec.PodManagementPolicy)
	if policy == "" {
		policy = "OrderedReady"
	}
	return Outputs{
		"containers":           containerNames(tmpl.Containers),
		"images":               containerImages(tmpl.Containers),
		"resourceRequests":     renderResourceList(tmpl.Containers, false),
		"resourceLimits":       renderResourceList(tmpl.Containers, true),
		"serviceAccount":       tmpl.ServiceAccountName,
		"selector":             selector,
		"serviceName":          s.Spec.ServiceName,
		"updateStrategy":       strategy,
		"partition":            partition,
		"podManagementPolicy":  policy,
		"volumeClaimTemplates": renderVolumeClaimTemplates(s.Spec.VolumeClaimTemplates),
		"manifest":             Truncate(string(y), defaultLogBytes),
	}, nil
}

// renderVolumeClaimTemplates renders "name: <size> (<storageClass>)" per PVC
// template, comma-joined; "" if none. Unset size/class render as "-".
func renderVolumeClaimTemplates(vcts []corev1.PersistentVolumeClaim) string {
	if len(vcts) == 0 {
		return ""
	}
	parts := make([]string, len(vcts))
	for i, v := range vcts {
		size := "-"
		if q, ok := v.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			size = q.String()
		}
		sc := "-"
		if v.Spec.StorageClassName != nil && *v.Spec.StorageClassName != "" {
			sc = *v.Spec.StorageClassName
		}
		parts[i] = fmt.Sprintf("%s: %s (%s)", v.Name, size, sc)
	}
	return strings.Join(parts, ", ")
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(describeStatefulSet{}) }) }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/methods/ -run TestDescribeStatefulSet -v`
Expected: PASS.

- [ ] **Step 5: Verify the suite**

Run: `go test ./internal/methods/ -count=1`
Expected: PASS.

---

### Task 9: New method `check_pvc`

**Files:**
- Create: `internal/methods/pvc.go`
- Test: `internal/methods/pvc_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/methods/pvc_test.go`:

```go
package methods

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckPVCBound(t *testing.T) {
	sc := "gp3"
	mode := corev1.PersistentVolumeFilesystem
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-db-0", Namespace: "data"},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &sc,
			VolumeName:       "pv-123",
			VolumeMode:       &mode,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
			},
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase:    corev1.ClaimBound,
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
		},
	}
	client := fake.NewSimpleClientset(pvc)
	m, ok := Builtin().Get("check_pvc")
	if !ok {
		t.Fatal("check_pvc not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "data", "name": "data-db-0"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"exists": true, "phase": "Bound", "storageClass": "gp3",
		"requestedStorage": "10Gi", "capacity": "10Gi", "volumeName": "pv-123",
		"accessModes": "ReadWriteOnce", "volumeMode": "Filesystem",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}

func TestCheckPVCMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_pvc")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "data", "name": "nope"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["exists"] != false || out["phase"] != "" || out["volumeName"] != "" {
		t.Errorf("missing PVC should default-empty, got %v", out)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/methods/ -run TestCheckPVC -v`
Expected: FAIL — `check_pvc not registered`.

- [ ] **Step 3: Write the implementation** — create `internal/methods/pvc.go`:

```go
package methods

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkPVC struct{}

func (checkPVC) Name() string        { return "check_pvc" }
func (checkPVC) Description() string { return "PersistentVolumeClaim binding status" }

func (checkPVC) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "PVC namespace"},
		{Name: "name", Required: true, Description: "PVC name"},
	}
}

func (checkPVC) OutputFields() []OutputField {
	return []OutputField{
		{Name: "exists", Type: FieldBool, Description: "PVC exists"},
		{Name: "phase", Type: FieldString, Description: `Pending|Bound|Lost, "" if not exists`},
		{Name: "storageClass", Type: FieldString, Description: `spec.storageClassName, "" if nil/default`},
		{Name: "requestedStorage", Type: FieldString, Description: "spec.resources.requests.storage"},
		{Name: "capacity", Type: FieldString, Description: `status.capacity.storage (actual), "" if unbound`},
		{Name: "volumeName", Type: FieldString, Description: `bound PV name, "" if unbound`},
		{Name: "accessModes", Type: FieldString, Description: "comma-separated access modes"},
		{Name: "volumeMode", Type: FieldString, Description: "Filesystem|Block"},
	}
}

func (checkPVC) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	out := Outputs{
		"exists": false, "phase": "", "storageClass": "", "requestedStorage": "",
		"capacity": "", "volumeName": "", "accessModes": "", "volumeMode": "",
	}
	pvc, err := deps.Kube.CoreV1().PersistentVolumeClaims(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pvc %s/%s: %w", params["namespace"], params["name"], err)
	}
	out["exists"] = true
	out["phase"] = string(pvc.Status.Phase)
	if pvc.Spec.StorageClassName != nil {
		out["storageClass"] = *pvc.Spec.StorageClassName
	}
	if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		out["requestedStorage"] = q.String()
	}
	if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		out["capacity"] = q.String()
	}
	out["volumeName"] = pvc.Spec.VolumeName
	out["accessModes"] = renderAccessModes(pvc.Spec.AccessModes)
	if pvc.Spec.VolumeMode != nil {
		out["volumeMode"] = string(*pvc.Spec.VolumeMode)
	}
	return out, nil
}

// renderAccessModes renders PVC access modes comma-joined; "" if none.
func renderAccessModes(modes []corev1.PersistentVolumeAccessMode) string {
	if len(modes) == 0 {
		return ""
	}
	parts := make([]string, len(modes))
	for i, m := range modes {
		parts[i] = string(m)
	}
	return strings.Join(parts, ",")
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkPVC{}) }) }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/methods/ -run TestCheckPVC -v`
Expected: PASS (bound + missing).

- [ ] **Step 5: Verify the suite**

Run: `go test ./internal/methods/ -count=1`
Expected: PASS.

---

### Task 10: New method `check_job`

**Files:**
- Create: `internal/methods/job.go`
- Test: `internal/methods/job_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/methods/job_test.go`:

```go
package methods

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckJobFailed(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "default"},
		Spec:       batchv1.JobSpec{Completions: i32(1), Parallelism: i32(1), BackoffLimit: i32(4)},
		Status: batchv1.JobStatus{
			Failed: 5,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"},
			},
		},
	}
	client := fake.NewSimpleClientset(job)
	m, ok := Builtin().Get("check_job")
	if !ok {
		t.Fatal("check_job not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "name": "migrate"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"exists": true, "failed": int64(5), "completions": int64(1),
		"parallelism": int64(1), "backoffLimit": int64(4),
		"failedCondition": true, "complete": false, "conditionReason": "BackoffLimitExceeded",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}

func TestCheckJobMissingDefaults(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_job")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "name": "nope"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["exists"] != false || out["completions"] != int64(-1) || out["backoffLimit"] != int64(6) {
		t.Errorf("missing job defaults wrong: %v", out)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/methods/ -run TestCheckJob -v`
Expected: FAIL — `check_job not registered`.

- [ ] **Step 3: Write the implementation** — create `internal/methods/job.go`:

```go
package methods

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkJob struct{}

func (checkJob) Name() string        { return "check_job" }
func (checkJob) Description() string { return "Job completion and failure status" }

func (checkJob) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Job namespace"},
		{Name: "name", Required: true, Description: "Job name"},
	}
}

func (checkJob) OutputFields() []OutputField {
	return []OutputField{
		{Name: "exists", Type: FieldBool, Description: "Job exists"},
		{Name: "active", Type: FieldInt, Description: "status.active"},
		{Name: "succeeded", Type: FieldInt, Description: "status.succeeded"},
		{Name: "failed", Type: FieldInt, Description: "status.failed"},
		{Name: "completions", Type: FieldInt, Description: "spec.completions, -1 if unset"},
		{Name: "parallelism", Type: FieldInt, Description: "spec.parallelism, 1 if unset"},
		{Name: "backoffLimit", Type: FieldInt, Description: "spec.backoffLimit, 6 if unset (k8s default)"},
		{Name: "complete", Type: FieldBool, Description: "Complete condition is True"},
		{Name: "failedCondition", Type: FieldBool, Description: "Failed condition is True"},
		{Name: "conditionReason", Type: FieldString, Description: `e.g. BackoffLimitExceeded, DeadlineExceeded, "" if none`},
	}
}

func (checkJob) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	out := Outputs{
		"exists": false, "active": int64(0), "succeeded": int64(0), "failed": int64(0),
		"completions": int64(-1), "parallelism": int64(1), "backoffLimit": int64(6),
		"complete": false, "failedCondition": false, "conditionReason": "",
	}
	job, err := deps.Kube.BatchV1().Jobs(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get job %s/%s: %w", params["namespace"], params["name"], err)
	}
	out["exists"] = true
	out["active"] = int64(job.Status.Active)
	out["succeeded"] = int64(job.Status.Succeeded)
	out["failed"] = int64(job.Status.Failed)
	if job.Spec.Completions != nil {
		out["completions"] = int64(*job.Spec.Completions)
	}
	if job.Spec.Parallelism != nil {
		out["parallelism"] = int64(*job.Spec.Parallelism)
	}
	if job.Spec.BackoffLimit != nil {
		out["backoffLimit"] = int64(*job.Spec.BackoffLimit)
	}
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			out["complete"] = true
		case batchv1.JobFailed:
			out["failedCondition"] = true
			out["conditionReason"] = c.Reason
		}
	}
	return out, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkJob{}) }) }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/methods/ -run TestCheckJob -v`
Expected: PASS.

- [ ] **Step 5: Verify the suite**

Run: `go test ./internal/methods/ -count=1`
Expected: PASS.

---

### Task 11: New method `check_cronjob`

**Files:**
- Create: `internal/methods/cronjob.go`
- Test: `internal/methods/cronjob_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/methods/cronjob_test.go`:

```go
package methods

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckCronJob(t *testing.T) {
	suspend := false
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default"},
		Spec: batchv1.CronJobSpec{
			Schedule:          "0 2 * * *",
			Suspend:           &suspend,
			ConcurrencyPolicy: batchv1.ForbidConcurrent,
		},
		Status: batchv1.CronJobStatus{Active: []corev1.ObjectReference{{Name: "backup-123"}}},
	}
	client := fake.NewSimpleClientset(cj)
	m, ok := Builtin().Get("check_cronjob")
	if !ok {
		t.Fatal("check_cronjob not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "name": "backup"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"exists": true, "schedule": "0 2 * * *", "suspended": false,
		"activeJobs": int64(1), "concurrencyPolicy": "Forbid", "lastScheduleTime": "",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}

func TestCheckCronJobMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_cronjob")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "name": "nope"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["exists"] != false || out["schedule"] != "" {
		t.Errorf("missing cronjob defaults wrong: %v", out)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/methods/ -run TestCheckCronJob -v`
Expected: FAIL — `check_cronjob not registered`.

- [ ] **Step 3: Write the implementation** — create `internal/methods/cronjob.go`:

```go
package methods

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkCronJob struct{}

func (checkCronJob) Name() string        { return "check_cronjob" }
func (checkCronJob) Description() string { return "CronJob schedule and recent run status" }

func (checkCronJob) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "CronJob namespace"},
		{Name: "name", Required: true, Description: "CronJob name"},
	}
}

func (checkCronJob) OutputFields() []OutputField {
	return []OutputField{
		{Name: "exists", Type: FieldBool, Description: "CronJob exists"},
		{Name: "schedule", Type: FieldString, Description: "spec.schedule (cron expression)"},
		{Name: "suspended", Type: FieldBool, Description: "spec.suspend"},
		{Name: "activeJobs", Type: FieldInt, Description: "number of currently active jobs"},
		{Name: "lastScheduleTime", Type: FieldString, Description: `RFC3339, "" if never scheduled`},
		{Name: "lastSuccessfulTime", Type: FieldString, Description: `RFC3339, "" if never succeeded`},
		{Name: "concurrencyPolicy", Type: FieldString, Description: "Allow|Forbid|Replace"},
	}
}

func (checkCronJob) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	out := Outputs{
		"exists": false, "schedule": "", "suspended": false, "activeJobs": int64(0),
		"lastScheduleTime": "", "lastSuccessfulTime": "", "concurrencyPolicy": "",
	}
	cj, err := deps.Kube.BatchV1().CronJobs(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get cronjob %s/%s: %w", params["namespace"], params["name"], err)
	}
	out["exists"] = true
	out["schedule"] = cj.Spec.Schedule
	if cj.Spec.Suspend != nil {
		out["suspended"] = *cj.Spec.Suspend
	}
	out["activeJobs"] = int64(len(cj.Status.Active))
	if cj.Status.LastScheduleTime != nil {
		out["lastScheduleTime"] = cj.Status.LastScheduleTime.Time.UTC().Format(time.RFC3339)
	}
	if cj.Status.LastSuccessfulTime != nil {
		out["lastSuccessfulTime"] = cj.Status.LastSuccessfulTime.Time.UTC().Format(time.RFC3339)
	}
	out["concurrencyPolicy"] = string(cj.Spec.ConcurrencyPolicy)
	return out, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkCronJob{}) }) }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/methods/ -run TestCheckCronJob -v`
Expected: PASS.

- [ ] **Step 5: Verify the whole package**

Run: `go test ./internal/methods/ -count=1`
Expected: PASS — all method tests green. Then `go build ./...` to confirm the binary compiles.

---

### Task 12: RBAC — grant reads for PVC + batch

**Files:**
- Modify: `charts/kato/templates/rbac.yaml`

- [ ] **Step 1: Add `persistentvolumeclaims` to the core rule.** In `charts/kato/templates/rbac.yaml`, change the first rule's resources list:

Replace:
```yaml
  - apiGroups: [""]
    resources: [pods, pods/log, nodes, services, events, endpoints, configmaps]
    verbs: [get, list, watch]
```
With:
```yaml
  - apiGroups: [""]
    resources: [pods, pods/log, nodes, services, events, endpoints, configmaps, persistentvolumeclaims]
    verbs: [get, list, watch]
```

- [ ] **Step 2: Add the `batch` rule.** Immediately after the `apps` rule block (the one with `resources: [deployments, replicasets, daemonsets, statefulsets]`), insert:

```yaml
  - apiGroups: ["batch"]
    resources: [jobs, cronjobs]
    verbs: [get, list, watch]
```

- [ ] **Step 3: Verify the chart still renders.**

Run: `helm template charts/kato | grep -A2 'apiGroups: \["batch"\]'`
Expected: shows the new batch rule with `jobs, cronjobs`. Also confirm `persistentvolumeclaims` appears:
Run: `helm template charts/kato | grep persistentvolumeclaims`
Expected: one match in the ClusterRole rules.

---

### Task 13: Documentation — METHOD.md + README table

**Files:**
- Modify: `docs/METHOD.md`
- Modify: `charts/kato/README.md.gotmpl`
- Regenerate: `README.md` (via `make readme`)

- [ ] **Step 1: Update the `docs/METHOD.md` Index table.** Add these rows (place the workload rows near the existing daemonset/statefulset-adjacent entries, and add Storage/Batch rows after `check_configmap`):

```markdown
| [`describe_daemonset`](#describe_daemonset) | Sanitized daemonset manifest + structured fields (update strategy, node targeting) |
| [`check_statefulset_status`](#check_statefulset_status) | StatefulSet replica counts and rollout state |
| [`describe_statefulset`](#describe_statefulset) | Sanitized statefulset manifest + structured fields (serviceName, partition, volumeClaimTemplates) |
| [`check_pvc`](#check_pvc) | PersistentVolumeClaim binding status (phase, capacity, bound PV) |
| [`check_job`](#check_job) | Job completion/failure counts and conditions |
| [`check_cronjob`](#check_cronjob) | CronJob schedule, suspension, and recent-run times |
```

- [ ] **Step 2: Add `docs/METHOD.md` body sections** for the 6 new methods, each mirroring the existing format (a `### \`name\`` heading, a one-line description, an **Inputs** table, and an **Outputs** table). Use these exact field sets:

`describe_daemonset` (under `## Workloads`) — inputs `namespace` (yes), `name` (yes); outputs: `containers`, `images`, `resourceRequests`, `resourceLimits`, `serviceAccount`, `selector`, `updateStrategy` (RollingUpdate|OnDelete), `maxUnavailable`, `nodeSelector`, `tolerations`, `probes`, `manifest`.

`check_statefulset_status` (under `## Workloads`) — inputs `namespace` (yes), `name` (yes); outputs: `desiredReplicas`(int), `readyReplicas`(int), `currentReplicas`(int), `updatedReplicas`(int), `availableReplicas`(int), `updateRevisionPending`(bool).

`describe_statefulset` (under `## Workloads`) — inputs `namespace` (yes), `name` (yes); outputs: `containers`, `images`, `resourceRequests`, `resourceLimits`, `serviceAccount`, `selector`, `serviceName`, `updateStrategy`, `partition`(int, -1 unset), `podManagementPolicy`, `volumeClaimTemplates`, `manifest`.

`check_pvc` (new `## Storage` section) — inputs `namespace` (yes), `name` (yes); outputs: `exists`(bool), `phase`, `storageClass`, `requestedStorage`, `capacity`, `volumeName`, `accessModes`, `volumeMode`. Note: a missing PVC reports `exists: false`, not an error.

`check_job` (new `## Batch` section) — inputs `namespace` (yes), `name` (yes); outputs: `exists`(bool), `active`(int), `succeeded`(int), `failed`(int), `completions`(int, -1 unset), `parallelism`(int), `backoffLimit`(int, 6 default), `complete`(bool), `failedCondition`(bool), `conditionReason`. Missing Job reports `exists: false`.

`check_cronjob` (under `## Batch`) — inputs `namespace` (yes), `name` (yes); outputs: `exists`(bool), `schedule`, `suspended`(bool), `activeJobs`(int), `lastScheduleTime`, `lastSuccessfulTime`, `concurrencyPolicy`. Missing CronJob reports `exists: false`.

- [ ] **Step 3: Add the new fields to the existing describe sections** in `docs/METHOD.md`:
  - `describe_pod` Outputs: add `nodeName`, `conditions`, `probes`, `ownerReferences`, `nodeSelector`, `tolerations`, `priorityClassName`, `hostNetwork`(bool).
  - `describe_deployment` Outputs: add `replicas`(int), `selector`, `maxSurge`, `maxUnavailable`, `minReadySeconds`(int), `revisionHistoryLimit`(int), `paused`(bool), `probes`, `nodeSelector`, `tolerations`.
  - `describe_node` Outputs: add `kubeletVersion`, `osImage`, `kernelVersion`, `containerRuntime`, `capacityPods`, `unschedulable`(bool), `conditions`.
  - `describe_service` Outputs: replace the single `manifest` row with `type`, `clusterIP`, `selector`, `ports`, `externalName`, `loadBalancerIngress`, `manifest`.

- [ ] **Step 4: Update the README methods table** in `charts/kato/README.md.gotmpl`. Replace the existing `## Built-in methods` intro line and table with:

```markdown
kato ships **25 read-only checks** you compose into flows — pods, workloads,
storage, batch, nodes, networking, and config:

| Area | Methods |
|---|---|
| Pods | `check_pod_status`, `check_pod_logs`, `describe_pod`, `check_pod_resources`, `check_pod_usage` |
| Workloads | `check_deployment_status`, `describe_deployment`, `check_replicaset`, `check_daemonset_status`, `describe_daemonset`, `check_statefulset_status`, `describe_statefulset`, `check_hpa` |
| Storage | `check_pvc` |
| Batch | `check_job`, `check_cronjob` |
| Listing / fan-out | `list_pods`, `list_failing_pods` (drive `forEach` across a workload's pods) |
| Nodes | `check_node_status`, `describe_node` |
| Networking | `check_service_endpoints`, `describe_service`, `check_ingress` |
| Config & events | `check_configmap`, `check_events` |
```

- [ ] **Step 5: Regenerate README and verify counts.**

Run: `make readme`
Then: `grep -c 'check_\|describe_\|list_' README.md` (sanity) and confirm the rendered `README.md` shows "**25 read-only checks**" and the Storage/Batch rows.
Expected: `make readme` exits 0; README contains the new table.

- [ ] **Step 6: Final whole-repo verification.**

Run: `go build ./... && go test ./... -count=1`
Expected: build succeeds; all tests pass (the new methods auto-appear in `GET /api/v1/methods` via `OutputFields()` — no API code changed).

---

## Self-Review

**Spec coverage:**
- describe_pod / describe_deployment / describe_node / describe_service enrichments → Tasks 2–5 ✓
- describe_daemonset → Task 6 ✓
- check_statefulset_status + describe_statefulset → Tasks 7–8 ✓
- check_pvc → Task 9 ✓; check_job → Task 10 ✓; check_cronjob → Task 11 ✓
- Shared render helpers → Task 1 ✓
- RBAC (PVC + batch) → Task 12 ✓
- METHOD.md + README table → Task 13 ✓
- `exists`-vs-error convention: status method (statefulset) errors on missing (Task 7 test asserts error); lookup methods (pvc/job/cronjob) return `exists:false` (Tasks 9–11 assert no error) ✓
- `labels` field: intentionally absent from all enrichments ✓

**Type consistency:** helper names (`renderKVMap`, `renderTolerations`, `renderOwnerRefs`, `renderPodConditions`, `renderNodeConditions`, `renderProbes`, `renderPorts`) are defined in Task 1 and used identically in Tasks 2–8. `renderVolumeClaimTemplates` (Task 8) and `renderAccessModes` (Task 9) are defined and used within their own files. `i32` is reused from `hpa_test.go` (never redefined). Int outputs are `int64`; bools are `bool`; everywhere consistent.

**Placeholder scan:** every code step contains complete code; every run step has an exact command and expected result. No TBD/TODO/"similar to".

**Known version note:** `PersistentVolumeClaimSpec.Resources` is `corev1.VolumeResourceRequirements` in this client-go version (called out in Task 8). `StatefulSetStatus.AvailableReplicas` exists in the apps/v1 type. If `go build` flags either, that is a client-go version surprise to resolve before proceeding.
