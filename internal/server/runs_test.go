package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gopaytech/kato/api/v1alpha1"
)

func TestListAndGetRuns(t *testing.T) {
	s := testServer(sampleUseCase(), true)
	fr := s.Runs.(*fakeRuns)
	fr.list = []*v1alpha1.Run{{ObjectMeta: metav1.ObjectMeta{Name: "pod-crashloop-1"}}}
	fr.byName = map[string]*v1alpha1.Run{"pod-crashloop-1": fr.list[0]}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs?usecase=pod-crashloop", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "pod-crashloop-1") {
		t.Errorf("list runs: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/runs/missing", nil)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing run: %d", w.Code)
	}
}
