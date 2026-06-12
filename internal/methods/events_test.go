package methods

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckEvents(t *testing.T) {
	warn := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "payments"},
		InvolvedObject: corev1.ObjectReference{Name: "app-1", Kind: "Pod"},
		Type:           corev1.EventTypeWarning, Reason: "BackOff",
		Message: "Back-off restarting failed container", Count: 12,
	}
	normal := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e2", Namespace: "payments"},
		InvolvedObject: corev1.ObjectReference{Name: "other", Kind: "Pod"},
		Type:           corev1.EventTypeNormal, Reason: "Pulled", Message: "Image pulled",
	}
	client := fake.NewSimpleClientset(warn, normal)
	m, ok := Builtin().Get("check_events")
	if !ok {
		t.Fatal("check_events not registered")
	}

	// Filtered by involvedObject.
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "involvedObject": "app-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != int64(1) || out["warningCount"] != int64(1) {
		t.Errorf("count=%v warningCount=%v", out["count"], out["warningCount"])
	}
	if !strings.Contains(out["events"].(string), "BackOff") ||
		strings.Contains(out["events"].(string), "Pulled") {
		t.Errorf("events = %q", out["events"])
	}

	// Whole namespace.
	out, err = m.Run(context.Background(), Deps{Kube: client}, map[string]string{"namespace": "payments"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != int64(2) {
		t.Errorf("count = %v, want 2", out["count"])
	}
}
