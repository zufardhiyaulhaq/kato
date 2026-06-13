package methods

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckConfigMapExists(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "eni-config", Namespace: "kube-system"},
		Data: map[string]string{
			"eni_conf":       `{"version":"1"}`,
			"10-terway.conf": `{"cniVersion":"0.3.1"}`,
		},
	}
	client := fake.NewSimpleClientset(cm)
	m, ok := Builtin().Get("check_configmap")
	if !ok {
		t.Fatal("check_configmap not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "kube-system", "name": "eni-config"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["exists"] != true || out["keyCount"] != int64(2) {
		t.Errorf("exists=%v keyCount=%v", out["exists"], out["keyCount"])
	}
	if out["keys"] != "10-terway.conf,eni_conf" { // sorted
		t.Errorf("keys = %v", out["keys"])
	}
	if data, _ := out["data"].(string); !strings.Contains(data, "cniVersion") {
		t.Errorf("data missing key value: %q", data)
	}
}

func TestCheckConfigMapKeyFilter(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "eni-config", Namespace: "kube-system"},
		Data:       map[string]string{"eni_conf": "ENICONF", "10-terway.conf": "CNICONF"},
	}
	client := fake.NewSimpleClientset(cm)
	m, _ := Builtin().Get("check_configmap")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "kube-system", "name": "eni-config", "keys": "eni_conf"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// keys output still lists ALL present keys (so a missing key is visible).
	if out["keys"] != "10-terway.conf,eni_conf" || out["keyCount"] != int64(2) {
		t.Errorf("keys=%v keyCount=%v", out["keys"], out["keyCount"])
	}
	// data renders only the selected key.
	data, _ := out["data"].(string)
	if !strings.Contains(data, "ENICONF") {
		t.Errorf("selected key not rendered: %q", data)
	}
	if strings.Contains(data, "CNICONF") {
		t.Errorf("unselected key rendered: %q", data)
	}
}

func TestCheckConfigMapMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_configmap")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "kube-system", "name": "nope"})
	if err != nil {
		t.Fatalf("missing configmap should not error: %v", err)
	}
	if out["exists"] != false || out["keyCount"] != int64(0) {
		t.Errorf("out = %+v", out)
	}
}
