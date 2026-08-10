package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/engine"
	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

// --- test doubles ---

type fakeUseCases struct {
	items map[string]*v1alpha1.UseCase
	ready map[string]bool
}

func (f *fakeUseCases) GetUseCase(name string) (*v1alpha1.UseCase, bool) {
	uc, ok := f.items[name]
	return uc, ok
}
func (f *fakeUseCases) IsReady(name string) bool { return f.ready[name] }
func (f *fakeUseCases) ListUseCases() []*v1alpha1.UseCase {
	out := []*v1alpha1.UseCase{}
	for _, uc := range f.items {
		out = append(out, uc)
	}
	return out
}

type fakeRuns struct {
	saved  *v1alpha1.Run
	list   []*v1alpha1.Run
	byName map[string]*v1alpha1.Run
}

func (f *fakeRuns) SaveRun(_ context.Context, useCase string, inputs map[string]string,
	res engine.Result, _, _ time.Time) (*v1alpha1.Run, error) {
	f.saved = &v1alpha1.Run{ObjectMeta: metav1.ObjectMeta{Name: useCase + "-abc"}}
	return f.saved, nil
}

func (f *fakeRuns) GetRun(_ context.Context, name string) (*v1alpha1.Run, bool, error) {
	r, ok := f.byName[name]
	return r, ok, nil
}
func (f *fakeRuns) ListRuns(_ context.Context, _ string) ([]*v1alpha1.Run, error) {
	return f.list, nil
}

func testServer(uc *v1alpha1.UseCase, ready bool) *Server {
	src := &fakeUseCases{
		items: map[string]*v1alpha1.UseCase{uc.Name: uc},
		ready: map[string]bool{uc.Name: ready},
	}
	exec := func(ctx context.Context, u *v1alpha1.UseCase, inputs map[string]string) (engine.Result, error) {
		// minimal echo executor; real engine tested in Task 10
		if _, ok := inputs["namespace"]; !ok {
			return engine.Result{}, &engine.InputError{Msg: "missing required input \"namespace\""}
		}
		return engine.Result{Phase: "Succeeded", Summary: "ok"}, nil
	}
	return &Server{
		UseCases: src, Runs: &fakeRuns{}, Execute: exec,
		Registry: methods.Builtin(), MaxConcurrent: 10, Clock: func() time.Time { return time.Unix(0, 0) },
	}
}

func sampleUseCase() *v1alpha1.UseCase {
	return &v1alpha1.UseCase{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-crashloop"},
		Spec: v1alpha1.UseCaseSpec{
			Description: "diagnose crashloop",
			Inputs:      []v1alpha1.InputDecl{{Name: "namespace", Required: true}},
			Steps:       []v1alpha1.Step{{Name: "s", Method: "check_pod_status"}},
			Summary:     v1alpha1.SummarySpec{Prompt: "x"},
		},
	}
}

func TestRunEndpointSuccess(t *testing.T) {
	s := testServer(sampleUseCase(), true)
	body := strings.NewReader(`{"inputs":{"namespace":"payments"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/usecases/pod-crashloop/run", body)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["phase"] != "Succeeded" || resp["summary"] != "ok" {
		t.Errorf("resp = %v", resp)
	}
	if resp["run"] != "pod-crashloop-abc" {
		t.Errorf("run name missing: %v", resp)
	}
}

func TestRunUseCaseResponseIncludesVerdict(t *testing.T) {
	uc := sampleUseCase()
	healthy := false
	src := &fakeUseCases{
		items: map[string]*v1alpha1.UseCase{uc.Name: uc},
		ready: map[string]bool{uc.Name: true},
	}
	exec := func(ctx context.Context, u *v1alpha1.UseCase, inputs map[string]string) (engine.Result, error) {
		return engine.Result{Phase: engine.PhaseSucceeded, Summary: "s", Healthy: &healthy, Headline: "CrashLoopBackOff"}, nil
	}
	s := &Server{
		UseCases: src, Runs: &fakeRuns{}, Execute: exec,
		Registry: methods.Builtin(), MaxConcurrent: 10, Clock: func() time.Time { return time.Unix(0, 0) },
	}

	body := strings.NewReader(`{"inputs":{"namespace":"payments"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/usecases/pod-crashloop/run", body)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Phase    string `json:"phase"`
		Healthy  *bool  `json:"healthy"`
		Headline string `json:"headline"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Healthy == nil || *resp.Healthy != false {
		t.Errorf("healthy = %v, want false", resp.Healthy)
	}
	if resp.Headline != "CrashLoopBackOff" {
		t.Errorf("headline = %q, want CrashLoopBackOff", resp.Headline)
	}
}

func TestRunEndpointErrors(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		body       string
		ready      bool
		wantStatus int
	}{
		{"unknown usecase", "/api/v1/usecases/ghost/run", `{"inputs":{}}`, true, http.StatusNotFound},
		{"not ready", "/api/v1/usecases/pod-crashloop/run", `{"inputs":{"namespace":"x"}}`, false, http.StatusUnprocessableEntity},
		{"bad inputs", "/api/v1/usecases/pod-crashloop/run", `{"inputs":{}}`, true, http.StatusBadRequest},
		{"malformed json", "/api/v1/usecases/pod-crashloop/run", `{not json`, true, http.StatusBadRequest},
	}
	for _, tc := range cases {
		s := testServer(sampleUseCase(), tc.ready)
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != tc.wantStatus {
			t.Errorf("%s: status = %d, want %d (body %s)", tc.name, w.Code, tc.wantStatus, w.Body.String())
		}
	}
}

func TestListUseCasesAndMethods(t *testing.T) {
	s := testServer(sampleUseCase(), true)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usecases", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "pod-crashloop") {
		t.Errorf("usecases list: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/methods", nil)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "check_pod_status") {
		t.Errorf("methods list: %d %s", w.Code, w.Body.String())
	}
	// method listing must expose output fields for discoverability (spec §5/§7).
	if !strings.Contains(w.Body.String(), "restartCount") {
		t.Error("methods list should include output fields")
	}
}

func TestHealthEndpoints(t *testing.T) {
	s := testServer(sampleUseCase(), true)
	for _, path := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s = %d", path, w.Code)
		}
	}
}

func TestConcurrencyCap(t *testing.T) {
	s := testServer(sampleUseCase(), true)
	s.MaxConcurrent = 0 // every run should be rejected
	req := httptest.NewRequest(http.MethodPost, "/api/v1/usecases/pod-crashloop/run",
		strings.NewReader(`{"inputs":{"namespace":"x"}}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
}

func TestListMethodsIncludesListOutputs(t *testing.T) {
	s := testServer(sampleUseCase(), true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/methods", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(body, "list_failing_pods") {
		t.Error("list_failing_pods missing from methods")
	}
	// The list output and its item fields must be exposed.
	if !strings.Contains(body, `"pods"`) || !strings.Contains(body, "restartCount") {
		t.Errorf("list output / item fields missing: %s", body)
	}
}
