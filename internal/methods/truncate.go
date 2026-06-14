package methods

import (
	"fmt"
	"strconv"
	"strings"
)

// Truncate caps s at max bytes, keeping the head and tail halves with a
// marker in between. Used for large string outputs like logs (spec §4).
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := max / 2
	return s[:half] + "\n[... truncated ...]\n" + s[len(s)-half:]
}

// defaultMaxListItems bounds a method's list output so a workload with very many matching
// pods cannot overflow the Run CR (etcd ~1.5MB) or the LLM evidence. It is set above
// the forEach iteration ceiling (engine.maxItemsCeiling = 20) so a bounded forEach is
// never starved of items to examine.
const defaultMaxListItems = 50

// parseMaxListItems reads the maxListItems param: unset -> defaultMaxListItems, "0" ->
// 0 (unlimited; capItems treats max <= 0 as no cap), a positive integer -> that cap.
// A negative or non-integer value is a param error.
func parseMaxListItems(params map[string]string) (int, error) {
	v := params["maxListItems"]
	if v == "" {
		return defaultMaxListItems, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("param maxListItems: %w", err)
	}
	if n < 0 {
		return 0, fmt.Errorf("param maxListItems: must be >= 0, got %d", n)
	}
	return n, nil
}

// capItems returns at most max items (the first max, preserving caller ordering) and
// whether truncation occurred. max <= 0 returns items unchanged. Callers sort
// worst-first before capping so the surviving items are the most important.
func capItems(items []map[string]any, max int) ([]map[string]any, bool) {
	if max <= 0 || len(items) <= max {
		return items, false
	}
	out := make([]map[string]any, max)
	copy(out, items[:max])
	return out, true
}

// ClampLineLength trims each line of s to at most maxRunes characters, appending
// a "…[+N chars]" marker to any line it shortened (N = runes dropped from that
// line). maxRunes <= 0 returns s unchanged. Counts runes (UTF-8 safe), so a
// multibyte character is never split.
func ClampLineLength(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		r := []rune(line)
		if len(r) > maxRunes {
			lines[i] = fmt.Sprintf("%s…[+%d chars]", string(r[:maxRunes]), len(r)-maxRunes)
		}
	}
	return strings.Join(lines, "\n")
}
