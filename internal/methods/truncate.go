package methods

// Truncate caps s at max bytes, keeping the head and tail halves with a
// marker in between. Used for large string outputs like logs (spec §4).
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := max / 2
	return s[:half] + "\n[... truncated ...]\n" + s[len(s)-half:]
}
