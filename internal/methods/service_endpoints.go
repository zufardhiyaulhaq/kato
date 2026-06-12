package methods

import (
	"context"
	"fmt"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkServiceEndpoints struct{}

func (checkServiceEndpoints) Name() string { return "check_service_endpoints" }
func (checkServiceEndpoints) Description() string {
	return "Does the service selector match ready endpoints?"
}

func (checkServiceEndpoints) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Service namespace"},
		{Name: "name", Required: true, Description: "Service name"},
	}
}

func (checkServiceEndpoints) OutputFields() []OutputField {
	return []OutputField{
		{Name: "hasSelector", Type: FieldBool, Description: "service has a pod selector"},
		{Name: "readyEndpoints", Type: FieldInt, Description: "endpoints in Ready condition"},
		{Name: "notReadyEndpoints", Type: FieldInt, Description: "endpoints not Ready"},
	}
}

func (checkServiceEndpoints) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	svc, err := deps.Kube.CoreV1().Services(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get service %s/%s: %w", params["namespace"], params["name"], err)
	}
	slices, err := deps.Kube.DiscoveryV1().EndpointSlices(params["namespace"]).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + params["name"],
	})
	if err != nil {
		return nil, fmt.Errorf("list endpointslices for %s: %w", params["name"], err)
	}
	ready, notReady := int64(0), int64(0)
	for _, s := range slices.Items {
		for _, ep := range s.Endpoints {
			if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
				ready++
			} else {
				notReady++
			}
		}
	}
	return Outputs{
		"hasSelector":       len(svc.Spec.Selector) > 0,
		"readyEndpoints":    ready,
		"notReadyEndpoints": notReady,
	}, nil
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkServiceEndpoints{}) })
}
