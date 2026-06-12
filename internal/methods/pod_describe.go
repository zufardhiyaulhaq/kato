package methods

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

type describePod struct{}

func (describePod) Name() string        { return "describe_pod" }
func (describePod) Description() string { return "Sanitized pod manifest (spec+status)" }

func (describePod) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Pod namespace"},
		{Name: "name", Required: true, Description: "Pod name"},
	}
}

func (describePod) OutputFields() []OutputField {
	return []OutputField{
		{Name: "containers", Type: FieldString, Description: "comma-separated container names"},
		{Name: "images", Type: FieldString, Description: "comma-separated container images"},
		{Name: "resourceRequests", Type: FieldString, Description: `per-container CPU/memory requests, e.g. "app: cpu=100m mem=128Mi"; "" if none set`},
		{Name: "resourceLimits", Type: FieldString, Description: `per-container CPU/memory limits; "" if none set`},
		{Name: "restartPolicy", Type: FieldString, Description: "Always|OnFailure|Never"},
		{Name: "serviceAccount", Type: FieldString, Description: `pod's service account, "" if default`},
		{Name: "volumes", Type: FieldString, Description: `comma-separated volume names, "" if none`},
		{Name: "manifest", Type: FieldString, Description: "full YAML manifest; env values redacted, managedFields stripped"},
	}
}

func (describePod) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	pod, err := deps.Kube.CoreV1().Pods(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", params["namespace"], params["name"], err)
	}
	p := pod.DeepCopy()
	sanitizeObjectMeta(&p.ObjectMeta)
	for i := range p.Spec.Containers {
		redactEnv(p.Spec.Containers[i].Env)
	}
	for i := range p.Spec.InitContainers {
		redactEnv(p.Spec.InitContainers[i].Env)
	}
	y, err := yaml.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal pod: %w", err)
	}
	return Outputs{
		"containers":       containerNames(pod.Spec.Containers),
		"images":           containerImages(pod.Spec.Containers),
		"resourceRequests": renderResourceList(pod.Spec.Containers, false),
		"resourceLimits":   renderResourceList(pod.Spec.Containers, true),
		"restartPolicy":    string(pod.Spec.RestartPolicy),
		"serviceAccount":   pod.Spec.ServiceAccountName,
		"volumes":          volumeNames(pod.Spec.Volumes),
		"manifest":         Truncate(string(y), defaultLogBytes),
	}, nil
}

// containerNames / containerImages / renderResourceList / volumeNames are
// shared by describe_pod and describe_deployment (which inspects its pod
// template's containers).
func containerNames(cs []corev1.Container) string {
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

func containerImages(cs []corev1.Container) string {
	imgs := make([]string, len(cs))
	for i, c := range cs {
		imgs[i] = c.Image
	}
	return strings.Join(imgs, ", ")
}

func volumeNames(vols []corev1.Volume) string {
	names := make([]string, len(vols))
	for i, v := range vols {
		names[i] = v.Name
	}
	return strings.Join(names, ", ")
}

// renderResourceList renders per-container CPU/memory. limits=false renders
// requests, limits=true renders limits.
func renderResourceList(cs []corev1.Container, limits bool) string {
	var parts []string
	for _, c := range cs {
		rl := c.Resources.Requests
		if limits {
			rl = c.Resources.Limits
		}
		seg, any := c.Name+":", false
		if cpu, ok := rl[corev1.ResourceCPU]; ok {
			seg += " cpu=" + cpu.String()
			any = true
		}
		if mem, ok := rl[corev1.ResourceMemory]; ok {
			seg += " mem=" + mem.String()
			any = true
		}
		if any {
			parts = append(parts, seg)
		}
	}
	return strings.Join(parts, "; ")
}

// sanitizeObjectMeta strips noise the AI never needs (spec §7).
func sanitizeObjectMeta(m *metav1.ObjectMeta) {
	m.ManagedFields = nil
	delete(m.Annotations, "kubectl.kubernetes.io/last-applied-configuration")
}

func redactEnv(env []corev1.EnvVar) {
	for i := range env {
		if env[i].Value != "" {
			env[i].Value = "[REDACTED]"
		}
	}
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(describePod{}) }) }
