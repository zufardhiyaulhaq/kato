package store

import (
	"context"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/engine"
)

func runtimeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	_ = apiextensionsv1.AddToScheme(s)
	return s
}

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(runtimeScheme(t)).
		WithStatusSubresource(&v1alpha1.Run{}).
		WithObjects(objs...).Build()
}

func TestSaveRunWritesSpecAndStatus(t *testing.T) {
	c := newFakeClient(t)
	s := &Store{Client: c, Namespace: "kato"}
	res := engine.Result{
		Phase:       "Succeeded",
		Summary:     "it is OOM",
		ModelConfig: "ollama-local",
		Steps: []engine.StepResult{
			{Name: "status", Outcome: "completed", Outputs: map[string]any{"phase": "Running"}},
			{Name: "node", Outcome: "failed", Error: "not found"},
		},
	}
	run, err := s.SaveRun(context.Background(), "pod-crashloop",
		map[string]string{"namespace": "payments", "pod": "app-1"}, res,
		time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 12, 10, 0, 5, 0, time.UTC))
	if err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	if run.Spec.UseCase != "pod-crashloop" || run.Spec.Inputs["pod"] != "app-1" {
		t.Errorf("spec = %+v", run.Spec)
	}
	if run.Labels["kato.zufardhiyaulhaq.com/usecase"] != "pod-crashloop" {
		t.Errorf("missing usecase label: %v", run.Labels)
	}

	var got v1alpha1.Run
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(run), &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status.Phase != "Succeeded" || got.Status.Summary != "it is OOM" {
		t.Errorf("status = %+v", got.Status)
	}
	if len(got.Status.Steps) != 2 || got.Status.Steps[1].Outcome != "failed" {
		t.Errorf("steps = %+v", got.Status.Steps)
	}
	if got.Status.Steps[0].Outputs == nil {
		t.Error("step outputs not persisted")
	}
}

func TestSaveRunPersistsIterations(t *testing.T) {
	c := newFakeClient(t)
	s := &Store{Client: c, Namespace: "kato"}
	res := engine.Result{
		Phase: "Succeeded",
		Steps: []engine.StepResult{{
			Name: "check", Outcome: "completed",
			Note: "matched 3, checked 2 (worst-first); 1 not examined",
			Iterations: []engine.IterationResult{
				{Item: map[string]string{"name": "b"}, Outcome: "completed", Outputs: map[string]any{"restartCount": int64(9)}},
				{Item: map[string]string{"name": "a"}, Outcome: "failed", Error: "boom"},
			},
		}},
	}
	run, err := s.SaveRun(context.Background(), "fe", map[string]string{"workload": "nld"}, res,
		time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC), time.Date(2026, 6, 12, 10, 0, 1, 0, time.UTC))
	if err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	var got v1alpha1.Run
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(run), &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	st := got.Status.Steps[0]
	if st.Note == "" || len(st.Iterations) != 2 {
		t.Fatalf("iterations not persisted: %+v", st)
	}
	if st.Iterations[1].Outcome != "failed" || st.Iterations[1].Error != "boom" {
		t.Errorf("iteration1 = %+v", st.Iterations[1])
	}
	if st.Iterations[0].Outputs == nil {
		t.Error("iteration0 outputs not persisted")
	}
}

func TestGCDeletesExpiredRuns(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	old := &v1alpha1.Run{ObjectMeta: metav1.ObjectMeta{
		Name: "old", Namespace: "kato",
		CreationTimestamp: metav1.NewTime(now.Add(-8 * 24 * time.Hour))}}
	fresh := &v1alpha1.Run{ObjectMeta: metav1.ObjectMeta{
		Name: "fresh", Namespace: "kato",
		CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour))}}
	c := newFakeClient(t, old, fresh)
	s := &Store{Client: c, Namespace: "kato", TTL: 7 * 24 * time.Hour}

	deleted, err := s.GarbageCollect(context.Background(), now)
	if err != nil {
		t.Fatalf("GarbageCollect: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
	var list v1alpha1.RunList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].Name != "fresh" {
		t.Errorf("remaining = %+v", list.Items)
	}
}
