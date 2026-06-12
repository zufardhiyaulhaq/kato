# kato Methods

Methods are the built-in, **read-only** Kubernetes checks that a `UseCase` step
invokes. Each method declares typed **inputs** (params) and a flat set of typed
**outputs**. Outputs are simultaneously what `when` conditions match, what
`$(steps.<name>.<field>)` references read, what the LLM receives (after each
step's `summaryFilter`), and what the `Run` record stores — one structure,
identical everywhere.

This document is generated from the method definitions in
`internal/methods/`. `GET /api/v1/methods` returns the same information at
runtime.

## Conventions

- **Output types** are `string`, `int`, or `bool`. Every output is
  guaranteed present with a defined default (e.g. `""`, `0`, `false`, `-1`) —
  a `when` condition never breaks on missing data.
- **Required** inputs must be supplied; **optional** inputs may be omitted.
- All inputs and outputs are strings on the wire; numeric/boolean inputs (e.g.
  `previous`, `tailLines`) are passed as strings and parsed by the method.
- Large string outputs (e.g. `logs`, `manifest`) are truncated head+tail when
  they exceed the size cap.
- Some methods declare a **list output** in addition to scalar outputs. A list
  output is a named collection of typed-field records (e.g. `pods`). Lists are
  consumable only by a `forEach` step (`forEach: $(steps.<step>.<list>)`); they
  cannot be used in `when` conditions or `$(steps.x.y)` scalar references.
- `describe_*` methods sanitize manifests: `managedFields` and the
  `last-applied-configuration` annotation are stripped, and container env var
  **values** are redacted (`[REDACTED]`). Other annotations are preserved — set
  a step `summaryFilter` if you need to keep them away from a hosted LLM.

## Index

| Method | Purpose |
|---|---|
| [`check_pod_status`](#check_pod_status) | Pod phase, readiness, restarts, last termination |
| [`check_pod_logs`](#check_pod_logs) | Container logs (optionally previous instance) |
| [`describe_pod`](#describe_pod) | Sanitized pod manifest + structured fields |
| [`check_pod_resources`](#check_pod_resources) | Configured CPU/memory requests and limits (from spec) |
| [`check_pod_usage`](#check_pod_usage) | Live CPU/memory usage (from metrics-server) |
| [`check_node_status`](#check_node_status) | Node readiness and pressure conditions |
| [`describe_node`](#describe_node) | Node capacity, allocatable, taints, manifest |
| [`check_events`](#check_events) | Kubernetes events for an object or namespace, warnings first |
| [`check_deployment_status`](#check_deployment_status) | Deployment replica counts and rollout conditions |
| [`describe_deployment`](#describe_deployment) | Sanitized deployment manifest + structured fields |
| [`check_replicaset`](#check_replicaset) | State of the ReplicaSets owned by a deployment |
| [`check_daemonset_status`](#check_daemonset_status) | DaemonSet scheduling and readiness counts |
| [`check_service_endpoints`](#check_service_endpoints) | Does the service selector match ready endpoints? |
| [`describe_service`](#describe_service) | Sanitized service manifest |
| [`check_ingress`](#check_ingress) | Ingress rules, backend service existence, LB status |
| [`list_failing_pods`](#list_failing_pods) | Failing pods of a workload (Deployment/DaemonSet/StatefulSet), worst-first — produces list output `pods` |

---

## Pod

### `check_pod_status`

Pod phase, readiness, restarts, last termination.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Pod namespace |
| `name` | yes | Pod name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `phase` | string | `Pending\|Running\|Succeeded\|Failed\|Unknown` |
| `ready` | bool | Ready condition is True |
| `restartCount` | int | max restartCount across containers, 0 if none |
| `nodeName` | string | scheduled node, `""` if unscheduled |
| `waitingReason` | string | e.g. `CrashLoopBackOff`, `""` if none |
| `waitingMessage` | string | waiting message, `""` if none |
| `lastTerminationReason` | string | e.g. `OOMKilled`, `""` if none |
| `lastTerminationExitCode` | int | `-1` if no prior termination |
| `qosClass` | string | `Guaranteed\|Burstable\|BestEffort` |

---

### `check_pod_logs`

Container logs (optionally previous instance).

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Pod namespace |
| `name` | yes | Pod name |
| `container` | no | container name; empty = first container |
| `previous` | no | `"true"` to fetch the previous instance's logs |
| `tailLines` | no | max lines from the end (integer); **defaults to 10** |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `logs` | string | log text, truncated head+tail if large |

---

### `describe_pod`

Sanitized pod manifest (spec+status), with structured fields broken out.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Pod namespace |
| `name` | yes | Pod name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `containers` | string | comma-separated container names |
| `images` | string | comma-separated container images |
| `resourceRequests` | string | per-container CPU/memory requests, e.g. `"app: cpu=100m mem=128Mi"`; `""` if none set |
| `resourceLimits` | string | per-container CPU/memory limits; `""` if none set |
| `restartPolicy` | string | `Always\|OnFailure\|Never` |
| `serviceAccount` | string | pod's service account, `""` if default |
| `volumes` | string | comma-separated volume names, `""` if none |
| `manifest` | string | full YAML manifest; env values redacted, managedFields stripped |

---

### `check_pod_resources`

Configured CPU/memory requests and limits from the pod spec (summed across
containers). Always available — no metrics-server required. Pair with
`check_pod_usage` to compare reserved vs. consumed.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Pod namespace |
| `name` | yes | Pod name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `cpuRequest` | string | summed CPU request, e.g. `"250m"`; `"0"` if none set |
| `cpuLimit` | string | summed CPU limit; `"0"` if none set |
| `memoryRequest` | string | summed memory request, e.g. `"256Mi"`; `"0"` if none set |
| `memoryLimit` | string | summed memory limit; `"0"` if none set |
| `noLimitsSet` | bool | true if no container sets any CPU or memory limit |

---

### `check_pod_usage`

Live pod CPU/memory usage from metrics-server (`metrics.k8s.io`), summed across
containers. Requires metrics-server in the cluster; when it is absent or has no
data yet, the step still succeeds with `metricsAvailable: false` and zero values
rather than failing.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Pod namespace |
| `name` | yes | Pod name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `cpuMillicores` | int | current CPU usage in millicores, 0 if unavailable |
| `memoryBytes` | int | current memory usage in bytes, 0 if unavailable |
| `memoryHuman` | string | current memory usage, e.g. `"142Mi"`; `"0"` if unavailable |
| `metricsAvailable` | bool | false if metrics-server is absent or has no data for this pod yet |

---

## Node

### `check_node_status`

Node readiness and pressure conditions.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `name` | yes | Node name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `ready` | bool | Ready condition is True |
| `readyReason` | string | Ready condition reason, `""` if ready |
| `memoryPressure` | bool | MemoryPressure condition is True |
| `diskPressure` | bool | DiskPressure condition is True |
| `pidPressure` | bool | PIDPressure condition is True |
| `unschedulable` | bool | `spec.unschedulable` (cordoned) |

---

### `describe_node`

Node capacity, allocatable, taints, manifest.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `name` | yes | Node name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `taints` | string | rendered `"key=value:Effect"` list, `""` if none |
| `allocatableCPU` | string | allocatable CPU quantity |
| `allocatableMemory` | string | allocatable memory quantity |
| `manifest` | string | sanitized YAML manifest |

---

## Events

### `check_events`

Kubernetes events for an object or namespace, warnings first.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Namespace to read events from |
| `involvedObject` | no | filter to events about this object name; empty = whole namespace |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `events` | string | rendered event lines, warnings first |
| `count` | int | number of events matched |
| `warningCount` | int | number of Warning events matched |

---

## Workloads

### `check_deployment_status`

Deployment replica counts and rollout conditions.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Deployment namespace |
| `name` | yes | Deployment name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `desiredReplicas` | int | `spec.replicas` |
| `readyReplicas` | int | `status.readyReplicas` |
| `updatedReplicas` | int | `status.updatedReplicas` |
| `available` | bool | Available condition is True |
| `progressing` | bool | Progressing condition is True |
| `progressingReason` | string | e.g. `ProgressDeadlineExceeded`, `""` if progressing |

---

### `describe_deployment`

Sanitized deployment manifest (spec+status), with structured fields broken out
from the pod template.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Deployment namespace |
| `name` | yes | Deployment name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `containers` | string | comma-separated container names (pod template) |
| `images` | string | comma-separated container images (pod template) |
| `resourceRequests` | string | per-container CPU/memory requests, e.g. `"app: cpu=100m mem=128Mi"`; `""` if none set |
| `resourceLimits` | string | per-container CPU/memory limits; `""` if none set |
| `strategy` | string | `RollingUpdate\|Recreate` |
| `serviceAccount` | string | pod template's service account, `""` if default |
| `manifest` | string | full YAML manifest; env values redacted, managedFields stripped |

---

### `check_replicaset`

State of the ReplicaSets owned by a deployment.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Namespace |
| `deployment` | yes | Owning deployment name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `replicaFailure` | bool | any owned RS has `ReplicaFailure=True` |
| `failureReason` | string | e.g. `FailedCreate`, `""` if none |
| `failureMessage` | string | failure message, `""` if none |
| `activeReplicaSets` | int | owned RS with desired replicas > 0 |

---

### `check_daemonset_status`

DaemonSet scheduling and readiness counts.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | DaemonSet namespace |
| `name` | yes | DaemonSet name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `desiredScheduled` | int | nodes that should run the pod (`status.desiredNumberScheduled`) |
| `currentScheduled` | int | nodes running at least one pod (`status.currentNumberScheduled`) |
| `ready` | int | pods ready (`status.numberReady`) |
| `available` | int | pods available (`status.numberAvailable`) |
| `misscheduled` | int | pods running where they should not be (`status.numberMisscheduled`) |
| `updatedScheduled` | int | pods on the updated template (`status.updatedNumberScheduled`) |

---

## Networking

### `check_service_endpoints`

Does the service selector match ready endpoints?

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Service namespace |
| `name` | yes | Service name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `hasSelector` | bool | service has a pod selector |
| `readyEndpoints` | int | endpoints in Ready condition |
| `notReadyEndpoints` | int | endpoints not Ready |

---

### `describe_service`

Sanitized service manifest.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Service namespace |
| `name` | yes | Service name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `manifest` | string | YAML manifest, managedFields stripped |

---

### `check_ingress`

Ingress rules, backend service existence, LB status.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Ingress namespace |
| `name` | yes | Ingress name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `rules` | string | rendered host/path -> backend lines |
| `missingBackends` | string | comma-separated backend services that don't exist, `""` if all exist |
| `loadBalancerReady` | bool | `status.loadBalancer` has an ingress IP/hostname |

---

## Discovery

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
