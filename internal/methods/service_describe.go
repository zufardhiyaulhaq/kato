package methods

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

type describeService struct{}

func (describeService) Name() string        { return "describe_service" }
func (describeService) Description() string { return "Sanitized service manifest" }

func (describeService) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Service namespace"},
		{Name: "name", Required: true, Description: "Service name"},
	}
}

func (describeService) OutputFields() []OutputField {
	return []OutputField{
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
	return Outputs{"manifest": Truncate(string(y), defaultLogBytes)}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(describeService{}) }) }
