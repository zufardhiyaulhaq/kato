package methods

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkIngress struct{}

func (checkIngress) Name() string { return "check_ingress" }
func (checkIngress) Description() string {
	return "Ingress rules, backend service existence, LB status"
}

func (checkIngress) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Ingress namespace"},
		{Name: "name", Required: true, Description: "Ingress name"},
	}
}

func (checkIngress) OutputFields() []OutputField {
	return []OutputField{
		{Name: "rules", Type: FieldString, Description: "rendered host/path -> backend lines"},
		{Name: "missingBackends", Type: FieldString, Description: `comma-separated backend services that don't exist, "" if all exist`},
		{Name: "loadBalancerReady", Type: FieldBool, Description: "status.loadBalancer has an ingress IP/hostname"},
	}
}

func (checkIngress) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	ing, err := deps.Kube.NetworkingV1().Ingresses(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get ingress %s/%s: %w", params["namespace"], params["name"], err)
	}
	var rules []string
	missing := map[string]bool{}
	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, path := range rule.HTTP.Paths {
			backend := ""
			if path.Backend.Service != nil {
				backend = path.Backend.Service.Name
				_, err := deps.Kube.CoreV1().Services(params["namespace"]).Get(ctx, backend, metav1.GetOptions{})
				if errors.IsNotFound(err) {
					missing[backend] = true
				} else if err != nil {
					return nil, fmt.Errorf("check backend service %s: %w", backend, err)
				}
			}
			rules = append(rules, fmt.Sprintf("%s%s -> %s", rule.Host, path.Path, backend))
		}
	}
	missingList := make([]string, 0, len(missing))
	for name := range missing {
		missingList = append(missingList, name)
	}
	return Outputs{
		"rules":             strings.Join(rules, "\n"),
		"missingBackends":   strings.Join(missingList, ","),
		"loadBalancerReady": len(ing.Status.LoadBalancer.Ingress) > 0,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkIngress{}) }) }
