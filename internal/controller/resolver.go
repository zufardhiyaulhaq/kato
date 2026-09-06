package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gopaytech/kato/api/v1alpha1"
	"github.com/gopaytech/kato/internal/summarizer"
)

// ModelConfigCache stores ModelConfig CRs and resolves them to Completers.
// APIKeyLookup fetches the API key from a Secret (nil in tests). Thread-safe.
type ModelConfigCache struct {
	mu           sync.RWMutex
	items        map[string]*v1alpha1.ModelConfig
	APIKeyLookup func(ctx context.Context, name, key string) (string, error)
	// LLMTimeout caps each completion request; 0 -> the client's default.
	LLMTimeout time.Duration
}

func NewModelConfigCache() *ModelConfigCache {
	return &ModelConfigCache{items: map[string]*v1alpha1.ModelConfig{}}
}

func (c *ModelConfigCache) Set(m *v1alpha1.ModelConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[m.Name] = m.DeepCopy()
}

func (c *ModelConfigCache) Delete(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, name)
}

func (c *ModelConfigCache) Exists(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.items[name]
	return ok
}

// summarizerClient adapts OpenAIClient to summarizer.Completer and exposes the
// underlying client for tests.
type summarizerClient struct {
	client *summarizer.OpenAIClient
}

func (s *summarizerClient) Complete(ctx context.Context, system, user string) (string, error) {
	return s.client.Complete(ctx, system, user)
}

// Resolve implements summarizer's resolution rules (spec §4): explicit ref,
// else the default; multiple defaults -> lexicographically-first; none -> error.
func (c *ModelConfigCache) Resolve(uc *v1alpha1.UseCase) (summarizer.Completer, string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var chosen *v1alpha1.ModelConfig
	if ref := uc.Spec.Summary.ModelConfigRef; ref != "" {
		m, ok := c.items[ref]
		if !ok {
			return nil, "", fmt.Errorf("modelConfigRef %q not found", ref)
		}
		chosen = m
	} else {
		names := make([]string, 0, len(c.items))
		for n, m := range c.items {
			if m.Spec.Default {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			return nil, "", fmt.Errorf("no default ModelConfig and no modelConfigRef set")
		}
		sort.Strings(names)
		chosen = c.items[names[0]]
	}

	temp := 0.0
	if chosen.Spec.Temperature != "" {
		t, err := strconv.ParseFloat(chosen.Spec.Temperature, 64)
		if err != nil {
			return nil, "", fmt.Errorf("modelConfig %q: invalid temperature: %w", chosen.Name, err)
		}
		temp = t
	}
	apiKey := ""
	if ref := chosen.Spec.APIKeySecretRef; ref != nil && c.APIKeyLookup != nil {
		k, err := c.APIKeyLookup(context.Background(), ref.Name, ref.Key)
		if err != nil {
			return nil, "", fmt.Errorf("modelConfig %q: read api key: %w", chosen.Name, err)
		}
		apiKey = k
	}
	return &summarizerClient{client: &summarizer.OpenAIClient{
		BaseURL: chosen.Spec.BaseURL, Model: chosen.Spec.Model, APIKey: apiKey,
		MaxTokens: chosen.Spec.MaxTokens, Temperature: temp, Timeout: c.LLMTimeout,
	}}, chosen.Name, nil
}
