package summarizer

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// verdictInstruction is appended to the system prompt so every summary begins
// with a machine-readable verdict line. A one-shot example raises format
// compliance on weaker/local models. \p{Pd} matches any dash punctuation
// (hyphen, en/em dash) as the optional separator.
const verdictInstruction = `Begin your reply with EXACTLY one line in this format, then a blank line, then your analysis:
VERDICT: <healthy|unhealthy|unknown> — <one short reason>
Use "healthy" only if the evidence shows the subject is working, "unhealthy" if it shows a problem, and "unknown" if the evidence is insufficient to decide. Example:

VERDICT: unhealthy — CrashLoopBackOff, image tag :v2 not found

The deployment has 0/3 ready replicas because ...`

// verdictLineRE matches the verdict on a single line: the keyword, then an
// optional dash/colon separator, then the rest of the line as the headline.
var verdictLineRE = regexp.MustCompile(`(?i)^\s*VERDICT:\s*(healthy|unhealthy|unknown)\b[\s\p{Pd}:]*(.*)$`)

const headlineMaxRunes = 120

// parseVerdict extracts a health verdict from the model's first non-empty line
// and returns the prose with that line removed. It is total: a missing or
// malformed line yields healthy=nil, headline="", and the ORIGINAL text
// unchanged. "healthy"->true, "unhealthy"->false, "unknown"/no-match->nil.
func parseVerdict(raw string) (summary string, healthy *bool, headline string) {
	trimmed := strings.TrimLeft(raw, " \t\r\n")
	parts := strings.SplitN(trimmed, "\n", 2)
	first := parts[0]
	m := verdictLineRE.FindStringSubmatch(first)
	if m == nil {
		return raw, nil, ""
	}
	switch strings.ToLower(m[1]) {
	case "healthy":
		t := true
		healthy = &t
	case "unhealthy":
		f := false
		healthy = &f
	}
	headline = strings.TrimSpace(m[2])
	if utf8.RuneCountInString(headline) > headlineMaxRunes {
		r := []rune(headline)
		// Reserve one rune for the ellipsis so the result is at most 120 runes.
		headline = strings.TrimSpace(string(r[:headlineMaxRunes-1])) + "…"
	}
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}
	return strings.TrimLeft(rest, "\r\n"), healthy, headline
}
