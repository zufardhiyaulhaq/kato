package methods

import (
	"fmt"
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
