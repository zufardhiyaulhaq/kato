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
