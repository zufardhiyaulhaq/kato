package methods

import (
	"context"
	"fmt"
	"strings"
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

func TestAggregateContainerLogs(t *testing.T) {
	got := aggregateContainerLogs([]containerLog{
		{name: "app", text: "line1\nline2"},
		{name: "istio-proxy", err: fmt.Errorf(`previous terminated container "istio-proxy" not found`)},
	})
	want := "=== container: app ===\nline1\nline2\n\n" +
		"=== container: istio-proxy ===\n(no logs: previous terminated container \"istio-proxy\" not found)"
	if got != want {
		t.Fatalf("aggregate =\n%q\nwant\n%q", got, want)
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

func TestCheckPodLogsAllContainers(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "payments"},
		Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "app"}, {Name: "istio-proxy"}},
			InitContainers: []corev1.Container{{Name: "init-db"}},
		},
	}
	client := fake.NewSimpleClientset(pod)
	m, _ := Builtin().Get("check_pod_logs")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1", "previous": "true"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	logs := out["logs"].(string)
	for _, c := range []string{"app", "istio-proxy", "init-db"} {
		if !strings.Contains(logs, "=== container: "+c+" ===") {
			t.Errorf("missing header for %q in:\n%s", c, logs)
		}
	}
	if strings.Count(logs, "fake logs") != 3 {
		t.Errorf("want 3 container log bodies, got:\n%s", logs)
	}
}

func TestCheckPodLogsSingleContainerNoHeader(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "payments"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	client := fake.NewSimpleClientset(pod)
	m, _ := Builtin().Get("check_pod_logs")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "app-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["logs"] != "fake logs" {
		t.Errorf("single-container logs = %q, want plain \"fake logs\"", out["logs"])
	}
}

func TestCheckPodLogsPodNotFound(t *testing.T) {
	client := fake.NewSimpleClientset() // no pods registered
	m, _ := Builtin().Get("check_pod_logs")
	if _, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "name": "missing"}); err == nil {
		t.Fatal("expected error when pod does not exist")
	}
}

func TestParseMaxLineLength(t *testing.T) {
	// unset -> default
	n, err := parseMaxLineLength(map[string]string{})
	if err != nil || n != defaultMaxLineLength {
		t.Fatalf("default = %d, %v; want %d", n, err, defaultMaxLineLength)
	}
	// explicit override
	n, err = parseMaxLineLength(map[string]string{"maxLineLength": "200"})
	if err != nil || n != 200 {
		t.Fatalf("override = %d, %v; want 200", n, err)
	}
	// "0" -> unlimited (0)
	n, err = parseMaxLineLength(map[string]string{"maxLineLength": "0"})
	if err != nil || n != 0 {
		t.Fatalf("zero = %d, %v; want 0", n, err)
	}
	// negative -> error
	if _, err := parseMaxLineLength(map[string]string{"maxLineLength": "-5"}); err == nil {
		t.Error("expected error for negative maxLineLength")
	}
	// non-integer -> error
	if _, err := parseMaxLineLength(map[string]string{"maxLineLength": "abc"}); err == nil {
		t.Error("expected error for non-integer maxLineLength")
	}
}

func TestCheckPodLogsBadMaxLineLength(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_pod_logs")
	if _, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "x", "name": "y", "maxLineLength": "abc"}); err == nil {
		t.Fatal("expected error for non-integer maxLineLength")
	}
}
