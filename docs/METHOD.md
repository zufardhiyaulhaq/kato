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
| [`list_failing_pods`](#list_failing_pods) | Failing pods of a workload (Deployment/DaemonSet/StatefulSet), worst-first — produces list output `pods` |
| [`list_pods`](#list_pods) | All pods of a workload (Deployment/DaemonSet/StatefulSet), not-ready first — produces list output `pods` |

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

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `count` | int | number of pods owned by the workload |
| `notReadyCount` | int | pods whose Ready condition is not True |

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
