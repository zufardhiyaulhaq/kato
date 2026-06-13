# Per-line Length Cap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cap the length of an individual line in `check_pod_logs` and `check_events` output (default 1000 chars/line, overridable per step) so one giant line can't dominate the result or blow up tokens.

**Architecture:** A new rune-safe `ClampLineLength` helper trims over-long lines with a `…[+N chars]` marker; a shared `parseMaxLineLength` reads the new `maxLineLength` param (default 1000, `0` = unlimited). Both methods clamp before the existing 64 KB `Truncate` backstop.

**Tech Stack:** Go 1.24.8, `k8s.io/client-go/kubernetes/fake` for tests.

**Spec:** `docs/superpowers/specs/2026-06-13-per-line-length-cap-design.md`

> **⚠️ Standing constraint for THIS session:** the user said **don't commit**. Every task ends with a *verification* step (run the suite), **not** a git commit. Leave all changes in the working tree. Do not run `git add` / `git commit`.

---

## File Structure

```
internal/methods/
  truncate.go        (edit) + ClampLineLength helper
  truncate_test.go   (edit) + helper tests
  pod_logs.go        (edit) + defaultMaxLineLength const, parseMaxLineLength, maxLineLength param, clamp in Run
  pod_logs_test.go   (edit) + parseMaxLineLength tests + bad-param test
  events.go          (edit) + maxLineLength param, clamp in Run
  events_test.go     (edit) + end-to-end long-message clamp test
docs/METHOD.md       (edit) + maxLineLength rows for both methods
```

Run order: Task 1 (helper) first — Tasks 2 and 3 depend on it. Task 2 defines `defaultMaxLineLength` and `parseMaxLineLength`, which Task 3 reuses, so do Task 2 before Task 3.

---

### Task 1: `ClampLineLength` helper

**Files:**
- Modify: `internal/methods/truncate.go`
- Test: `internal/methods/truncate_test.go`

- [ ] **Step 1: Write the failing tests** — append to `internal/methods/truncate_test.go`:

```go
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
```

`truncate_test.go` already imports `strings` and `testing`; no import change needed.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/methods/ -run TestClampLineLength -v`
Expected: FAIL — compile error, `undefined: ClampLineLength`.

- [ ] **Step 3: Implement the helper** — replace the entire contents of `internal/methods/truncate.go` with:

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/methods/ -run 'TestClampLineLength|TestTruncate' -v`
Expected: PASS (new clamp tests + existing Truncate tests).

- [ ] **Step 5: Verify the package**

Run: `go test ./internal/methods/ -count=1`
Expected: PASS.

---

### Task 2: `maxLineLength` param on `check_pod_logs`

**Files:**
- Modify: `internal/methods/pod_logs.go`
- Test: `internal/methods/pod_logs_test.go`

- [ ] **Step 1: Write the failing tests** — append to `internal/methods/pod_logs_test.go`:

```go
func TestParseMaxLineLength(t *testing.T) {
	// unset -> default
	n, err := parseMaxLineLength(map[string]string{})
	if err != nil || n != defaultMaxLineLength {
		t.Fatalf("default = %d, %v; want %d", n, err, defaultMaxLineLength)
	}
	// explicit override
	n, err = parseMaxLineLength(map[string]string{"maxLineLength": "200"})
	if err != nil || n != 200 {
		t.Fatalf("override = %d, %v; want 200", n, err)
	}
	// "0" -> unlimited (0)
	n, err = parseMaxLineLength(map[string]string{"maxLineLength": "0"})
	if err != nil || n != 0 {
		t.Fatalf("zero = %d, %v; want 0", n, err)
	}
	// negative -> error
	if _, err := parseMaxLineLength(map[string]string{"maxLineLength": "-5"}); err == nil {
		t.Error("expected error for negative maxLineLength")
	}
	// non-integer -> error
	if _, err := parseMaxLineLength(map[string]string{"maxLineLength": "abc"}); err == nil {
		t.Error("expected error for non-integer maxLineLength")
	}
}

func TestCheckPodLogsBadMaxLineLength(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_pod_logs")
	if _, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "x", "name": "y", "maxLineLength": "abc"}); err == nil {
		t.Fatal("expected error for non-integer maxLineLength")
	}
}
```

(`pod_logs_test.go` already imports `context`, `testing`, `corev1`, `metav1`, `fake` — no import change.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/methods/ -run 'TestParseMaxLineLength|TestCheckPodLogsBadMaxLineLength' -v`
Expected: FAIL — `undefined: parseMaxLineLength` / `undefined: defaultMaxLineLength`.

- [ ] **Step 3: Add the const, parser, param, and clamp** — make three edits to `internal/methods/pod_logs.go`.

(3a) Extend the const block:

```go
const (
	defaultLogBytes      = 64 * 1024
	defaultTailLines     = 10
	defaultMaxLineLength = 1000
)
```

(3b) Add the `maxLineLength` param to `Params()` (append after the `tailLines` entry) and update the `logs` output description in `OutputFields()`:

```go
		{Name: "tailLines", Description: "max lines from the end (integer); defaults to 10"},
		{Name: "maxLineLength", Description: `max characters per line; longer lines are trimmed with a "…[+N chars]" marker (default "1000"; "0" = unlimited)`},
```

```go
		{Name: "logs", Type: FieldString, Description: "log text; per line trimmed to maxLineLength, whole blob truncated head+tail if large"},
```

(3c) Add the parser function (place it right after `buildPodLogOptions`):

```go
// parseMaxLineLength reads the maxLineLength param: unset -> defaultMaxLineLength,
// "0" -> 0 (unlimited), a valid non-negative int -> that value; negative or
// non-integer -> error.
func parseMaxLineLength(params map[string]string) (int, error) {
	v := params["maxLineLength"]
	if v == "" {
		return defaultMaxLineLength, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("param maxLineLength: %w", err)
	}
	if n < 0 {
		return 0, fmt.Errorf("param maxLineLength: must be >= 0, got %d", n)
	}
	return n, nil
}
```

(3d) Apply it in `Run` — replace the body of `check_pod_logs`'s `Run` from the `opts, err := ...` line through the `return` with:

```go
func (checkPodLogs) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	opts, err := buildPodLogOptions(params)
	if err != nil {
		return nil, err
	}
	maxLine, err := parseMaxLineLength(params)
	if err != nil {
		return nil, err
	}
	raw, err := deps.Kube.CoreV1().Pods(params["namespace"]).
		GetLogs(params["name"], opts).DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("get logs %s/%s: %w", params["namespace"], params["name"], err)
	}
	return Outputs{"logs": Truncate(ClampLineLength(string(raw), maxLine), defaultLogBytes)}, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/methods/ -run 'TestParseMaxLineLength|TestCheckPodLogs' -v`
Expected: PASS (new tests + existing `TestCheckPodLogs`, `TestCheckPodLogsBadParams`).

- [ ] **Step 5: Verify the package**

Run: `go test ./internal/methods/ -count=1`
Expected: PASS.

---

### Task 3: `maxLineLength` param on `check_events`

**Files:**
- Modify: `internal/methods/events.go`
- Test: `internal/methods/events_test.go`

Reuses `parseMaxLineLength` and `defaultMaxLineLength` from Task 2 (same package).

- [ ] **Step 1: Write the failing test** — append to `internal/methods/events_test.go`:

```go
func TestCheckEventsClampsLongMessage(t *testing.T) {
	huge := strings.Repeat("z", 5000)
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "big", Namespace: "ns"},
		InvolvedObject: corev1.ObjectReference{Name: "obj", Kind: "Pod"},
		Type:           corev1.EventTypeWarning, Reason: "Boom", Message: huge, Count: 1,
	}
	client := fake.NewSimpleClientset(ev)
	m, _ := Builtin().Get("check_events")

	// Default cap (1000): the rendered line is trimmed with a marker.
	out, err := m.Run(context.Background(), Deps{Kube: client}, map[string]string{"namespace": "ns"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	events := out["events"].(string)
	if !strings.Contains(events, "…[+") {
		t.Errorf("long event message not clamped: %q", events[:min(120, len(events))])
	}
	if strings.Count(events, "z") >= 5000 {
		t.Errorf("full 5000-char message survived the cap")
	}

	// Disabled (maxLineLength "0"): full message survives.
	out, err = m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "ns", "maxLineLength": "0"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Count(out["events"].(string), "z") < 5000 {
		t.Errorf("maxLineLength 0 should leave the message whole")
	}
}
```

(`events_test.go` already imports `context`, `strings`, `testing`, `corev1`, `metav1`, `fake`. `min` is a Go 1.21+ builtin — no import. The events code already uses `max`, confirming builtins are available on this toolchain.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/methods/ -run TestCheckEventsClampsLongMessage -v`
Expected: FAIL — the default-cap assertion fails because the message is not yet clamped (`…[+` absent).

- [ ] **Step 3: Add the param and clamp** — two edits to `internal/methods/events.go`.

(3a) Add the `maxLineLength` param to `Params()` (append after the `limit` entry) and update the `events` output description in `OutputFields()`:

```go
		{Name: "limit", Description: `max event lines to render, warnings first (default "20"; "0" = no limit)`},
		{Name: "maxLineLength", Description: `max characters per rendered line; longer lines are trimmed with a "…[+N chars]" marker (default "1000"; "0" = unlimited)`},
```

```go
		{Name: "events", Type: FieldString, Description: "rendered event lines, warnings first (capped by limit); each line trimmed to maxLineLength"},
```

(3b) In `Run`, parse the param near the top (right after the `limit` parsing block) and apply the clamp to the final string. Replace the existing `limit` parsing block:

```go
	limit := defaultEventLimit
	if v := params["limit"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("param limit: %w", err)
		}
		limit = n
	}
```

with:

```go
	limit := defaultEventLimit
	if v := params["limit"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("param limit: %w", err)
		}
		limit = n
	}
	maxLine, err := parseMaxLineLength(params)
	if err != nil {
		return nil, err
	}
```

and replace the `events` value in the returned `Outputs`:

```go
		"events":       Truncate(b.String(), defaultLogBytes),
```

with:

```go
		"events":       Truncate(ClampLineLength(b.String(), maxLine), defaultLogBytes),
```

> Note: `check_events`'s `Run` already declares `err` earlier (the `list, err :=` line) using `:=`. The added `maxLine, err := parseMaxLineLength(params)` sits **above** that `list, err :=` line, so `err` is first declared here — both use `:=` and compile. If a "no new variables on left side of :=" error appears, change the later `list, err :=` to `list, err =`. (As written, parse-order above keeps `:=` valid.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/methods/ -run 'TestCheckEvents' -v`
Expected: PASS (new clamp test + existing `TestCheckEvents`, `TestCheckEventsLimit`).

- [ ] **Step 5: Verify build + package**

Run: `go build ./... && go test ./internal/methods/ -count=1`
Expected: build OK; package PASS.

---

### Task 4: Docs — `maxLineLength` in METHOD.md

**Files:**
- Modify: `docs/METHOD.md`

- [ ] **Step 1: Add the param to `check_pod_logs`** — in `docs/METHOD.md`, find the `### check_pod_logs` Inputs table and add a row after the `tailLines` row:

```markdown
| `maxLineLength` | no | max characters per line; longer lines are trimmed with a `…[+N chars]` marker (default `1000`; `0` = unlimited) |
```

- [ ] **Step 2: Add the param to `check_events`** — find the `### check_events` Inputs table and add a row after the `limit` row:

```markdown
| `maxLineLength` | no | max characters per rendered line; longer lines are trimmed with a `…[+N chars]` marker (default `1000`; `0` = unlimited) |
```

- [ ] **Step 3: Verify the docs render and nothing else broke**

Run: `grep -c maxLineLength docs/METHOD.md`
Expected: `2`.
Run: `go test ./... -count=1`
Expected: whole repo PASS (no Go changed in this task; this is a final guard).

---

## Self-Review

**Spec coverage:**
- `ClampLineLength` helper (rune-safe, marker, `<=0` passthrough) → Task 1 ✓
- `maxLineLength` param, default 1000, `0` = unlimited, negative/non-int error → Task 2 (`parseMaxLineLength`) ✓, reused in Task 3 ✓
- Applied to `check_pod_logs` (clamp then Truncate) → Task 2 ✓
- Applied to `check_events` (clamp then Truncate) → Task 3 ✓
- Docs (both methods, METHOD.md) → Task 4 ✓; output-field descriptions updated in Tasks 2 & 3 ✓
- Out of scope (configmap, manifests, tailLines/Truncate caps) → untouched ✓

**Placeholder scan:** every code step has complete code; every run step has an exact command + expected result. No TBD/TODO/"similar to".

**Type consistency:** `defaultMaxLineLength` (Task 2) and `parseMaxLineLength` (Task 2) are reused verbatim in Task 3. `ClampLineLength(string, int) string` (Task 1) is called identically in Tasks 2 and 3. Marker format `…[+N chars]` is identical across helper, params, and docs.

**Known toolchain notes:** `min`/`max` are builtins on Go 1.24.8 (events.go already uses `max`). The fake clientset returns `"fake logs"` for `GetLogs`, so `check_pod_logs` content-clamping is covered by the `ClampLineLength` unit tests (Task 1) rather than a method-level test; `check_events` uses real `List` objects and is clamp-tested end-to-end (Task 3).
