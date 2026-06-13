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
