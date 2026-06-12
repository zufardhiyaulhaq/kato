package methods

import (
	"strings"
	"testing"
)

func TestTruncateKeepsHeadAndTail(t *testing.T) {
	long := strings.Repeat("a", 500) + "MIDDLE" + strings.Repeat("z", 500)
	got := Truncate(long, 100)
	if len(got) > 150 { // 100 bytes content + marker
		t.Fatalf("too long: %d", len(got))
	}
	if !strings.HasPrefix(got, "aaaaa") || !strings.HasSuffix(got, "zzzzz") {
		t.Fatalf("head/tail not preserved: %q", got)
	}
	if !strings.Contains(got, "[... truncated ...]") {
		t.Fatalf("missing truncation marker: %q", got)
	}
}

func TestTruncateShortStringUnchanged(t *testing.T) {
	if got := Truncate("short", 100); got != "short" {
		t.Fatalf("short string modified: %q", got)
	}
}
