// Package server exposes the kato REST API (spec §5). All front doors share
// one engine via the Execute func; this is the v1 HTTP front door.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/engine"
	"github.com/zufardhiyaulhaq/kato/internal/methods"
)

// UseCaseSource is the read side of the UseCase cache (Task 14 satisfies it).
type UseCaseSource interface {
	GetUseCase(name string) (*v1alpha1.UseCase, bool)
	IsReady(name string) bool
	ListUseCases() []*v1alpha1.UseCase
}

// RunSource persists Runs and provides read access (store.Store satisfies it).
type RunSource interface {
	SaveRun(ctx context.Context, useCase string, inputs map[string]string,
		res engine.Result, startedAt, completedAt time.Time) (*v1alpha1.Run, error)
	GetRun(ctx context.Context, name string) (*v1alpha1.Run, bool, error)
	ListRuns(ctx context.Context, useCase string) ([]*v1alpha1.Run, error)
}

// ExecuteFunc runs a flow (engine.Engine.Execute satisfies it).
type ExecuteFunc func(ctx context.Context, uc *v1alpha1.UseCase, inputs map[string]string) (engine.Result, error)

type Server struct {
	UseCases      UseCaseSource
	Runs          RunSource
	Execute       ExecuteFunc
	Registry      *methods.Registry
	MaxConcurrent int
	Clock         func() time.Time

	sem chan struct{}
}

func (s *Server) Handler() http.Handler {
	if s.Clock == nil {
		s.Clock = time.Now
	}
	if s.sem == nil {
		s.sem = make(chan struct{}, max(s.MaxConcurrent, 0))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /api/v1/usecases", s.listUseCases)
	mux.HandleFunc("GET /api/v1/usecases/{name}", s.getUseCase)
	mux.HandleFunc("POST /api/v1/usecases/{name}/run", s.runUseCase)
	mux.HandleFunc("GET /api/v1/methods", s.listMethods)
	mux.HandleFunc("GET /api/v1/runs", s.listRuns)
	mux.HandleFunc("GET /api/v1/runs/{name}", s.getRun)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type useCaseView struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Inputs      []v1alpha1.InputDecl `json:"inputs"`
	Ready       bool                 `json:"ready"`
}

func (s *Server) view(uc *v1alpha1.UseCase) useCaseView {
	return useCaseView{
		Name: uc.Name, Description: uc.Spec.Description,
		Inputs: uc.Spec.Inputs, Ready: s.UseCases.IsReady(uc.Name),
	}
}

func (s *Server) listUseCases(w http.ResponseWriter, _ *http.Request) {
	views := []useCaseView{}
	for _, uc := range s.UseCases.ListUseCases() {
		views = append(views, s.view(uc))
	}
	writeJSON(w, 200, map[string]any{"usecases": views})
}

func (s *Server) getUseCase(w http.ResponseWriter, r *http.Request) {
	uc, ok := s.UseCases.GetUseCase(r.PathValue("name"))
	if !ok {
		writeErr(w, http.StatusNotFound, "use case not found")
		return
	}
	writeJSON(w, 200, s.view(uc))
}

type itemFieldView struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

type listOutputView struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	ItemFields  []itemFieldView `json:"itemFields"`
}

type methodView struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Params      []methods.Param       `json:"params"`
	Outputs     []methods.OutputField `json:"outputs"`
	Lists       []listOutputView      `json:"lists,omitempty"`
}

func (s *Server) listMethods(w http.ResponseWriter, _ *http.Request) {
	views := []methodView{}
	for _, m := range s.Registry.All() {
		mv := methodView{
			Name: m.Name(), Description: m.Description(),
			Params: m.Params(), Outputs: m.OutputFields(),
		}
		for _, lo := range methods.ListOutputsOf(m) {
			lv := listOutputView{Name: lo.Name, Description: lo.Description}
			for _, f := range lo.ItemFields {
				lv.ItemFields = append(lv.ItemFields, itemFieldView{
					Name: f.Name, Type: string(f.Type), Description: f.Description,
				})
			}
			mv.Lists = append(mv.Lists, lv)
		}
		views = append(views, mv)
	}
	writeJSON(w, 200, map[string]any{"methods": views})
}

type runRequest struct {
	Inputs map[string]string `json:"inputs"`
}

type runResponse struct {
	Run     string              `json:"run"`
	Phase   string              `json:"phase"`
	Summary string              `json:"summary,omitempty"`
	Warning string              `json:"warning,omitempty"`
	Steps   []engine.StepResult `json:"steps,omitempty"`
}

func (s *Server) runUseCase(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	uc, ok := s.UseCases.GetUseCase(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "use case not found")
		return
	}
	if !s.UseCases.IsReady(name) {
		writeErr(w, http.StatusUnprocessableEntity, "use case is not Ready (failed validation)")
		return
	}

	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Concurrency cap (spec §5): non-blocking acquire.
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		writeErr(w, http.StatusTooManyRequests, "too many concurrent runs")
		return
	}

	started := s.Clock()
	res, err := s.Execute(r.Context(), uc, req.Inputs)
	if err != nil {
		var inputErr *engine.InputError
		if errors.As(err, &inputErr) {
			writeErr(w, http.StatusBadRequest, inputErr.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	completed := s.Clock()

	run, err := s.Runs.SaveRun(r.Context(), name, req.Inputs, res, started, completed)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "run executed but failed to persist: "+err.Error())
		return
	}

	resp := runResponse{
		Run: run.Name, Phase: res.Phase, Summary: res.Summary, Warning: res.Warning,
	}
	if r.URL.Query().Get("includeOutputs") != "false" {
		resp.Steps = res.Steps
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.Runs.ListRuns(r.Context(), r.URL.Query().Get("usecase"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"runs": runs})
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, ok, err := s.Runs.GetRun(r.Context(), r.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, 200, run)
}
