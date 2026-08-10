// internal/summarizer/verdict_test.go
package summarizer

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func boolPtr(b bool) *bool { return &b }

func TestParseVerdict(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantHealthy  *bool
		wantHeadline string
		wantSummary  string
	}{
		{
			name:         "healthy",
			raw:          "VERDICT: healthy — all replicas ready\n\nThe deployment is fine.",
			wantHealthy:  boolPtr(true),
			wantHeadline: "all replicas ready",
			wantSummary:  "The deployment is fine.",
		},
		{
			name:         "unhealthy with em dash",
			raw:          "VERDICT: unhealthy — CrashLoopBackOff, bad image tag\n\nPods are crashing.",
			wantHealthy:  boolPtr(false),
			wantHeadline: "CrashLoopBackOff, bad image tag",
			wantSummary:  "Pods are crashing.",
		},
		{
			name:         "unhealthy with hyphen separator",
			raw:          "VERDICT: unhealthy - OOMKilled\n\nRaise the memory limit.",
			wantHealthy:  boolPtr(false),
			wantHeadline: "OOMKilled",
			wantSummary:  "Raise the memory limit.",
		},
		{
			name:         "unknown keyword yields nil healthy",
			raw:          "VERDICT: unknown — evidence inconclusive\n\nNot enough data.",
			wantHealthy:  nil,
			wantHeadline: "evidence inconclusive",
			wantSummary:  "Not enough data.",
		},
		{
			name:         "case insensitive",
			raw:          "verdict: HEALTHY — ok\n\nprose",
			wantHealthy:  boolPtr(true),
			wantHeadline: "ok",
			wantSummary:  "prose",
		},
		{
			name:         "leading blank lines before verdict",
			raw:          "\n\nVERDICT: healthy — ok\n\nprose",
			wantHealthy:  boolPtr(true),
			wantHeadline: "ok",
			wantSummary:  "prose",
		},
		{
			name:         "no verdict line leaves prose untouched",
			raw:          "The deployment is fine, nothing to report.",
			wantHealthy:  nil,
			wantHeadline: "",
			wantSummary:  "The deployment is fine, nothing to report.",
		},
		{
			name:         "malformed verdict value treated as no match",
			raw:          "VERDICT: maybe — who knows\n\nprose",
			wantHealthy:  nil,
			wantHeadline: "",
			wantSummary:  "VERDICT: maybe — who knows\n\nprose",
		},
		{
			name:         "missing headline",
			raw:          "VERDICT: healthy\n\nprose",
			wantHealthy:  boolPtr(true),
			wantHeadline: "",
			wantSummary:  "prose",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, healthy, headline := parseVerdict(tt.raw)
			if !eqBoolPtr(healthy, tt.wantHealthy) {
				t.Errorf("healthy = %v, want %v", healthy, tt.wantHealthy)
			}
			if headline != tt.wantHeadline {
				t.Errorf("headline = %q, want %q", headline, tt.wantHeadline)
			}
			if summary != tt.wantSummary {
				t.Errorf("summary = %q, want %q", summary, tt.wantSummary)
			}
		})
	}
}

func TestParseVerdictTruncatesHeadline(t *testing.T) {
	// Use a multibyte rune so this also proves the truncation is rune-safe
	// (never splits a rune mid-bytes), not merely correct for ASCII.
	long := strings.Repeat("界", 200)
	_, _, headline := parseVerdict("VERDICT: unhealthy — " + long + "\n\nprose")
	if n := len([]rune(headline)); n != 120 {
		t.Fatalf("headline rune count = %d, want 120 (119 runes + ellipsis)", n)
	}
	if !utf8.ValidString(headline) {
		t.Errorf("headline is not valid UTF-8 (a rune was split): %q", headline)
	}
	if headline[len(headline)-len("…"):] != "…" {
		t.Errorf("headline should end with ellipsis, got %q", headline)
	}
}

func eqBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
