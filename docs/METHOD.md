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
| [`check_pod_status`](#check_pod_status) | Pod phase, readiness, restarts, last termination — also produces list output `containers` |
| [`check_pod_logs`](#check_pod_logs) | Container logs (optionally previous instance) |
| [`describe_pod`](#describe_pod) | Sanitized pod manifest + structured fields — also produces list output `containerList` |
| [`check_pod_resources`](#check_pod_resources) | Configured CPU/memory requests and limits (from spec) |
| [`check_pod_usage`](#check_pod_usage) | Live CPU/memory usage (from metrics-server) |
| [`check_node_status`](#check_node_status) | Node readiness and pressure conditions |
| [`describe_node`](#describe_node) | Node capacity, allocatable, taints, manifest |
| [`check_events`](#check_events) | Kubernetes events for an object or namespace, warnings first |
| [`check_deployment_status`](#check_deployment_status) | Deployment replica counts and rollout conditions |
| [`describe_deployment`](#describe_deployment) | Sanitized deployment manifest + structured fields |
| [`check_replicaset`](#check_replicaset) | State of the ReplicaSets owned by a deployment |
| [`check_daemonset_status`](#check_daemonset_status) | DaemonSet scheduling and readiness counts |
| [`describe_daemonset`](#describe_daemonset) | Sanitized daemonset manifest + structured fields (update strategy, node targeting) |
| [`check_statefulset_status`](#check_statefulset_status) | StatefulSet replica counts and rollout state |
| [`describe_statefulset`](#describe_statefulset) | Sanitized statefulset manifest + structured fields (serviceName, partition, volumeClaimTemplates) |
| [`check_hpa`](#check_hpa) | HPA replica bounds, current scale, metrics, and scaling conditions |
| [`check_service_endpoints`](#check_service_endpoints) | Does the service selector match ready endpoints? |
| [`describe_service`](#describe_service) | Sanitized service manifest |
| [`check_ingress`](#check_ingress) | Ingress rules, backend service existence, LB status |
| [`check_configmap`](#check_configmap) | ConfigMap existence, keys, and rendered data |
| [`check_pvc`](#check_pvc) | PersistentVolumeClaim binding status (phase, capacity, bound PV) |
| [`check_job`](#check_job) | Job completion/failure counts and conditions |
| [`check_cronjob`](#check_cronjob) | CronJob schedule, suspension, and recent-run times |
| [`check_apiserver`](#check_apiserver) | Health of the connected API server (`/livez` or `/healthz`) with failing-check names |
| [`list_failing_pods`](#list_failing_pods) | Failing pods of a workload (Deployment/DaemonSet/StatefulSet), worst-first — produces list output `pods` |
| [`list_pods`](#list_pods) | All pods of a workload (Deployment/DaemonSet/StatefulSet), not-ready first — produces list output `pods` |
| [`list_nodes`](#list_nodes) | Fleet node health bucketed by status (counts) + a worst-first list of only the not-fully-healthy nodes — produces list output `nodes` |
| [`list_node_pods`](#list_node_pods) | Pods scheduled on a node, optionally filtered by a name regex (e.g. `coredns\|terway`), worst-first — produces list output `pods` |
| [`probe_tcp`](#probe_tcp) | Active TCP connect check — does `target:port` accept a connection? |
| [`probe_http`](#probe_http) | Active HTTP(S) GET with status and optional body assertions |
| [`probe_dns`](#probe_dns) | Active DNS resolution check — does a name resolve to an address? |
| [`probe_traceroute`](#probe_traceroute) | Active ICMP traceroute — is the destination reachable, how many hops away, and where does the path stop? — also produces list output `hops` |
| [`probe_grpc`](#probe_grpc) | Active gRPC health check — is `target:port` SERVING (`grpc.health.v1.Health/Check`)? |

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

**List output `containers`** (one entry per container; `forEach` source for per-container checks)

| Item field | Type | Description |
|---|---|---|
| `name` | string | container name |
| `restartCount` | int | container restart count |
| `ready` | bool | container Ready |

Fan out per-container checks with `forEach: $(steps.<step>.containers)`, binding
`$(item.name)` in the step's `with`.

---

### `check_pod_logs`

Container logs (optionally previous instance). When `container` is omitted on a
multi-container pod, every container (regular + init) is fetched and rendered as
labeled `=== container: <name> ===` blocks; a per-container fetch failure (e.g. a
sidecar with no previous instance) becomes an inline `(no logs: …)` note rather
than failing the step. A single-container pod produces unlabeled logs as before.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Pod namespace |
| `name` | yes | Pod name |
| `container` | no | container name; empty = all containers (regular + init) |
| `previous` | no | `"true"` to fetch the previous instance's logs |
| `tailLines` | no | max lines from the end (integer); **defaults to 10** |
| `maxLineLength` | no | max characters per line; longer lines are trimmed with a `…[+N chars]` marker (default `1000`; `0` = unlimited) |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `logs` | string | log text; each line trimmed to `maxLineLength`, whole blob truncated head+tail if large |

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
| `nodeName` | string | scheduled node, `""` if unscheduled |
| `conditions` | string | PodScheduled/Initialized/ContainersReady/Ready as `Type=Status (Reason)` |
| `probes` | string | per-container liveness/readiness/startup probe summary |
| `ownerReferences` | string | controllers owning the pod, e.g. `"ReplicaSet/api-abc"` |
| `nodeSelector` | string | pod nodeSelector, `""` if none |
| `tolerations` | string | pod tolerations, `""` if none |
| `priorityClassName` | string | priority class, `""` if none |
| `hostNetwork` | bool | pod uses host network |

**List output `containerList`** (one entry per container; `forEach` source — the scalar `containers` is the comma-joined form)

| Item field | Type | Description |
|---|---|---|
| `name` | string | container name |
| `image` | string | container image |

Fan out per-container checks with `forEach: $(steps.<step>.containerList)`, binding
`$(item.name)` / `$(item.image)` in the step's `with`.

---

### `check_pod_resources`

Configured CPU/memory requests and limits **per container** (init containers
marked). Always available — no metrics-server required. Pair with
`check_pod_usage` to compare reserved vs. consumed. Reported per container rather
than summed: in a multi-container pod (e.g. terway-eniip) a summed view is
meaningless when containers set limits unevenly.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Pod namespace |
| `name` | yes | Pod name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `containers` | string | one line per container: `<name>[ (init)]: req cpu=<v> mem=<v>; lim cpu=<v> mem=<v>`, with an unset value shown as `-` |
| `noLimitsSet` | bool | true if no (non-init) container sets any CPU or memory limit |

Example `containers`:

```
terway-init (init): req cpu=10m mem=-; lim cpu=- mem=-
terway: req cpu=20m mem=320Mi; lim cpu=1100m mem=256Mi
policy: req cpu=10m mem=-; lim cpu=- mem=-
```

---

### `check_pod_usage`

Live **per-container** CPU/memory usage from metrics-server (`metrics.k8s.io`).
Requires metrics-server in the cluster; when it is absent or has no data yet, the
step still succeeds with `metricsAvailable: false` and empty `containers` rather
than failing.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Pod namespace |
| `name` | yes | Pod name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `containers` | string | one line per container: `<name>: cpu=<m>m mem=<n>Mi`, sorted by name; empty if unavailable |
| `metricsAvailable` | bool | false if metrics-server is absent or has no data for this pod yet |

Pair with `check_pod_resources` to compare each container's live usage against its
configured limit (e.g. `terway` using `cpu=83m mem=259Mi` against a `mem=256Mi`
limit → over its memory limit, imminent OOM).

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
| `kubeletVersion` | string | `status.nodeInfo.kubeletVersion` |
| `osImage` | string | `status.nodeInfo.osImage` |
| `kernelVersion` | string | `status.nodeInfo.kernelVersion` |
| `containerRuntime` | string | `status.nodeInfo.containerRuntimeVersion` |
| `capacityPods` | string | `status.capacity.pods` (scheduling ceiling) |
| `unschedulable` | bool | `spec.unschedulable` (cordoned) |
| `conditions` | string | node conditions as `Type=Status (Reason)` |

---

## Events

### `check_events`

Kubernetes events for an object or namespace, warnings first.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Namespace to read events from |
| `involvedObject` | no | filter to events about this object name; empty = whole namespace |
| `limit` | no | max event lines to render, warnings first (default `"20"`; `"0"` = no limit) |
| `maxLineLength` | no | max characters per rendered line; longer lines are trimmed with a `…[+N chars]` marker (default `1000`; `0` = unlimited) |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `events` | string | rendered event lines, warnings first, capped at `limit` (a `[... N more events not shown ...]` marker is appended when truncated); each line trimmed to `maxLineLength` |
| `count` | int | number of events matched (the full count, before `limit`) |
| `warningCount` | int | number of Warning events matched (full count) |

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
| `replicas` | int | `spec.replicas` (1 if unset) |
| `selector` | string | `spec.selector` matchLabels |
| `maxSurge` | string | RollingUpdate maxSurge, `""` for Recreate |
| `maxUnavailable` | string | RollingUpdate maxUnavailable, `""` for Recreate |
| `minReadySeconds` | int | `spec.minReadySeconds` |
| `revisionHistoryLimit` | int | `spec.revisionHistoryLimit`, `-1` if unset |
| `paused` | bool | `spec.paused` |
| `probes` | string | per-container probe summary (pod template) |
| `nodeSelector` | string | pod template nodeSelector |
| `tolerations` | string | pod template tolerations |

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

### `describe_daemonset`

Sanitized daemonset manifest (spec+status), with structured fields broken out from the pod template.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | DaemonSet namespace |
| `name` | yes | DaemonSet name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `containers` | string | comma-separated container names (pod template) |
| `images` | string | comma-separated container images (pod template) |
| `resourceRequests` | string | per-container CPU/memory requests, e.g. `"app: cpu=100m mem=128Mi"`; `""` if none set |
| `resourceLimits` | string | per-container CPU/memory limits; `""` if none set |
| `serviceAccount` | string | pod template's service account, `""` if default |
| `selector` | string | `spec.selector` matchLabels |
| `updateStrategy` | string | `RollingUpdate\|OnDelete` |
| `maxUnavailable` | string | RollingUpdate maxUnavailable, `""` for OnDelete |
| `nodeSelector` | string | pod template nodeSelector (which nodes the DS targets) |
| `tolerations` | string | pod template tolerations |
| `probes` | string | per-container probe summary |
| `manifest` | string | full YAML manifest; env values redacted, managedFields stripped |

---

### `check_statefulset_status`

StatefulSet replica counts and rollout state.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | StatefulSet namespace |
| `name` | yes | StatefulSet name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `desiredReplicas` | int | `spec.replicas` (1 if unset) |
| `readyReplicas` | int | `status.readyReplicas` |
| `currentReplicas` | int | `status.currentReplicas` |
| `updatedReplicas` | int | `status.updatedReplicas` |
| `availableReplicas` | int | `status.availableReplicas` |
| `updateRevisionPending` | bool | `currentRevision != updateRevision` (rollout in flight) |

---

### `describe_statefulset`

Sanitized statefulset manifest (spec+status), with structured fields broken out from the pod template.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | StatefulSet namespace |
| `name` | yes | StatefulSet name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `containers` | string | comma-separated container names (pod template) |
| `images` | string | comma-separated container images (pod template) |
| `resourceRequests` | string | per-container CPU/memory requests, e.g. `"app: cpu=100m mem=128Mi"`; `""` if none set |
| `resourceLimits` | string | per-container CPU/memory limits; `""` if none set |
| `serviceAccount` | string | pod template's service account, `""` if default |
| `selector` | string | `spec.selector` matchLabels |
| `serviceName` | string | governing headless service (`spec.serviceName`) |
| `updateStrategy` | string | `RollingUpdate\|OnDelete` |
| `partition` | int | RollingUpdate partition (canary cutoff), `-1` if unset |
| `podManagementPolicy` | string | `OrderedReady\|Parallel` |
| `volumeClaimTemplates` | string | per template `"name: size (storageClass)"`, `""` if none |
| `manifest` | string | full YAML manifest; env values redacted, managedFields stripped |

---

### `check_hpa`

HorizontalPodAutoscaler replica bounds, current scale, metrics, and scaling conditions. A missing HPA is reported as `exists: false` (no autoscaling), not an error.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | HPA namespace |
| `name` | yes | HPA name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `exists` | bool | HPA exists |
| `scaleTarget` | string | scale target, e.g. `Deployment/coredns` |
| `minReplicas` | int | `spec.minReplicas` (1 if unset) |
| `maxReplicas` | int | `spec.maxReplicas` |
| `currentReplicas` | int | `status.currentReplicas` |
| `desiredReplicas` | int | `status.desiredReplicas` |
| `atMax` | bool | `currentReplicas >= maxReplicas` (saturated, cannot scale out further) |
| `ableToScale` | bool | `AbleToScale` condition is True |
| `scalingLimited` | bool | `ScalingLimited` condition is True (held at a min/max bound) |
| `metrics` | string | per-metric current vs target, one line each: `<name>: cur=<v> target=<v>` |
| `conditionReason` | string | reason/message when scaling is limited or unable to scale, `""` otherwise |

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

Sanitized service manifest, with structured fields broken out.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Service namespace |
| `name` | yes | Service name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `type` | string | `ClusterIP\|NodePort\|LoadBalancer\|ExternalName` |
| `clusterIP` | string | cluster IP, `"None"` for headless, `""` if unset |
| `selector` | string | pod selector, `""` if selector-less |
| `ports` | string | rendered `port→targetPort/Protocol` list |
| `externalName` | string | `spec.externalName`, `""` unless type ExternalName |
| `loadBalancerIngress` | string | LB IP/hostname(s), `""` if none/pending |
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

## Config

### `check_configmap`

ConfigMap existence, keys, and rendered data. A missing ConfigMap is reported as
`exists: false` (not an error), so existence is itself a usable finding.

> Unlike the `describe_*` methods, ConfigMap **values are NOT redacted** (a
> ConfigMap is non-secret by Kubernetes contract). If a ConfigMap holds anything
> sensitive, set a step `summaryFilter` to keep `data` away from a hosted LLM.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | ConfigMap namespace |
| `name` | yes | ConfigMap name |
| `keys` | no | comma-separated key names to render in `data` (default: all keys). The `keys`/`keyCount` outputs still describe every key present, so a missing key stays visible. |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `exists` | bool | ConfigMap exists |
| `keyCount` | int | number of keys (`data` + `binaryData`), all keys |
| `keys` | string | comma-separated key names, sorted, all keys |
| `data` | string | rendered `key:\n<value>` blocks for the selected keys (values not redacted), truncated if large; binary keys rendered as `<N binary bytes>` |

---

## Storage

### `check_pvc`

PersistentVolumeClaim binding status. A missing PVC is reported as `exists: false` (not an error), so existence is itself a usable finding.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | PVC namespace |
| `name` | yes | PVC name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `exists` | bool | PVC exists |
| `phase` | string | `Pending\|Bound\|Lost`, `""` if not exists |
| `storageClass` | string | `spec.storageClassName`, `""` if nil/default |
| `requestedStorage` | string | `spec.resources.requests.storage` |
| `capacity` | string | `status.capacity.storage` (actual), `""` if unbound |
| `volumeName` | string | bound PV name, `""` if unbound |
| `accessModes` | string | comma-separated access modes |
| `volumeMode` | string | `Filesystem\|Block` |

---

## Batch

### `check_job`

Job completion and failure status. A missing Job is reported as `exists: false` (not an error), so existence is itself a usable finding.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | Job namespace |
| `name` | yes | Job name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `exists` | bool | Job exists |
| `active` | int | `status.active` |
| `succeeded` | int | `status.succeeded` |
| `failed` | int | `status.failed` |
| `completions` | int | `spec.completions`, `-1` if unset |
| `parallelism` | int | `spec.parallelism`, `1` if unset |
| `backoffLimit` | int | `spec.backoffLimit`, `6` if unset (k8s default) |
| `complete` | bool | Complete condition is True |
| `failedCondition` | bool | Failed condition is True |
| `conditionReason` | string | e.g. `BackoffLimitExceeded`, `DeadlineExceeded`, `""` if none |

---

### `check_cronjob`

CronJob schedule and recent run status. A missing CronJob is reported as `exists: false` (not an error), so existence is itself a usable finding.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | CronJob namespace |
| `name` | yes | CronJob name |

**Outputs**

| Name | Type | Description |
|---|---|---|
| `exists` | bool | CronJob exists |
| `schedule` | string | `spec.schedule` (cron expression) |
| `suspended` | bool | `spec.suspend` |
| `activeJobs` | int | number of currently active jobs |
| `lastScheduleTime` | string | RFC3339, `""` if never scheduled |
| `lastSuccessfulTime` | string | RFC3339, `""` if never succeeded |
| `concurrencyPolicy` | string | `Allow\|Forbid\|Replace` |

---

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

**List output `failedChecks`** (one entry per failing health-check name)

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
| `includeOOM` | no | count **not-ready** pods whose last termination was `OOMKilled` / non-zero exit (default `true`). A recovered pod (now Running + Ready) is not counted on its historical `lastState` alone. |
| `includeNotReady` | no | count any not-Ready pod (default `false`) |
| `maxListItems` | no | cap the `pods` list at this many items, worst-first (default `50`; `0` = unlimited) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `count` | int | number of failing pods matched |
| `anyFailing` | bool | `count > 0` |
| `listTruncated` | bool | `true` if more pods matched than the `pods` list carries |

**List output `pods`** (items sorted worst-first by `restartCount`)

| Item field | Type | Description |
|---|---|---|
| `namespace` | string | pod namespace |
| `name` | string | pod name |
| `reason` | string | dominant failure reason (e.g. `CrashLoopBackOff`, `OOMKilled`) |
| `restartCount` | int | max restartCount across the pod's containers |
| `node` | string | scheduled node, `""` if unscheduled |

Reference the list from a `forEach` step: `forEach: $(steps.<step>.pods)`, then
bind `$(item.namespace)` / `$(item.name)` in the step's `with`. `$(item.node)` lets the
same fan-out drill from a crashing pod straight into its node (`check_node_status` /
`describe_node`) — memory pressure explains an OOM, disk pressure an image-pull failure.

Note: `maxListItems` caps the `pods` list output itself; the step's separate `maxItems` field caps how many of those items a subsequent `forEach` iterates.

---

### `list_pods`

All pods of a workload (Deployment / DaemonSet / StatefulSet), not-ready first.
Produces a **list output** (`pods`) consumable only by a `forEach` step. Unlike
`list_failing_pods`, this includes healthy/running pods — use it to fan out checks
(e.g. `check_pod_resources` + `check_pod_usage`) over every pod regardless of
health, so resourcing can be assessed for pods that are not failing.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `namespace` | yes | workload namespace |
| `kind` | yes | `Deployment` \| `DaemonSet` \| `StatefulSet` |
| `name` | yes | workload name |
| `maxListItems` | no | cap the `pods` list at this many items, worst-first (default `50`; `0` = unlimited) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `count` | int | number of pods owned by the workload |
| `notReadyCount` | int | pods whose Ready condition is not True |
| `listTruncated` | bool | `true` if more pods were owned than the `pods` list carries |

**List output `pods`** (items sorted not-ready first, then by `restartCount`)

| Item field | Type | Description |
|---|---|---|
| `namespace` | string | pod namespace |
| `name` | string | pod name |
| `ready` | bool | Ready condition is True |
| `restartCount` | int | max restartCount across the pod's containers |
| `node` | string | scheduled node, `""` if unscheduled |

Reference the list from a `forEach` step: `forEach: $(steps.<step>.pods)`, then
bind `$(item.namespace)` / `$(item.name)` in the step's `with`.

Note: `maxListItems` caps the `pods` list output itself; the step's separate `maxItems` field caps how many of those items a subsequent `forEach` iterates.

---

### `list_nodes`

Fleet-wide node health for large clusters, **bucketed by status**. Scans all nodes
(optionally scoped by a label selector) and returns per-status **counts** as scalars
plus a **list output** (`nodes`) of only the *not-fully-healthy* nodes, worst-first.
Healthy nodes are counted but never listed, so a 400-node cluster collapses to a few
rows + counts rather than 400 manifests. Pair with `describe_node` via a `forEach` to
drill into only the surfaced problem nodes.

A node is "not fully healthy" (and therefore listed) when it is NotReady, under any
pressure (memory/disk/PID), or cordoned (`spec.unschedulable`). Set `includeHealthy`
to also list the rest.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `labelSelector` | no | k8s label selector to scope the scan (e.g. a nodepool/role label); empty = all nodes |
| `includeHealthy` | no | `"true"` to also list Ready/schedulable/no-pressure nodes (default `"false"` = problem nodes only) |
| `maxListItems` | no | cap the `nodes` list at this many items, worst-first (default `"50"`; `"0"` = unlimited) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `total` | int | nodes matched by the selector |
| `ready` | int | nodes with Ready=True |
| `notReady` | int | nodes with Ready≠True |
| `memoryPressure` | int | nodes with MemoryPressure=True |
| `diskPressure` | int | nodes with DiskPressure=True |
| `pidPressure` | int | nodes with PIDPressure=True |
| `unschedulable` | int | cordoned nodes (`spec.unschedulable`) |
| `anyUnhealthy` | bool | `notReady > 0` or any pressure count > 0 |
| `listTruncated` | bool | `true` if more problem nodes matched than the `nodes` list carries |

**List output `nodes`** (items sorted worst-first: NotReady → pressured → cordoned-only → name)

| Item field | Type | Description |
|---|---|---|
| `name` | string | node name |
| `ready` | bool | Ready condition is True |
| `status` | string | compact status label, e.g. `NotReady`, `Ready,MemoryPressure`, `Ready,SchedulingDisabled` |
| `reason` | string | Ready reason when NotReady, else a pressure summary; `""` if none |
| `unschedulable` | bool | cordoned |

Reference the list from a `forEach` step: `forEach: $(steps.<step>.nodes)`, then bind
`$(item.name)` in the step's `with`. The scalar counts always reflect the full matched
fleet even when the list is capped, so a summary sees `397 ready / 2 NotReady / 1
MemoryPressure` regardless of `maxListItems`.

Note: `maxListItems` caps the `nodes` list output itself; the step's separate `maxItems`
field caps how many of those items a subsequent `forEach` iterates.

---

### `list_node_pods`

Pods scheduled on a given node (`spec.nodeName`), optionally filtered by a **name
regex** and/or a namespace, as a worst-first **list output** (`pods`). Built for node
troubleshooting: list the core-component pods on a node (e.g. CoreDNS, terway,
node-local-dns) and fan out per-pod checks (`check_pod_status`, `check_pod_logs`,
`check_pod_resources`) over them. Pods are field-selected by node server-side, so it
scales on large clusters.

The `namePattern` is an RE2 regex matched as a **partial** match against the pod name,
so `coredns` matches `coredns-7d8f-x` and `coredns|terway` matches either family;
anchor with `^…$` for an exact match. An invalid pattern is a param error.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `node` | yes | Node name; lists pods with `spec.nodeName` == this |
| `namePattern` | no | RE2 regex matched (partial) against pod name, e.g. `coredns\|terway`; empty = all pods on the node |
| `namespace` | no | restrict to this namespace; empty = all namespaces |
| `maxListItems` | no | cap the `pods` list at this many items, worst-first (default `"50"`; `"0"` = unlimited) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `count` | int | pods matched (node + namespace + namePattern) |
| `notReadyCount` | int | matched pods whose Ready condition is not True |
| `listTruncated` | bool | `true` if more pods matched than the `pods` list carries |

**List output `pods`** (items sorted not-ready first, then by `restartCount`, then name)

| Item field | Type | Description |
|---|---|---|
| `namespace` | string | pod namespace |
| `name` | string | pod name |
| `ready` | bool | Ready condition is True |
| `restartCount` | int | max restartCount across the pod's containers |
| `phase` | string | pod phase (`Pending\|Running\|Succeeded\|Failed\|Unknown`) |
| `reason` | string | dominant waiting/termination reason (e.g. `CrashLoopBackOff`, `OOMKilled`), `""` if none |

Reference the list from a `forEach` step: `forEach: $(steps.<step>.pods)`, then bind
`$(item.namespace)` / `$(item.name)` in the step's `with`. The scalar `count` reflects
the full match even when the list is capped by `maxListItems`.

---

## Probes

Active checks run **from kato's pod**: they send real traffic rather than reading the
API server, so they answer "can this actually be reached from inside the cluster?".
A failed probe is a finding (`success: false`), never a method error, so a flow can
gate later steps on `$(steps.<step>.success)`. None of them need Kubernetes RBAC —
reachability is governed by NetworkPolicy.

### `probe_tcp`

Active TCP connect check: opens a TCP connection to `target:port` and reports whether
it was accepted within the timeout (a "telnet"-style port check, e.g. "is the database
serving on 5432?"). A refused/timed-out connection is a finding (`success: false`), not
an error, so a flow can gate later steps on `$(steps.<step>.success)`. Runs from kato's
pod; reachability is governed by NetworkPolicy (no Kubernetes RBAC needed).

**Inputs**

| Name | Required | Description |
|---|---|---|
| `target` | yes | host, IP, or DNS name (e.g. `postgres.data.svc.cluster.local`, `10.0.0.5`) |
| `port` | yes | TCP port (1–65535) |
| `timeout` | no | connect timeout as a Go duration (default `5s`) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `success` | bool | TCP connection established within the timeout |
| `latencyMs` | int | connect time in ms; `-1` on failure |
| `error` | string | failure reason (refused/timeout/DNS); `""` on success |

---

### `probe_http`

Active HTTP(S) `GET` to `scheme://target:port/path`, asserting the response status code
and (optionally) that the body contains a substring (e.g. "does the health endpoint
return 200?"). A transport failure or non-matching status/body is a finding
(`success: false`), not an error. The response body is never returned as an output —
only `statusCode`/`statusMatched`/`bodyMatched` — so payloads (which may contain
secrets) stay out of the Run record and the LLM evidence. Runs from kato's pod;
reachability is governed by NetworkPolicy (no Kubernetes RBAC needed).

**Inputs**

| Name | Required | Description |
|---|---|---|
| `target` | yes | host, IP, or DNS name |
| `port` | yes | port (1–65535) |
| `scheme` | no | `http` or `https` (default `http`) |
| `path` | no | request path (default `/`) |
| `expectStatus` | no | expected HTTP status code, 100–599 (default `200`) |
| `expectBodyContains` | no | substring the response body must contain (matched within the first 64 KiB of the response); empty = no body check |
| `insecureSkipVerify` | no | `true` to accept self-signed certs (https only; default `false`) |
| `timeout` | no | request timeout as a Go duration (default `5s`) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `success` | bool | `statusMatched` and `bodyMatched` |
| `statusCode` | int | HTTP status; `0` if no response (transport failure) |
| `statusMatched` | bool | `statusCode == expectStatus` |
| `bodyMatched` | bool | body contains `expectBodyContains` (`true` if unset) |
| `latencyMs` | int | round-trip in ms; `-1` on failure |
| `error` | string | failure reason; `""` on success |

---

### `probe_dns`

Active DNS resolution: resolves `name` to its A/AAAA addresses and reports whether it
resolved, the addresses, and the query latency (e.g. "does `kubernetes.default.svc.cluster.local`
resolve?"). A failed lookup (NXDOMAIN, timeout, server unreachable) is a finding
(`success: false`), not an error, so a flow can gate later steps on `$(steps.<step>.success)`.
By default the query uses the pod's configured resolver (`/etc/resolv.conf`) — what real
workloads experience; set `server` to send the query to a specific resolver (e.g. the
node-local-dns link-local IP or the kube-dns clusterIP) to isolate which layer is broken.
Runs from kato's pod; reachability is governed by NetworkPolicy (no Kubernetes RBAC needed).

To resolve several names, fan out with `forEach`.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `name` | yes | hostname to resolve (e.g. `kubernetes.default.svc.cluster.local`) |
| `server` | no | DNS server IP to query directly; empty = pod's configured resolver |
| `port` | no | DNS server port (default `53`); only used when `server` is set |
| `timeout` | no | query timeout as a Go duration (default `5s`) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `success` | bool | resolution returned at least one address |
| `addresses` | string | comma-separated resolved IPs (A/AAAA), sorted; `""` if none |
| `recordCount` | int | number of addresses resolved |
| `latencyMs` | int | query time in ms; `-1` on failure |
| `error` | string | failure reason (NXDOMAIN/timeout/unreachable); `""` on success |

---

### `probe_traceroute`

Active ICMP traceroute from kato's pod: sends echo probes with an increasing IP TTL and
reads the returning `time-exceeded` (intermediate routers) and `echo-reply` (destination)
messages, reporting whether the destination was **reached**, how many **hops** away it is,
and the per-hop path. Scoped to reachability + hop count — when `success` is `false`, the
`hops` list shows *where* replies stopped. A not-reached destination, a DNS failure, or a
blocked ICMP socket is a finding (`success: false`), not an error, so a flow can gate later
steps on `$(steps.<step>.success)`. IPv4 only.

Uses an **unprivileged ICMP datagram socket** — no `CAP_NET_RAW` and no Kubernetes RBAC. Its
one requirement is that the node sysctl `net.ipv4.ping_group_range` includes kato's pod GID
(many distros default it open: `0 2147483647`). Where it is locked down, the probe surfaces
`success: false` with an `error` naming the sysctl — it never crashes and never needs raw
sockets. Reachability is otherwise governed by NetworkPolicy.

Worst-case wall time ≈ `maxHops × probesPerHop × timeout` when the target is unreachable, so
keep the engine step timeout in mind, or lower `maxHops`.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `target` | yes | host, IP, or DNS name (resolved to its first IPv4) |
| `maxHops` | no | maximum TTL to probe before giving up (1–255; default `30`) |
| `timeout` | no | per-hop reply wait as a Go duration (default `2s`) |
| `probesPerHop` | no | probes sent per TTL (1–10; default `1`); `>1` improves accuracy against transient loss |
| `resolveNames` | no | `"true"` to reverse-DNS each responding hop (adds latency; default `"false"`) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `success` | bool | destination reached (an echo-reply was received) within `maxHops` |
| `hopCount` | int | hops to the destination when reached; `-1` if not reached |
| `respondingHops` | int | count of hops (TTLs) that returned any reply |
| `destinationIp` | string | resolved IPv4 of `target`; `""` if DNS resolution failed |
| `latencyMs` | int | RTT to the destination on the final hop in ms; `-1` if not reached |
| `error` | string | setup failure (DNS / ICMP socket); `""` when the traceroute ran |

**List output `hops`** (one record per probed TTL, in hop order)

| Item field | Type | Description |
|---|---|---|
| `hop` | int | TTL / hop number (1-based) |
| `address` | string | responding router IP; `""` for a silent (`*`) hop |
| `name` | string | reverse-DNS hostname when `resolveNames` is set; else `""` |
| `rttMs` | int | RTT for this hop in ms; `-1` if no reply |
| `responded` | bool | a reply was received at this TTL |
| `reached` | bool | this hop is the destination (echo-reply) |

When `success` is `false`, reference the list from a `forEach` step to surface the path:
`forEach: $(steps.<step>.hops)`, then bind `$(item.address)` / `$(item.hop)` in the step's
`with`. Pair with `probe_tcp`: if a TCP connect fails, a `probe_traceroute` (gated by `when`)
shows whether the path even gets close — distinguishing "service down" from "network path broken".

---

### `probe_grpc`

Active gRPC health check from kato's pod: dials `target:port` and calls the standard
`grpc.health.v1.Health/Check` RPC (the same check as the widely-used `grpc-health-probe`),
reporting whether the service is **SERVING**. A `NOT_SERVING`/`UNKNOWN` status, a transport
failure, a missing health service (`UNIMPLEMENTED`), or an unregistered service name
(`NotFound`) is a finding (`success: false`), not an error — so a flow can gate later steps on
`$(steps.<step>.success)`. Set an empty `service` (the default) to check overall server health,
or a service name to check a specific registered service. Supports plaintext (`tls: "false"`,
the default) and TLS (`tls: "true"`, with `insecureSkipVerify`/`serverName` for cert handling).
Runs from kato's pod; reachability is governed by NetworkPolicy (no Kubernetes RBAC needed).

**Inputs**

| Name | Required | Description |
|---|---|---|
| `target` | yes | host, IP, or DNS name |
| `port` | yes | gRPC port (1–65535) |
| `service` | no | health service name; empty = overall server health |
| `tls` | no | `"true"` for TLS, `"false"` for plaintext h2c (default `"false"`) |
| `insecureSkipVerify` | no | `"true"` to accept self-signed certs (TLS only; default `"false"`) |
| `serverName` | no | TLS SNI / cert-name override; empty = derived from `target` |
| `timeout` | no | whole-operation timeout (dial + RPC) as a Go duration (default `5s`) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `success` | bool | health status is `SERVING` |
| `status` | string | `SERVING`/`NOT_SERVING`/`UNKNOWN`; `""` if the RPC never completed |
| `latencyMs` | int | Check round-trip in ms; `-1` on failure |
| `error` | string | failure reason (dial/timeout/`UNIMPLEMENTED`/`NotFound`); `""` on success |

Pair with `probe_tcp`: gate `probe_grpc` on `$(steps.<tcp>.success)` so the health RPC runs only
when the port is open, separating "network path broken" from "service unhealthy". `status` is the
gRPC analog of `probe_http`'s `statusCode`: a non-empty `status` means the endpoint answered
(reachable), while `success` is specifically `SERVING`.

---
