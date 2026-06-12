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

func feUseCase() *v1alpha1.UseCase {
	return &v1alpha1.UseCase{
		ObjectMeta: metav1.ObjectMeta{Name: "fe"},
		Spec: v1alpha1.UseCaseSpec{
			Inputs: []v1alpha1.InputDecl{{Name: "namespace", Required: true}, {Name: "workload", Required: true}},
			Steps: []v1alpha1.Step{
				{Name: "crashing", Method: "list_failing_pods",
					With: map[string]string{"namespace": "$(inputs.namespace)", "kind": "DaemonSet", "name": "$(inputs.workload)"}},
				{Name: "check", Method: "check_pod_status", ForEach: "$(steps.crashing.pods)", MaxItems: 2,
					With: map[string]string{"namespace": "$(item.namespace)", "name": "$(item.name)"}},
			},
			Summary: v1alpha1.SummarySpec{Prompt: "x"},
		},
	}
}

func fePod(name string, restarts int32) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system",
			OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: "nld"}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "c", RestartCount: restarts,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
		},
	}
}

func feEngine(client *fake.Clientset) *Engine {
	return &Engine{Deps: methods.Deps{Kube: client}, Registry: methods.Builtin(),
		Summarize: okSummarizer("s"), StepTimeout: 5 * time.Second}
}

func TestExecuteForEachCapsAndOrders(t *testing.T) {
	client := fake.NewSimpleClientset(fePod("a", 3), fePod("b", 9), fePod("c", 1))
	res, err := feEngine(client).Execute(context.Background(), feUseCase(),
		map[string]string{"namespace": "kube-system", "workload": "nld"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Steps[0].Outcome != "completed" || res.Steps[0].Outputs["count"] != int64(3) {
		t.Fatalf("crashing step = %+v", res.Steps[0])
	}
	fe := res.Steps[1]
	if fe.Outcome != "completed" {
		t.Fatalf("forEach step outcome = %q", fe.Outcome)
	}
	if len(fe.Iterations) != 2 {
		t.Fatalf("iterations = %d, want 2 (capped)", len(fe.Iterations))
	}
	if fe.Note == "" {
		t.Error("expected truncation note")
	}
	// Worst-first: b (9 restarts) then a (3).
	if fe.Iterations[0].Item["name"] != "b" || fe.Iterations[1].Item["name"] != "a" {
		t.Errorf("order wrong: %v, %v", fe.Iterations[0].Item, fe.Iterations[1].Item)
	}
	if fe.Iterations[0].Outcome != "completed" || fe.Iterations[0].Outputs["restartCount"] != int64(9) {
		t.Errorf("iteration0 outputs = %+v", fe.Iterations[0])
	}
	if res.Phase != "Succeeded" {
		t.Errorf("phase = %q", res.Phase)
	}
}

func TestExecuteForEachSkipsWhenSourceFailed(t *testing.T) {
	client := fake.NewSimpleClientset()
	uc := feUseCase()
	// Force the list step to fail at runtime (unsupported kind). The forEach
	// step gates on $(steps.crashing.anyFailing) and forEach: $(steps.crashing.pods),
	// so it must SKIP (not fail) because its dependency did not complete.
	uc.Spec.Steps[0].With["kind"] = "BadKind"
	res, err := feEngine(client).Execute(context.Background(), uc,
		map[string]string{"namespace": "kube-system", "workload": "nld"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Steps[0].Outcome != "failed" {
		t.Fatalf("source step should fail: %+v", res.Steps[0])
	}
	if res.Steps[1].Outcome != "skipped" || !strings.Contains(res.Steps[1].Reason, "crashing") {
		t.Errorf("forEach step should skip naming the failed source: %+v", res.Steps[1])
	}
}

func TestExecuteForEachZeroItemsSkips(t *testing.T) {
	client := fake.NewSimpleClientset() // no pods
	res, err := feEngine(client).Execute(context.Background(), feUseCase(),
		map[string]string{"namespace": "kube-system", "workload": "nld"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Steps[1].Outcome != "skipped" || res.Steps[1].Reason == "" {
		t.Errorf("forEach over empty list should skip: %+v", res.Steps[1])
	}
	if res.Phase != "Succeeded" {
		t.Errorf("phase = %q", res.Phase)
	}
}
