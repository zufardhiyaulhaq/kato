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

func TestParseMaxListItems(t *testing.T) {
	cases := []struct {
		name    string
		params  map[string]string
		want    int
		wantErr bool
	}{
		{"unset", map[string]string{}, 50, false},
		{"explicit", map[string]string{"maxListItems": "10"}, 10, false},
		{"zero unlimited", map[string]string{"maxListItems": "0"}, 0, false},
		{"non-integer", map[string]string{"maxListItems": "abc"}, 0, true},
		{"negative", map[string]string{"maxListItems": "-1"}, 0, true},
	}
	for _, tc := range cases {
		got, err := parseMaxListItems(tc.params)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected error, got nil", tc.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestCapItems(t *testing.T) {
	mk := func(n int) []map[string]any {
		out := make([]map[string]any, n)
		for i := range out {
			out[i] = map[string]any{"i": int64(i)}
		}
		return out
	}

	// Under the cap: unchanged, not truncated.
	got, truncated := capItems(mk(3), 5)
	if len(got) != 3 || truncated {
		t.Errorf("under cap: len=%d truncated=%v, want 3,false", len(got), truncated)
	}

	// Exactly at the cap: unchanged, not truncated.
	got, truncated = capItems(mk(5), 5)
	if len(got) != 5 || truncated {
		t.Errorf("at cap: len=%d truncated=%v, want 5,false", len(got), truncated)
	}

	// Over the cap: first `max` kept (order preserved), truncated.
	got, truncated = capItems(mk(9), 5)
	if len(got) != 5 || !truncated {
		t.Fatalf("over cap: len=%d truncated=%v, want 5,true", len(got), truncated)
	}
	if got[0]["i"] != int64(0) || got[4]["i"] != int64(4) {
		t.Errorf("over cap kept wrong items / order: %v", got)
	}

	// max <= 0 disables capping.
	got, truncated = capItems(mk(7), 0)
	if len(got) != 7 || truncated {
		t.Errorf("max<=0: len=%d truncated=%v, want 7,false", len(got), truncated)
	}
}
