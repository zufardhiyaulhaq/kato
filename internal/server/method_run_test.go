package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

// fakeMethod is a scriptable methods.Method.
type fakeMethod struct {
	name    string
	params  []methods.Param
	outputs []methods.OutputField
	run     func(ctx context.Context, deps methods.Deps, params map[string]string) (methods.Outputs, error)
}

func (f *fakeMethod) Name() string                        { return f.name }
func (f *fakeMethod) Description() string                 { return "fake " + f.name }
func (f *fakeMethod) Params() []methods.Param             { return f.params }
func (f *fakeMethod) OutputFields() []methods.OutputField { return f.outputs }
func (f *fakeMethod) Run(ctx context.Context, deps methods.Deps, p map[string]string) (methods.Outputs, error) {
	return f.run(ctx, deps, p)
}

// newMethodServer builds a Server with only the given fakes registered.
func newMethodServer(t *testing.T, maxConcurrent int, fakes ...*fakeMethod) http.Handler {
	t.Helper()
	reg := methods.NewRegistry()
	for _, f := range fakes {
		reg.Register(f)
	}
	s := &Server{
		Registry:            reg,
		MethodMaxConcurrent: maxConcurrent,
		StepTimeout:         time.Second,
	}
	return s.Handler()
}

func postMethodRun(h http.Handler, method, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(http.MethodPost, "/api/v1/methods/"+method+"/run", nil)
	} else {
		r = httptest.NewRequest(http.MethodPost, "/api/v1/methods/"+method+"/run", strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Success: params reach the method; scalar and list outputs come back under
// "outputs" with outcome "completed".
func TestRunMethod_Success(t *testing.T) {
	var gotParams map[string]string
	fake := &fakeMethod{
		name:   "pod_status",
		params: []methods.Param{{Name: "namespace", Required: true}, {Name: "pod", Required: true}},
		run: func(_ context.Context, _ methods.Deps, p map[string]string) (methods.Outputs, error) {
			gotParams = p
			return methods.Outputs{
				"phase":    "Running",
				"restarts": int64(3),
				"pods":     []map[string]any{{"name": "api-0"}},
			}, nil
		},
	}
	w := postMethodRun(newMethodServer(t, 4, fake),
		"pod_status", `{"params":{"namespace":"payments","pod":"api-0"}}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if gotParams["namespace"] != "payments" || gotParams["pod"] != "api-0" {
		t.Errorf("params = %v, want namespace=payments pod=api-0", gotParams)
	}
	var resp struct {
		Outcome string         `json:"outcome"`
		Outputs map[string]any `json:"outputs"`
		Error   string         `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Outcome != "completed" || resp.Error != "" {
		t.Errorf("outcome = %q error = %q, want completed/empty", resp.Outcome, resp.Error)
	}
	if resp.Outputs["phase"] != "Running" {
		t.Errorf("outputs.phase = %v, want Running", resp.Outputs["phase"])
	}
	if _, ok := resp.Outputs["pods"].([]any); !ok {
		t.Errorf("outputs.pods = %T, want a JSON array", resp.Outputs["pods"])
	}
}

// A method-level failure is a finding, not an HTTP error: 200 + outcome failed.
func TestRunMethod_MethodFailureIs200(t *testing.T) {
	fake := &fakeMethod{
		name: "pod_status",
		run: func(context.Context, methods.Deps, map[string]string) (methods.Outputs, error) {
			return nil, context.DeadlineExceeded // any error; also covers nil outputs
		},
	}
	w := postMethodRun(newMethodServer(t, 4, fake), "pod_status", `{"params":{}}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Outcome string         `json:"outcome"`
		Outputs map[string]any `json:"outputs"`
		Error   string         `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Outcome != "failed" || resp.Error == "" {
		t.Errorf("outcome = %q error = %q, want failed + non-empty error", resp.Outcome, resp.Error)
	}
	if resp.Outputs == nil {
		t.Errorf(`outputs missing; want "outputs": {} even on failure`)
	}
}

// Caller mistakes: unknown method 404; missing required / unknown param /
// invalid JSON 400. The method must never execute on a 4xx.
func TestRunMethod_CallerErrors(t *testing.T) {
	executed := false
	fake := &fakeMethod{
		name:   "pod_status",
		params: []methods.Param{{Name: "namespace", Required: true}},
		run: func(context.Context, methods.Deps, map[string]string) (methods.Outputs, error) {
			executed = true
			return methods.Outputs{}, nil
		},
	}
	h := newMethodServer(t, 4, fake)

	cases := []struct {
		name, method, body string
		wantStatus         int
	}{
		{"unknown method", "nope", `{"params":{}}`, http.StatusNotFound},
		{"missing required param", "pod_status", `{"params":{}}`, http.StatusBadRequest},
		{"unknown param", "pod_status", `{"params":{"namespace":"x","bogus":"y"}}`, http.StatusBadRequest},
		{"invalid JSON", "pod_status", `{not json`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postMethodRun(h, tc.method, tc.body)
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", w.Code, tc.wantStatus, w.Body.String())
			}
			var e struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Error == "" {
				t.Errorf(`body = %s, want {"error": "..."}`, w.Body.String())
			}
		})
	}
	if executed {
		t.Error("method executed on a 4xx request; validation must run first")
	}
}

// An empty/missing body means empty params (methods with zero params are common).
func TestRunMethod_EmptyBodyIsEmptyParams(t *testing.T) {
	var gotParams map[string]string
	fake := &fakeMethod{
		name: "check_apiserver",
		run: func(_ context.Context, _ methods.Deps, p map[string]string) (methods.Outputs, error) {
			gotParams = p
			return methods.Outputs{"healthy": true}, nil
		},
	}
	w := postMethodRun(newMethodServer(t, 4, fake), "check_apiserver", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if gotParams == nil || len(gotParams) != 0 {
		t.Errorf("params = %v, want empty non-nil map", gotParams)
	}
}

// The dedicated limiter: cap 1, one in-flight run -> second call 429.
// The usecase semaphore is untouched.
func TestRunMethod_LimiterFull(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	fake := &fakeMethod{
		name: "slow",
		run: func(context.Context, methods.Deps, map[string]string) (methods.Outputs, error) {
			started <- struct{}{}
			<-release
			return methods.Outputs{}, nil
		},
	}
	h := newMethodServer(t, 1, fake)

	firstDone := make(chan *httptest.ResponseRecorder)
	go func() { firstDone <- postMethodRun(h, "slow", "") }()
	<-started // first request is now inside the method, holding the semaphore

	w := postMethodRun(h, "slow", "")
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("second call status = %d, want 429; body: %s", w.Code, w.Body.String())
	}

	close(release)
	if first := <-firstDone; first.Code != http.StatusOK {
		t.Errorf("first call status = %d, want 200", first.Code)
	}
}

// Log is the endpoint's only observability hook (spec Non-goals: "kato logs
// the call"): exactly one line per executed run, carrying method + outcome;
// nothing is logged when the request is rejected before the method runs.
func TestRunMethod_Log(t *testing.T) {
	type call struct {
		msg string
		kv  []any
	}

	kvString := func(kv []any, key string) (string, bool) {
		for i := 0; i+1 < len(kv); i += 2 {
			if k, ok := kv[i].(string); ok && k == key {
				v, ok := kv[i+1].(string)
				return v, ok
			}
		}
		return "", false
	}

	t.Run("successful run logs once with outcome completed", func(t *testing.T) {
		var calls []call
		fake := &fakeMethod{
			name: "pod_status",
			run: func(context.Context, methods.Deps, map[string]string) (methods.Outputs, error) {
				return methods.Outputs{"phase": "Running"}, nil
			},
		}
		reg := methods.NewRegistry()
		reg.Register(fake)
		s := &Server{
			Registry: reg, MethodMaxConcurrent: 4, StepTimeout: time.Second,
			Log: func(msg string, kv ...any) { calls = append(calls, call{msg, kv}) },
		}
		w := postMethodRun(s.Handler(), "pod_status", `{"params":{}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
		}
		if len(calls) != 1 {
			t.Fatalf("Log called %d times, want 1: %+v", len(calls), calls)
		}
		if method, ok := kvString(calls[0].kv, "method"); !ok || method != "pod_status" {
			t.Errorf(`kv["method"] = %v, want "pod_status"`, calls[0].kv)
		}
		if outcome, ok := kvString(calls[0].kv, "outcome"); !ok || outcome != "completed" {
			t.Errorf(`kv["outcome"] = %v, want "completed"`, calls[0].kv)
		}
	})

	t.Run("failed run logs once with outcome failed", func(t *testing.T) {
		var calls []call
		fake := &fakeMethod{
			name: "pod_status",
			run: func(context.Context, methods.Deps, map[string]string) (methods.Outputs, error) {
				return nil, context.DeadlineExceeded
			},
		}
		reg := methods.NewRegistry()
		reg.Register(fake)
		s := &Server{
			Registry: reg, MethodMaxConcurrent: 4, StepTimeout: time.Second,
			Log: func(msg string, kv ...any) { calls = append(calls, call{msg, kv}) },
		}
		w := postMethodRun(s.Handler(), "pod_status", `{"params":{}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
		}
		if len(calls) != 1 {
			t.Fatalf("Log called %d times, want 1: %+v", len(calls), calls)
		}
		if outcome, ok := kvString(calls[0].kv, "outcome"); !ok || outcome != "failed" {
			t.Errorf(`kv["outcome"] = %v, want "failed"`, calls[0].kv)
		}
	})

	t.Run("caller error (4xx) does not log", func(t *testing.T) {
		var calls []call
		reg := methods.NewRegistry()
		s := &Server{
			Registry: reg, MethodMaxConcurrent: 4, StepTimeout: time.Second,
			Log: func(msg string, kv ...any) { calls = append(calls, call{msg, kv}) },
		}
		w := postMethodRun(s.Handler(), "nope", `{"params":{}}`)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
		}
		if len(calls) != 0 {
			t.Errorf("Log called %d times on a 4xx, want 0: %+v", len(calls), calls)
		}
	})
}

// A method exceeding StepTimeout surfaces as outcome failed (deadline error),
// still HTTP 200.
func TestRunMethod_Timeout(t *testing.T) {
	fake := &fakeMethod{
		name: "sleepy",
		run: func(ctx context.Context, _ methods.Deps, _ map[string]string) (methods.Outputs, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return methods.Outputs{}, nil
			}
		},
	}
	reg := methods.NewRegistry()
	reg.Register(fake)
	s := &Server{Registry: reg, MethodMaxConcurrent: 1, StepTimeout: 30 * time.Millisecond}
	w := postMethodRun(s.Handler(), "sleepy", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Outcome string `json:"outcome"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Outcome != "failed" || !strings.Contains(resp.Error, "context deadline exceeded") {
		t.Errorf("outcome = %q error = %q, want failed + deadline error", resp.Outcome, resp.Error)
	}
}
