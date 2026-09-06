package store

import (
	"context"
	"strings"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/gopaytech/kato/api/v1alpha1"
	"github.com/gopaytech/kato/internal/engine"
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

func TestBuildRunStatusAppliesSummaryFilter(t *testing.T) {
	res := engine.Result{
		Phase: "Succeeded",
		Steps: []engine.StepResult{
			// non-nil filter -> only listed keys persisted (big manifest dropped)
			{Name: "describe", Outcome: "completed",
				SummaryFilter: []string{"tolerations"},
				Outputs:       map[string]any{"tolerations": "node-role:NoSchedule", "manifest": "HUGE-YAML-BLOB"}},
			// empty filter -> outputs omitted entirely
			{Name: "secret", Outcome: "completed",
				SummaryFilter: []string{},
				Outputs:       map[string]any{"data": "SENSITIVE"}},
			// nil filter -> all outputs (audit unchanged)
			{Name: "status", Outcome: "completed",
				Outputs: map[string]any{"phase": "Running", "restartCount": int64(3)}},
			// filter also applies to forEach iteration outputs
			{Name: "iter", Outcome: "completed",
				SummaryFilter: []string{"restartCount"},
				Iterations: []engine.IterationResult{
					{Item: map[string]string{"name": "p"}, Outcome: "completed",
						Outputs: map[string]any{"restartCount": int64(9), "phase": "Running"}},
				}},
		},
	}
	st, err := BuildRunStatus(res, time.Now(), time.Now())
	if err != nil {
		t.Fatalf("BuildRunStatus: %v", err)
	}
	if d := string(st.Steps[0].Outputs.Raw); !strings.Contains(d, "tolerations") ||
		strings.Contains(d, "manifest") || strings.Contains(d, "HUGE") {
		t.Errorf("describe outputs not filtered: %s", d)
	}
	if st.Steps[1].Outputs != nil {
		t.Errorf("empty summaryFilter should drop outputs, got %s", st.Steps[1].Outputs.Raw)
	}
	if s := string(st.Steps[2].Outputs.Raw); !strings.Contains(s, "phase") || !strings.Contains(s, "restartCount") {
		t.Errorf("nil filter should keep all outputs: %s", s)
	}
	if it := string(st.Steps[3].Iterations[0].Outputs.Raw); !strings.Contains(it, "restartCount") || strings.Contains(it, "phase") {
		t.Errorf("iteration outputs not filtered by parent summaryFilter: %s", it)
	}
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

func ptrTime(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}

func TestBuildRunStatusMapsResult(t *testing.T) {
	res := engine.Result{
		Phase:       engine.PhaseSucceeded,
		Summary:     "ok",
		ModelConfig: "openai",
		Steps: []engine.StepResult{
			{Name: "s", Outcome: engine.OutcomeCompleted, Outputs: map[string]any{"phase": "Running"}},
		},
	}
	start := time.Unix(100, 0)
	end := time.Unix(200, 0)
	st, err := BuildRunStatus(res, start, end)
	if err != nil {
		t.Fatalf("BuildRunStatus: %v", err)
	}
	if st.Phase != engine.PhaseSucceeded || st.Summary != "ok" || st.ModelConfig != "openai" {
		t.Errorf("status = %+v", st)
	}
	if len(st.Steps) != 1 || st.Steps[0].Outputs == nil {
		t.Errorf("steps = %+v", st.Steps)
	}
	if st.StartedAt == nil || !st.StartedAt.Time.Equal(start) {
		t.Errorf("startedAt = %v, want %v", st.StartedAt, start)
	}
}

func TestSaveRunSetsManagedByLabel(t *testing.T) {
	c := newFakeClient(t)
	s := &Store{Client: c, Namespace: "kato"}
	run, err := s.SaveRun(context.Background(), "pod-crashloop", nil,
		engine.Result{Phase: engine.PhaseSucceeded}, time.Unix(1, 0), time.Unix(2, 0))
	if err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	if run.Labels[v1alpha1.ManagedByLabel] != v1alpha1.ManagedByAPI {
		t.Errorf("managed-by label = %q, want %q", run.Labels[v1alpha1.ManagedByLabel], v1alpha1.ManagedByAPI)
	}
}

func TestBuildRunStatusCarriesVerdict(t *testing.T) {
	healthy := false
	res := engine.Result{
		Phase:    engine.PhaseSucceeded,
		Summary:  "pods crashing",
		Healthy:  &healthy,
		Headline: "CrashLoopBackOff",
	}
	now := time.Now()
	st, err := BuildRunStatus(res, now, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.Healthy == nil || *st.Healthy != false {
		t.Errorf("Healthy = %v, want false", st.Healthy)
	}
	if st.Headline != "CrashLoopBackOff" {
		t.Errorf("Headline = %q, want CrashLoopBackOff", st.Headline)
	}
}

func TestReapStuckRunsFailsOnlyStale(t *testing.T) {
	now := time.Unix(10000, 0)
	stuck := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "stuck", Namespace: "default"},
		Status:     v1alpha1.RunStatus{Phase: engine.PhaseRunning, StartedAt: ptrTime(now.Add(-2 * time.Hour))},
	}
	fresh := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "fresh", Namespace: "default"},
		Status:     v1alpha1.RunStatus{Phase: engine.PhaseRunning, StartedAt: ptrTime(now.Add(-time.Minute))},
	}
	done := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "done", Namespace: "kato"},
		Status:     v1alpha1.RunStatus{Phase: engine.PhaseSucceeded, StartedAt: ptrTime(now.Add(-3 * time.Hour))},
	}
	c := newFakeClient(t, stuck, fresh, done)
	s := &Store{Client: c, Namespace: "kato", TTL: time.Hour}

	n, err := s.ReapStuckRuns(context.Background(), now, time.Hour)
	if err != nil {
		t.Fatalf("ReapStuckRuns: %v", err)
	}
	if n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	var got v1alpha1.Run
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "stuck"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != engine.PhaseFailed {
		t.Errorf("stuck phase = %q, want Failed", got.Status.Phase)
	}
	if got.Status.Note == "" {
		t.Error("stuck Note is empty")
	}
	var stillFresh v1alpha1.Run
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "fresh"}, &stillFresh); err != nil {
		t.Fatal(err)
	}
	if stillFresh.Status.Phase != engine.PhaseRunning {
		t.Errorf("fresh phase = %q, want Running (untouched)", stillFresh.Status.Phase)
	}
}
