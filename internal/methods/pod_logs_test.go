package methods

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Note: the fake clientset always returns "fake logs" for GetLogs; we assert
// wiring + output shape, not log content. Param conversion is tested via errors.
func TestCheckPodLogs(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "payments"}}
	client := fake.NewSimpleClientset(pod)
	m, ok := Builtin().Get("check_pod_logs")
	if !ok {
		t.Fatal("check_pod_logs not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1", "previous": "true", "tailLines": "100"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["logs"] != "fake logs" {
		t.Errorf("logs = %q", out["logs"])
	}
}

func TestBuildPodLogOptionsTailLinesDefault(t *testing.T) {
	// No tailLines supplied -> defaults to 10.
	opts, err := buildPodLogOptions(map[string]string{"namespace": "x", "name": "y"})
	if err != nil {
		t.Fatalf("buildPodLogOptions: %v", err)
	}
	if opts.TailLines == nil || *opts.TailLines != defaultTailLines {
		t.Fatalf("default tailLines = %v, want %d", opts.TailLines, defaultTailLines)
	}
	// Explicit tailLines overrides the default.
	opts, err = buildPodLogOptions(map[string]string{"name": "y", "tailLines": "250"})
	if err != nil {
		t.Fatalf("buildPodLogOptions: %v", err)
	}
	if opts.TailLines == nil || *opts.TailLines != 250 {
		t.Fatalf("explicit tailLines = %v, want 250", opts.TailLines)
	}
}

func TestCheckPodLogsBadParams(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_pod_logs")
	if _, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "x", "name": "y", "tailLines": "NaN"}); err == nil {
		t.Fatal("expected error for non-integer tailLines")
	}
	if _, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "x", "name": "y", "previous": "yes"}); err == nil {
		t.Fatal("expected error for non-bool previous")
	}
}
