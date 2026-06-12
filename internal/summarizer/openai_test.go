package summarizer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIClientSummarize(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "diagnosis: OOM"}}},
		})
	}))
	defer srv.Close()

	c := &OpenAIClient{
		BaseURL: srv.URL, Model: "qwen3", APIKey: "sk-test",
		MaxTokens: 100, Temperature: 0, HTTPClient: srv.Client(),
	}
	out, err := c.Complete(context.Background(), "system prompt", "user evidence")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != "diagnosis: OOM" {
		t.Errorf("out = %q", out)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth = %q", gotAuth)
	}
	if !strings.Contains(gotBody, "qwen3") || !strings.Contains(gotBody, "user evidence") {
		t.Errorf("body = %q", gotBody)
	}
}

func TestOpenAIClientNoAPIKeyOmitsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "ok"}}},
		})
	}))
	defer srv.Close()
	c := &OpenAIClient{BaseURL: srv.URL, Model: "llama", HTTPClient: srv.Client()}
	if _, err := c.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("expected no auth header, got %q", gotAuth)
	}
}

func TestOpenAIClientHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()
	c := &OpenAIClient{BaseURL: srv.URL, Model: "x", HTTPClient: srv.Client()}
	if _, err := c.Complete(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected error on non-200")
	}
}
