package methods

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

func TestCheckServiceEndpoints(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "payments"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "api"}},
	}
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-abc", Namespace: "payments",
			Labels: map[string]string{discoveryv1.LabelServiceName: "api"},
		},
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)}},
			{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(false)}},
		},
	}
	client := fake.NewSimpleClientset(svc, slice)
	m, ok := Builtin().Get("check_service_endpoints")
	if !ok {
		t.Fatal("check_service_endpoints not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := Outputs{"hasSelector": true, "readyEndpoints": int64(1), "notReadyEndpoints": int64(1)}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %#v, want %#v", k, out[k], v)
		}
	}
}

func TestDescribeService(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "payments"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "api"},
			Ports:    []corev1.ServicePort{{Port: 80}},
		},
	}
	client := fake.NewSimpleClientset(svc)
	m, ok := Builtin().Get("describe_service")
	if !ok {
		t.Fatal("describe_service not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out["manifest"].(string), "app: api") {
		t.Errorf("manifest = %q", out["manifest"])
	}
}

func TestCheckIngress(t *testing.T) {
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "payments"},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
			Host: "api.example.com",
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
				Paths: []networkingv1.HTTPIngressPath{{
					Path: "/",
					Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
						Name: "missing-svc", Port: networkingv1.ServiceBackendPort{Number: 80},
					}},
				}},
			}},
		}}},
	}
	client := fake.NewSimpleClientset(ing) // note: backing service NOT created
	m, ok := Builtin().Get("check_ingress")
	if !ok {
		t.Fatal("check_ingress not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["missingBackends"] != "missing-svc" {
		t.Errorf("missingBackends = %v", out["missingBackends"])
	}
	if out["loadBalancerReady"] != false {
		t.Errorf("loadBalancerReady = %v", out["loadBalancerReady"])
	}
	if !strings.Contains(out["rules"].(string), "api.example.com") {
		t.Errorf("rules = %q", out["rules"])
	}
}
