// Package controller hosts the reconcilers that validate UseCase/ModelConfig
// CRs (setting Ready conditions, spec §4), the in-memory caches the API reads,
// and ModelConfig->Completer resolution.
package controller

import (
	"sort"
	"sync"

	"github.com/gopaytech/kato/api/v1alpha1"
)

// UseCaseCache is the API's read side: name -> (use case, ready). Thread-safe.
type UseCaseCache struct {
	mu    sync.RWMutex
	items map[string]*v1alpha1.UseCase
	ready map[string]bool
}

func NewUseCaseCache() *UseCaseCache {
	return &UseCaseCache{items: map[string]*v1alpha1.UseCase{}, ready: map[string]bool{}}
}

func (c *UseCaseCache) Set(uc *v1alpha1.UseCase, ready bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[uc.Name] = uc.DeepCopy()
	c.ready[uc.Name] = ready
}

func (c *UseCaseCache) Delete(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, name)
	delete(c.ready, name)
}

func (c *UseCaseCache) GetUseCase(name string) (*v1alpha1.UseCase, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	uc, ok := c.items[name]
	return uc, ok
}

func (c *UseCaseCache) IsReady(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ready[name]
}

func (c *UseCaseCache) ListUseCases() []*v1alpha1.UseCase {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*v1alpha1.UseCase, 0, len(c.items))
	for _, uc := range c.items {
		out = append(out, uc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
