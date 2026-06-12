// Package store persists each flow execution as a Run CR (audit record) and
// garbage-collects expired Runs by TTL (spec §4).
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
	"github.com/zufardhiyaulhaq/kato/internal/engine"
)

const usecaseLabel = "kato.zufardhiyaulhaq.com/usecase"

type Store struct {
	Client    client.Client
	Namespace string
	TTL       time.Duration
}

// SaveRun creates a Run with spec, then writes status via the status
// subresource. Returns the created Run.
func (s *Store) SaveRun(ctx context.Context, useCase string, inputs map[string]string,
	res engine.Result, startedAt, completedAt time.Time) (*v1alpha1.Run, error) {

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: useCase + "-",
			Namespace:    s.Namespace,
			Labels:       map[string]string{usecaseLabel: useCase},
		},
		Spec: v1alpha1.RunSpec{UseCase: useCase, Inputs: inputs},
	}
	if err := s.Client.Create(ctx, run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}

	steps := make([]v1alpha1.RunStep, 0, len(res.Steps))
	for _, sr := range res.Steps {
		rs := v1alpha1.RunStep{Name: sr.Name, Outcome: sr.Outcome, Reason: sr.Reason, Error: sr.Error}
		if len(sr.Outputs) > 0 {
			raw, err := json.Marshal(sr.Outputs)
			if err != nil {
				return nil, fmt.Errorf("marshal outputs for step %s: %w", sr.Name, err)
			}
			rs.Outputs = &apiextensionsv1.JSON{Raw: raw}
		}
		steps = append(steps, rs)
	}
	started := metav1.NewTime(startedAt)
	completed := metav1.NewTime(completedAt)
	run.Status = v1alpha1.RunStatus{
		Phase:       res.Phase,
		StartedAt:   &started,
		CompletedAt: &completed,
		Steps:       steps,
		Summary:     res.Summary,
		Warning:     res.Warning,
		ModelConfig: res.ModelConfig,
	}
	if err := s.Client.Status().Update(ctx, run); err != nil {
		return nil, fmt.Errorf("update run status: %w", err)
	}
	return run, nil
}

// GetRun retrieves a Run by name from kato's namespace. Returns (nil, false, nil) if not found.
func (s *Store) GetRun(ctx context.Context, name string) (*v1alpha1.Run, bool, error) {
	var run v1alpha1.Run
	err := s.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &run)
	if errors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &run, true, nil
}

// ListRuns lists Runs in kato's namespace, optionally filtered by usecase label.
func (s *Store) ListRuns(ctx context.Context, useCase string) ([]*v1alpha1.Run, error) {
	var list v1alpha1.RunList
	opts := []client.ListOption{client.InNamespace(s.Namespace)}
	if useCase != "" {
		opts = append(opts, client.MatchingLabels{usecaseLabel: useCase})
	}
	if err := s.Client.List(ctx, &list, opts...); err != nil {
		return nil, err
	}
	out := make([]*v1alpha1.Run, len(list.Items))
	for i := range list.Items {
		out[i] = &list.Items[i]
	}
	// Newest first, so the audit history reads in reverse-chronological order.
	sort.Slice(out, func(i, j int) bool {
		return out[j].CreationTimestamp.Before(&out[i].CreationTimestamp)
	})
	return out, nil
}

// GarbageCollect deletes Runs older than TTL relative to now. Returns the
// number deleted.
func (s *Store) GarbageCollect(ctx context.Context, now time.Time) (int, error) {
	var list v1alpha1.RunList
	if err := s.Client.List(ctx, &list, client.InNamespace(s.Namespace)); err != nil {
		return 0, fmt.Errorf("list runs: %w", err)
	}
	deleted := 0
	for i := range list.Items {
		run := &list.Items[i]
		if now.Sub(run.CreationTimestamp.Time) > s.TTL {
			if err := s.Client.Delete(ctx, run); err != nil {
				return deleted, fmt.Errorf("delete run %s: %w", run.Name, err)
			}
			deleted++
		}
	}
	return deleted, nil
}
