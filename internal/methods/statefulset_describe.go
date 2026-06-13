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

func (describeStatefulSet) Name() string { return "describe_statefulset" }
func (describeStatefulSet) Description() string {
	return "Sanitized statefulset manifest (spec+status)"
}

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
