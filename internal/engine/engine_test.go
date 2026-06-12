package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

// engineUseCase: status -> (when restarts>0) prev-logs -> (when nodeName!="") node.
func engineUseCase() *v1alpha1.UseCase {
	return &v1alpha1.UseCase{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-crashloop"},
		Spec: v1alpha1.UseCaseSpec{
			Inputs: []v1alpha1.InputDecl{
				{Name: "namespace", Required: true}, {Name: "pod", Required: true},
			},
			Steps: []v1alpha1.Step{
				{Name: "status", Method: "check_pod_status",
					With: map[string]string{"namespace": "$(inputs.namespace)", "name": "$(inputs.pod)"}},
				{Name: "prev-logs", Method: "check_pod_logs",
					When: "$(steps.status.restartCount) > 0",
					With: map[string]string{"namespace": "$(inputs.namespace)", "name": "$(inputs.pod)"}},
				{Name: "node", Method: "check_node_status",
					When: `$(steps.status.nodeName) != ""`,
					With: map[string]string{"name": "$(steps.status.nodeName)"}},
			},
			Summary: v1alpha1.SummarySpec{Prompt: "diagnose"},
		},
	}
}

func newEngine(client *fake.Clientset, summarize SummarizeFn) *Engine {
	return &Engine{
		Deps: methods.Deps{Kube: client}, Registry: methods.Builtin(),
		Summarize: summarize, StepTimeout: 5 * time.Second,
	}
}

func okSummarizer(summary string) SummarizeFn {
	return func(_ context.Context, _ *v1alpha1.UseCase, _ []StepResult) (string, string, error) {
		return summary, "test-model", nil
	}
}

func TestExecuteHappyPath(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-7"}}
	client := fake.NewSimpleClientset(crashloopPodForEngine(), node)
	res, err := newEngine(client, okSummarizer("it is OOM")).
		Execute(context.Background(), engineUseCase(), map[string]string{"namespace": "payments", "pod": "app-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Phase != "Succeeded" {
		t.Errorf("phase = %q", res.Phase)
	}
	if res.Summary != "it is OOM" || res.ModelConfig != "test-model" {
		t.Errorf("summary=%q model=%q", res.Summary, res.ModelConfig)
	}
	for i, want := range []string{"completed", "completed", "completed"} {
		if res.Steps[i].Outcome != want {
			t.Errorf("step %d outcome = %q, want %q", i, res.Steps[i].Outcome, want)
		}
	}
}

func TestExecuteWhenFalseSkips(t *testing.T) {
	pod := crashloopPodForEngine()
	pod.Status.ContainerStatuses[0].RestartCount = 0 // no restarts -> skip prev-logs
	pod.Spec.NodeName = ""                           // unscheduled -> skip node
	client := fake.NewSimpleClientset(pod)
	res, err := newEngine(client, okSummarizer("s")).
		Execute(context.Background(), engineUseCase(), map[string]string{"namespace": "payments", "pod": "app-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Steps[1].Outcome != "skipped" || !strings.Contains(res.Steps[1].Reason, "when") {
		t.Errorf("step1 = %+v", res.Steps[1])
	}
	if res.Steps[2].Outcome != "skipped" {
		t.Errorf("step2 = %+v", res.Steps[2])
	}
	if res.Phase != "Succeeded" {
		t.Errorf("phase = %q (skips are not failures)", res.Phase)
	}
}

func TestExecuteFailedStepIsAFinding(t *testing.T) {
	// Pod missing entirely: step "status" fails, dependents auto-skip.
	client := fake.NewSimpleClientset()
	res, err := newEngine(client, okSummarizer("s")).
		Execute(context.Background(), engineUseCase(), map[string]string{"namespace": "payments", "pod": "ghost"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Steps[0].Outcome != "failed" || res.Steps[0].Error == "" {
		t.Errorf("step0 = %+v", res.Steps[0])
	}
	if res.Steps[1].Outcome != "skipped" || !strings.Contains(res.Steps[1].Reason, "status") {
		t.Errorf("step1 should auto-skip naming the failed dependency: %+v", res.Steps[1])
	}
	if res.Phase != "Failed" { // ALL runnable steps failed or were skipped due to it
		t.Errorf("phase = %q", res.Phase)
	}
}

func TestExecutePartialSuccess(t *testing.T) {
	// status succeeds; node step depends on it but the node object is absent,
	// so check_node_status fails -> PartiallySucceeded.
	client := fake.NewSimpleClientset(crashloopPodForEngine())
	res, err := newEngine(client, okSummarizer("s")).
		Execute(context.Background(), engineUseCase(), map[string]string{"namespace": "payments", "pod": "app-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Steps[2].Outcome != "failed" {
		t.Errorf("node step = %+v", res.Steps[2])
	}
	if res.Phase != "PartiallySucceeded" {
		t.Errorf("phase = %q", res.Phase)
	}
}

func TestExecuteSummarizerDownIsWarningNotFailure(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-7"}}
	client := fake.NewSimpleClientset(crashloopPodForEngine(), node)
	down := func(_ context.Context, _ *v1alpha1.UseCase, _ []StepResult) (string, string, error) {
		return "", "", errors.New("connection refused")
	}
	res, err := newEngine(client, down).
		Execute(context.Background(), engineUseCase(), map[string]string{"namespace": "payments", "pod": "app-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Phase != "Succeeded" || res.Summary != "" || res.Warning == "" {
		t.Errorf("phase=%q summary=%q warning=%q", res.Phase, res.Summary, res.Warning)
	}
}

func TestExecuteInputValidation(t *testing.T) {
	client := fake.NewSimpleClientset()
	e := newEngine(client, okSummarizer("s"))
	if _, err := e.Execute(context.Background(), engineUseCase(),
		map[string]string{"namespace": "x"}); err == nil {
		t.Fatal("missing required input should error")
	}
	if _, err := e.Execute(context.Background(), engineUseCase(),
		map[string]string{"namespace": "x", "pod": "y", "bogus": "z"}); err == nil {
		t.Fatal("unknown input should error")
	}
}

func crashloopPodForEngine() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "payments"},
		Spec:       corev1.PodSpec{NodeName: "node-7"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app", RestartCount: 17,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
		},
	}
}
