# kato Methods Expansion — Design

**Date:** 2026-06-13
**Status:** Approved (design); implementation plan pending

## Goal

Correct and expand kato's built-in, read-only method library so it covers the
workload, storage, and batch resources that real Kubernetes troubleshooting
flows reach for. Takes the library from **19 → 25 methods** plus **4
enrichments** of existing methods.

Driven by two inputs:

1. Direct observations — `describe_pod` / `describe_deployment` break out too few
   typed fields (most signal is trapped inside the `manifest` blob, unusable in
   `when` conditions or a tight `summaryFilter`); `describe_daemonset` is absent;
   StatefulSet, PVC, Job, and CronJob have no coverage.
2. Cross-check against the learnk8s "Troubleshooting Kubernetes deployments"
   flowchart (https://learnk8s.io/troubleshooting-deployments). Every branch was
   mapped to a kato method. The one genuine gap that fit kato's read-only model
   and wasn't already planned: `describe_service` is `manifest`-only, yet the
   flowchart leans on Service selector and `port`→`targetPort` matching.

## Non-goals / out of scope

The flowchart also asks questions kato deliberately will **not** answer, because
they require in-cluster connectivity tests or `exec` that a read-only operator
does not perform:

- DNS resolution checks
- `kube-proxy` / Service reachability probing
- "curl the app from inside the cluster"
- container "listening on `0.0.0.0`" verification

These stay out. Also explicitly dropped after review: `check_secret`,
`check_resourcequota`, `imagePullSecrets` field, `labels` field on any describe
method, and any generic `describe_resource` (the user wants type-specific
methods, since Pod / Deployment / DaemonSet / StatefulSet expose genuinely
different troubleshooting-relevant fields).

## Design principles (inherited, unchanged)

- Each method is a struct implementing `methods.Method`
  (`Name`/`Description`/`Params`/`OutputFields`/`Run`), registered via a file-local
  `init()` that appends to `builtinFns`.
- Output types are `string`, `int` (Go `int64` on the wire), or `bool`. **Every
  declared output is always present with a defined default** (`""`, `0`,
  `false`, `-1`), so a `when` condition never breaks on missing data.
- `describe_*` methods sanitize: `sanitizeObjectMeta` (strips `managedFields` +
  `last-applied-configuration`), `redactEnv` on every container/initContainer,
  `Truncate(..., defaultLogBytes)` on the `manifest`.
- Uses typed client-go clients from `deps.Kube` (`AppsV1`, `CoreV1`, `BatchV1`).
  No new client dependencies.
- `exists`-vs-error convention: **status** methods error when the object is
  absent (sibling of `check_deployment_status`); **lookup/existence** methods
  return `exists: false` (sibling of `check_configmap` / `check_hpa`).

## Shared render helpers (new, added once)

Added to a single file (e.g. `internal/methods/render.go`) and reused across
the describe methods, mirroring the existing `containerNames` /
`containerImages` / `volumeNames` / `renderResourceList` helpers in
`pod_describe.go`. Each returns `""` for an empty input so the output default
holds.

| Helper | Signature (conceptual) | Output format |
|---|---|---|
| `renderConditions` | `([]<T with Type/Status/Reason>) string` | `Ready=True, PodScheduled=True (—), MemoryPressure=False` — `Type=Status`, with ` (Reason)` appended when Reason is non-empty and Status != True |
| `renderProbes` | `([]corev1.Container) string` | per container, one line: `app: liveness=httpGet:8080/healthz readiness=tcp:8080 startup=—`; containers with no probes omitted; `""` if none anywhere |
| `renderKVMap` | `(map[string]string) string` | sorted `k=v, k=v`; `""` if empty. Used for `nodeSelector`, `selector` (matchLabels) |
| `renderTolerations` | `([]corev1.Toleration) string` | `key=value:Effect` (or `key:Effect` / `<all>:Effect` for operator Exists / empty key), comma-joined; `""` if none |
| `renderOwnerRefs` | `([]metav1.OwnerReference) string` | `Kind/Name`, comma-joined (e.g. `ReplicaSet/api-abc123`); `""` if none |
| `renderPorts` | `([]corev1.ServicePort) string` | `name:port→targetPort/Protocol` per port (name omitted if empty, e.g. `80→8080/TCP`), comma-joined |

`renderProbes` summarizes a `*corev1.Probe` as `httpGet:<port><path>` /
`tcp:<port>` / `exec` / `grpc:<port>`, and `—` when a given probe is nil.

## Enrichments

Existing outputs are retained; only additions are listed. Field types in
brackets; default `""`/`0`/`false`/`-1` as appropriate.

### `describe_pod` (internal/methods/pod_describe.go)

Add: `nodeName`[string], `conditions`[string] (PodScheduled, Initialized,
ContainersReady, Ready via `renderConditions`), `probes`[string]
(`renderProbes` over `spec.Containers`), `ownerReferences`[string]
(`renderOwnerRefs`), `nodeSelector`[string] (`renderKVMap`),
`tolerations`[string] (`renderTolerations`), `priorityClassName`[string],
`hostNetwork`[bool].

### `describe_deployment` (internal/methods/deployment_describe.go)

Add: `replicas`[int] (`spec.replicas`, `1` if nil), `selector`[string]
(`spec.selector.matchLabels` via `renderKVMap`), `maxSurge`[string],
`maxUnavailable`[string] (both from `RollingUpdate`; `""` for `Recreate`),
`minReadySeconds`[int], `revisionHistoryLimit`[int] (`-1` if nil),
`paused`[bool], `probes`[string] (pod-template containers), `nodeSelector`[string],
`tolerations`[string].

### `describe_node` (internal/methods/node_describe.go)

Add: `kubeletVersion`[string], `osImage`[string], `kernelVersion`[string],
`containerRuntime`[string] (all from `status.nodeInfo`), `capacityPods`[string]
(`status.capacity.pods`), `unschedulable`[bool] (`spec.unschedulable`),
`conditions`[string] (`renderConditions` over `status.conditions`).

### `describe_service` (internal/methods/service_describe.go)

Currently outputs `manifest` only. Add: `type`[string]
(ClusterIP|NodePort|LoadBalancer|ExternalName), `clusterIP`[string] (`""` or
`None` for headless), `selector`[string] (`renderKVMap`; `""` for selector-less
services like ExternalName / manually-managed endpoints), `ports`[string]
(`renderPorts`), `externalName`[string] (`""` unless type ExternalName),
`loadBalancerIngress`[string] (comma-joined IP/hostname from
`status.loadBalancer.ingress`, `""` if none/pending).

## New methods

### `describe_daemonset` (internal/methods/daemonset_describe.go — new)

`AppsV1().DaemonSets(ns).Get`. Sanitize + redactEnv over pod template.

| Output | Type | Source |
|---|---|---|
| `containers` | string | template container names |
| `images` | string | template container images |
| `resourceRequests` | string | `renderResourceList(false)` |
| `resourceLimits` | string | `renderResourceList(true)` |
| `serviceAccount` | string | template SA, `""` if default |
| `selector` | string | `spec.selector.matchLabels` |
| `updateStrategy` | string | `RollingUpdate` \| `OnDelete` |
| `maxUnavailable` | string | from RollingUpdate, `""` for OnDelete |
| `nodeSelector` | string | `renderKVMap` (central to which nodes a DS targets) |
| `tolerations` | string | `renderTolerations` |
| `probes` | string | `renderProbes` |
| `manifest` | string | sanitized YAML, truncated |

### `check_statefulset_status` (internal/methods/statefulset_status.go — new)

`AppsV1().StatefulSets(ns).Get`. **Errors if absent** (status method).

| Output | Type | Source |
|---|---|---|
| `desiredReplicas` | int | `spec.replicas` (`1` if nil) |
| `readyReplicas` | int | `status.readyReplicas` |
| `currentReplicas` | int | `status.currentReplicas` |
| `updatedReplicas` | int | `status.updatedReplicas` |
| `availableReplicas` | int | `status.availableReplicas` |
| `updateRevisionPending` | bool | `status.currentRevision != status.updateRevision` (rollout in flight) |

### `describe_statefulset` (internal/methods/statefulset_describe.go — new)

`AppsV1().StatefulSets(ns).Get`. Sanitize + redactEnv over pod template.

| Output | Type | Source |
|---|---|---|
| `containers` | string | template container names |
| `images` | string | template container images |
| `resourceRequests` | string | `renderResourceList(false)` |
| `resourceLimits` | string | `renderResourceList(true)` |
| `serviceAccount` | string | template SA |
| `selector` | string | `spec.selector.matchLabels` |
| `serviceName` | string | `spec.serviceName` (governing headless service) |
| `updateStrategy` | string | `RollingUpdate` \| `OnDelete` |
| `partition` | int | RollingUpdate `partition` (canary cutoff), `-1` if unset/OnDelete |
| `podManagementPolicy` | string | `OrderedReady` \| `Parallel` |
| `volumeClaimTemplates` | string | per template: `name: <size> (<storageClass>)`, comma-joined; `""` if none |
| `manifest` | string | sanitized YAML, truncated |

### `check_pvc` (internal/methods/pvc.go — new)

`CoreV1().PersistentVolumeClaims(ns).Get`. **`exists: false` if absent** (not an
error) — a missing PVC referenced by a Pod is itself the finding.

| Output | Type | Source |
|---|---|---|
| `exists` | bool | PVC found |
| `phase` | string | `status.phase` (`Pending`\|`Bound`\|`Lost`), `""` if not exists |
| `storageClass` | string | `spec.storageClassName` (`""` if nil/default) |
| `requestedStorage` | string | `spec.resources.requests.storage` |
| `capacity` | string | `status.capacity.storage` (actual bound size), `""` if unbound |
| `volumeName` | string | `spec.volumeName` (bound PV), `""` if unbound |
| `accessModes` | string | rendered `RWO,RWX,ROX` style, comma-joined |
| `volumeMode` | string | `Filesystem` \| `Block` |

### `check_job` (internal/methods/job.go — new)

`BatchV1().Jobs(ns).Get`. **`exists: false` if absent.**

| Output | Type | Source |
|---|---|---|
| `exists` | bool | Job found |
| `active` | int | `status.active` |
| `succeeded` | int | `status.succeeded` |
| `failed` | int | `status.failed` |
| `completions` | int | `spec.completions` (`-1` if nil) |
| `parallelism` | int | `spec.parallelism` (`1` if nil) |
| `backoffLimit` | int | `spec.backoffLimit` (`6` if nil — the k8s default) |
| `complete` | bool | `Complete` condition True |
| `failedCondition` | bool | `Failed` condition True |
| `conditionReason` | string | reason of Failed (or Complete) condition, e.g. `BackoffLimitExceeded`, `DeadlineExceeded`; `""` if none |

### `check_cronjob` (internal/methods/cronjob.go — new)

`BatchV1().CronJobs(ns).Get`. **`exists: false` if absent.**

| Output | Type | Source |
|---|---|---|
| `exists` | bool | CronJob found |
| `schedule` | string | `spec.schedule` (cron expression) |
| `suspended` | bool | `spec.suspend` (`false` if nil) |
| `activeJobs` | int | `len(status.active)` |
| `lastScheduleTime` | string | `status.lastScheduleTime` RFC3339, `""` if never |
| `lastSuccessfulTime` | string | `status.lastSuccessfulTime` RFC3339, `""` if never |
| `concurrencyPolicy` | string | `Allow` \| `Forbid` \| `Replace` |

## Testing

Each new method and each enrichment gets a `_test.go` using the existing
`k8s.io/client-go/kubernetes/fake` clientset pattern (see
`pod_describe_test.go`, `daemonset_status_test.go`, `hpa_test.go`). Tests assert:

- Every declared `OutputField` is present in the returned `Outputs` (the
  "always defaulted" guarantee).
- Representative happy-path field values from a hand-built object.
- For `exists`-convention methods: the not-found case returns
  `exists: false` with defaults and **no error**.
- For status methods: the not-found case returns an error.
- Sanitization holds where relevant (env redacted, `managedFields` gone).
- Render helpers get focused unit tests (empty input → `""`; ordering is
  deterministic/sorted where claimed).

## Documentation surface (no logic)

- `docs/METHOD.md` — add sections for the 6 new methods and the new fields on the
  4 enriched ones; update the Index table and `## Storage` / `## Batch` groupings.
- `README.md.gotmpl` "Built-in methods" table — 19 → 25; add **Storage**
  (`check_pvc`) and **Batch** (`check_job`, `check_cronjob`) rows, and the
  StatefulSet entries under Workloads. Regenerate `README.md` via `make readme`.
- `GET /api/v1/methods` reflects everything automatically from `OutputFields()`;
  no API code changes.

## File structure summary

```
internal/methods/
  render.go                    (new) shared renderConditions/Probes/KVMap/Tolerations/OwnerRefs/Ports
  render_test.go               (new)
  pod_describe.go              (edit) + fields
  deployment_describe.go       (edit) + fields
  node_describe.go             (edit) + fields
  service_describe.go          (edit) + fields
  daemonset_describe.go        (new) + test
  statefulset_status.go        (new) + test
  statefulset_describe.go      (new) + test
  pvc.go                       (new) + test
  job.go                       (new) + test
  cronjob.go                   (new) + test
docs/METHOD.md                 (edit)
charts/kato/README.md.gotmpl   (edit) -> make readme regenerates README.md
charts/kato/templates/rbac.yaml (edit) -> add PVC + batch read grants
```

No changes to the registry, executor, CRDs, or API layer.

**RBAC (required):** the `kato-reader` ClusterRole already grants `get/list/watch`
on `apps` `daemonsets` and `statefulsets`, so those methods need nothing. Two
grants are **missing** and must be added:

- add `persistentvolumeclaims` to the existing core (`apiGroups: [""]`) resource
  list — for `check_pvc`;
- add a new rule block `apiGroups: ["batch"]`, `resources: [jobs, cronjobs]`,
  `verbs: [get, list, watch]` — for `check_job` / `check_cronjob`.

No other resource is newly read (Service, Node, Pod, Deployment are already
granted), so no further RBAC change is needed.
