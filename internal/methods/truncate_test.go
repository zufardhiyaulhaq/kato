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

func TestClampLineLengthBasics(t *testing.T) {
	if got := ClampLineLength("short line", 100); got != "short line" {
		t.Errorf("under cap modified: %q", got)
	}
	long := strings.Repeat("x", 5000)
	if got := ClampLineLength(long, 0); got != long {
		t.Errorf("maxRunes 0 should be unchanged")
	}
	if got := ClampLineLength(long, -1); got != long {
		t.Errorf("negative maxRunes should be unchanged")
	}
	got := ClampLineLength(strings.Repeat("a", 1010), 1000)
	want := strings.Repeat("a", 1000) + "…[+10 chars]"
	if got != want {
		t.Errorf("trim = %q, want %q", got, want)
	}
}

func TestClampLineLengthMultiline(t *testing.T) {
	in := "ok\n" + strings.Repeat("b", 12) + "\nfine"
	got := ClampLineLength(in, 5)
	want := "ok\nbbbbb…[+7 chars]\nfine"
	if got != want {
		t.Errorf("multiline = %q, want %q", got, want)
	}
	if n := strings.Count(got, "\n"); n != 2 {
		t.Errorf("line count changed: %d newlines", n)
	}
}

func TestClampLineLengthMultibyte(t *testing.T) {
	// 6 Japanese runes capped at 3: keep 3 runes + marker, never split a rune.
	got := ClampLineLength("日本語日本語", 3)
	want := "日本語…[+3 chars]"
	if got != want {
		t.Errorf("multibyte = %q, want %q", got, want)
	}
}
