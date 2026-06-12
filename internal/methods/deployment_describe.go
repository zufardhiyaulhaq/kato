package methods

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

type describeDeployment struct{}

func (describeDeployment) Name() string        { return "describe_deployment" }
func (describeDeployment) Description() string { return "Sanitized deployment manifest (spec+status)" }

func (describeDeployment) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Deployment namespace"},
		{Name: "name", Required: true, Description: "Deployment name"},
	}
}

func (describeDeployment) OutputFields() []OutputField {
	return []OutputField{
		{Name: "containers", Type: FieldString, Description: "comma-separated container names (pod template)"},
		{Name: "images", Type: FieldString, Description: "comma-separated container images (pod template)"},
		{Name: "resourceRequests", Type: FieldString, Description: `per-container CPU/memory requests, e.g. "app: cpu=100m mem=128Mi"; "" if none set`},
		{Name: "resourceLimits", Type: FieldString, Description: `per-container CPU/memory limits; "" if none set`},
		{Name: "strategy", Type: FieldString, Description: "RollingUpdate|Recreate"},
		{Name: "serviceAccount", Type: FieldString, Description: `pod template's service account, "" if default`},
		{Name: "manifest", Type: FieldString, Description: "full YAML manifest; env values redacted, managedFields stripped"},
	}
}

func (describeDeployment) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	d, err := deps.Kube.AppsV1().Deployments(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get deployment %s/%s: %w", params["namespace"], params["name"], err)
	}
	dep := d.DeepCopy()
	sanitizeObjectMeta(&dep.ObjectMeta)
	for i := range dep.Spec.Template.Spec.Containers {
		redactEnv(dep.Spec.Template.Spec.Containers[i].Env)
	}
	for i := range dep.Spec.Template.Spec.InitContainers {
		redactEnv(dep.Spec.Template.Spec.InitContainers[i].Env)
	}
	y, err := yaml.Marshal(dep)
	if err != nil {
		return nil, fmt.Errorf("marshal deployment: %w", err)
	}
	tmpl := d.Spec.Template.Spec
	return Outputs{
		"containers":       containerNames(tmpl.Containers),
		"images":           containerImages(tmpl.Containers),
		"resourceRequests": renderResourceList(tmpl.Containers, false),
		"resourceLimits":   renderResourceList(tmpl.Containers, true),
		"strategy":         string(d.Spec.Strategy.Type),
		"serviceAccount":   tmpl.ServiceAccountName,
		"manifest":         Truncate(string(y), defaultLogBytes),
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(describeDeployment{}) }) }
