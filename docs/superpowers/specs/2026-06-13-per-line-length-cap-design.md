# Per-line length cap for logs and events — Design

**Date:** 2026-06-13
**Status:** Approved (design); implementation plan pending

## Problem

`check_pod_logs` bounds the **number** of lines (`tailLines`, default 10) and caps
the whole blob at 64 KB head+tail (`Truncate`). Neither bounds the length of a
**single line**. A giant line — a JSON log entry, a stack trace flattened onto
one line, a base64 blob — can dominate the output, consume the 64 KB budget, and
blow up token use. `check_events` has the same exposure via a huge event
`Message` rendered on one line.

## Goal

Add a per-line **character** cap to `check_pod_logs` and `check_events`, on by
default, overridable per step.

## Decisions (from brainstorming)

- **Unit:** characters (runes), not words. Log lines are often space-less
  (JSON/base64), so a word cap wouldn't bound them.
- **Default:** on by default at **1000 chars/line**; a param overrides it, and
  `"0"` disables (restores prior behavior for that step).
- **Scope:** `check_pod_logs` and `check_events` only. `check_configmap` data is
  the user's own config and often wanted whole — left unchanged.
- **Marker:** `…[+N chars]` (Unicode ellipsis), appended to any trimmed line.

## Design

### Shared helper (`internal/methods/truncate.go`)

```go
// ClampLineLength trims each line of s to at most maxRunes characters, appending
// a "…[+N chars]" marker to any line it shortened (N = runes dropped from that
// line). maxRunes <= 0 returns s unchanged. Counts runes (UTF-8 safe), so a
// multibyte character is never split.
func ClampLineLength(s string, maxRunes int) string
```

Behavior:
- `maxRunes <= 0` → return `s` unchanged (disabled / unlimited).
- Otherwise split `s` on `"\n"`; for each line whose rune count > `maxRunes`,
  keep the first `maxRunes` runes and append `…[+N chars]` where
  `N = totalRunes - maxRunes`; rejoin with `"\n"`.
- Lines at or under `maxRunes` pass through byte-for-byte.
- Operates on runes via `[]rune(line)` so multibyte characters are never split.

### New param `maxLineLength` (both methods)

Parsing (shared semantics with existing int params):
- Unset / empty → default `defaultMaxLineLength = 1000`.
- Valid integer → use it; `0` → unlimited (cap disabled).
- Negative or non-integer → return a param error
  (`param maxLineLength: ...`), consistent with `tailLines` / `limit`.

A `defaultMaxLineLength = 1000` constant is added (alongside `defaultLogBytes` /
`defaultTailLines` in `pod_logs.go`, exported package-internal so `events.go`
reuses it).

### Application order (clamp first, then whole-blob truncate)

- `check_pod_logs.Run`:
  `Truncate(ClampLineLength(string(raw), maxLine), defaultLogBytes)`
- `check_events.Run`:
  `Truncate(ClampLineLength(b.String(), maxLine), defaultLogBytes)`

Clamping first shrinks oversized lines before the 64 KB head+tail backstop, so
the budget is spent on more lines rather than one runaway line. `tailLines`
(how many lines) and `maxLineLength` (how long each line) are orthogonal;
`Truncate` remains the final whole-blob backstop.

### Docs / surface

- Add the `maxLineLength` param to `Params()` of both methods with the
  default/`0`-disables note.
- Update the `logs` / `events` `OutputField` descriptions to mention per-line
  trimming.
- Update `docs/METHOD.md` for both methods (Inputs tables + a one-line note).
- `GET /api/v1/methods` reflects the new param automatically.

## Testing

**Helper (`truncate_test.go`):**
- Line under cap → unchanged.
- Line over cap → first `maxRunes` runes + `…[+N chars]` with correct N.
- `maxRunes <= 0` → input returned unchanged.
- Multibyte line (e.g. `日本語…`) → trimmed on a rune boundary, never mid-byte.
- Multi-line input where only some lines exceed → only those trimmed; others
  byte-identical; line count preserved.

**`check_pod_logs`:**
- A single 5000-char line is trimmed to ~1000 + marker by default.
- `maxLineLength: "0"` leaves the line whole.
- `maxLineLength: "50"` trims tighter.
- `maxLineLength: "abc"` → error.

**`check_events`:**
- An event whose `Message` is very long has its rendered line clamped by default.

## Out of scope

- `check_configmap` data and `describe_*` manifests (separate concern; manifests
  are already sanitized/truncated as a blob).
- Changing `tailLines` or the 64 KB `Truncate` cap.
