package methods

import (
	"context"
	"fmt"
	"strings"

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
		{Name: "type", Type: FieldString, Description: "ClusterIP|NodePort|LoadBalancer|ExternalName"},
		{Name: "clusterIP", Type: FieldString, Description: `cluster IP, "None" for headless, "" if unset`},
		{Name: "selector", Type: FieldString, Description: `pod selector, "" if selector-less`},
		{Name: "ports", Type: FieldString, Description: "rendered port→targetPort/Protocol list"},
		{Name: "externalName", Type: FieldString, Description: `spec.externalName, "" unless type ExternalName`},
		{Name: "loadBalancerIngress", Type: FieldString, Description: `LB IP/hostname(s), "" if none/pending`},
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
	lbParts := make([]string, 0, len(svc.Status.LoadBalancer.Ingress))
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			lbParts = append(lbParts, ing.IP)
		} else if ing.Hostname != "" {
			lbParts = append(lbParts, ing.Hostname)
		}
	}
	return Outputs{
		"type":                string(svc.Spec.Type),
		"clusterIP":           svc.Spec.ClusterIP,
		"selector":            renderKVMap(svc.Spec.Selector),
		"ports":               renderPorts(svc.Spec.Ports),
		"externalName":        svc.Spec.ExternalName,
		"loadBalancerIngress": strings.Join(lbParts, ", "),
		"manifest":            Truncate(string(y), defaultLogBytes),
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(describeService{}) }) }
