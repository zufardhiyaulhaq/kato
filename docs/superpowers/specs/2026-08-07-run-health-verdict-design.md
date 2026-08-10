{% raw %}
# kato Run Health Verdict — Design

**Status:** Approved (design)

**Goal:** Give every run a small, structured **health verdict** — `healthy`
(true / false / unknown) plus a short `headline` (single line, hard-capped) —
produced by the summarizer
that already reads all the evidence. Surface it on the `POST .../run` response, on
`GET /runs`, and on the `Run` CR status. This lets a machine (a dashboard, an
n8n branch, or kato-bot's group tallies) answer *"is this service healthy?"*
without re-reading prose.

**Consumed by:** kato-bot's Group runs feature
(`kato-bot/docs/superpowers/specs/2026-08-07-group-runs-lark-design.md`), which
colors its 🟢/🔴 tallies from this field. That feature degrades to "unknown"
when the verdict is absent, so the two ship independently; shipping this first
makes the tallies meaningful.

## Problem

kato's run **phase** (`Succeeded` / `PartiallySucceeded` / `Failed`) reports
whether the *steps executed*, not whether the *thing checked is well*. A
`deployment-troubleshooting` run against a CrashLooping deployment completes
every step and reports `Succeeded` — the bad news lives only in the LLM summary
prose. There is no field a caller can branch on. Any consumer that wants a
red/green signal today has to parse free text.

The LLM is already the right judge: it reads the filtered evidence to write the
summary. We ask it to also emit a verdict, and we capture it.

## Non-goals

- **Changing deterministic behavior.** The verdict is derived from the LLM only.
  It never affects step outcomes, the run phase, `when` evaluation, or
  `$(steps.x.y)` references. A missing/garbled verdict is treated exactly like a
  summary that failed: non-fatal, run still succeeds (spec §6.6 unchanged).
- **Requiring JSON mode / function calling.** kato supports any
  OpenAI-compatible endpoint including local Ollama, where structured-output
  modes are inconsistent. The verdict rides a strict **first-line convention** in
  the model's text output, robustly parsed and tolerant of omission.
- **Per-UseCase verdict configuration.** The verdict instruction is appended by
  kato to *every* summary prompt uniformly. UseCase authors do not opt in or
  tune it.
- **A separate verdict LLM call.** The verdict comes from the *same* summary
  completion — no extra token cost beyond a short header line.

## Mechanism

kato appends a fixed instruction to the resolved summary prompt: the model must
begin its reply with one line in the form

```
VERDICT: healthy|unhealthy|unknown — <short headline>
```

then a blank line, then the normal prose. kato parses that first line and strips
it from the stored/returned `summary`.

**Parser** (case-insensitive, whitespace- and dash-tolerant): match the first
non-empty line against `^\s*VERDICT:\s*(healthy|unhealthy|unknown)\b[\s—:-]*(.*)$`.

- `healthy` → `Healthy = &true`; `unhealthy` → `Healthy = &false`;
  `unknown` (or **no match**) → `Healthy = nil`.
- Capture group 2 → `Headline`, **trimmed and hard-truncated to 120 characters**
  by the parser (append `…` if cut). The capture stops at the first newline, so a
  `Headline` can never contain a line break or bleed into the prose. "Single line"
  is enforced by *where the capture ends*; length is enforced by *the parser*, not
  by trusting the model — a 10,000-char first line yields a 120-char `Headline`.
- On a match, remove that line (and a following blank line) from the prose. On no
  match, leave the prose untouched and `Headline = ""`.

The parser is total and never errors: any input yields a `(*bool, string)`.

## Data model

`api/v1alpha1/run_types.go` — add to `RunStatus`:

```go
// Healthy is the summarizer's verdict on the subject of the run: true =
// healthy, false = unhealthy, nil = unknown (no verdict emitted, or the
// summary itself failed). It never affects Phase.
Healthy  *bool  `json:"healthy,omitempty"`
// Headline is a short reason accompanying Healthy (e.g.
// "CrashLoopBackOff — bad image tag :v2"). Single line, never longer
// than 120 characters (kato truncates). Empty when unknown.
Headline string `json:"headline,omitempty"`
```

Nullable `*bool` so "unknown" is distinct from "unhealthy" on the wire and in
`kubectl get run -o yaml`. Requires `make generate` (deepcopy) and `make
manifests` (CRD) + copy into `charts/kato/crds/`.

## Wiring

- **Summarizer** (`internal/controller` resolver / `OpenAIClient` call site):
  append the verdict instruction to the prompt; run the completion; parse the
  first line into `(healthy, headline, prose)`; return all three. The summarize
  function's result grows from `summary string` to carry `healthy *bool` +
  `headline`. A summarizer error still sets `Result.Warning` and leaves the
  verdict `nil` (unchanged failure path).
- **Store** (`internal/store/store.go`, `BuildRunStatus`): set
  `Status.Healthy` / `Status.Headline` from the summarize result. Shared by both
  the REST and reconciler execution paths, so kubectl-created and API-created
  runs behave identically.
- **HTTP** (`internal/server/server.go`, `runResponse`): add `healthy` and
  `headline` to the run response JSON (omitted when nil/empty). Present on both
  `POST .../run` and `GET /runs/{name}`.
- **openapi.yaml** (repo root): add `healthy` (nullable boolean) + `headline`
  (string) to the run response and run schemas.
- **Docs:** a short note in `ARCHITECTURE.md` (verdict is LLM-derived, advisory,
  never affects phase) and the run-response section.

## Error handling

| Situation | Result |
|---|---|
| Model emits a well-formed `VERDICT:` line | `healthy` set, `headline` set, line stripped from prose |
| Model emits `VERDICT: unknown …` | `healthy = nil`, headline captured, line stripped |
| Model omits the line entirely | `healthy = nil`, `headline = ""`, prose untouched |
| Summarizer call fails | `healthy = nil`, `Warning` set (today's behavior); run still succeeds |
| Malformed first line | Treated as "no match" — prose untouched, verdict unknown |

The verdict is advisory everywhere. Nothing downstream in kato branches on it.

## Testing

Table-driven, no network (matches existing `internal/` tests):

- **Parser:** healthy/unhealthy/unknown; case variants; em-dash / hyphen / colon
  separators; missing headline; missing line; leading blank lines; extra
  whitespace; prose-stripping correctness; length cap.
- **Store:** `BuildRunStatus` sets `Healthy`/`Headline`; nil-safe when the
  summarizer returned no verdict.
- **HTTP:** `runResponse` includes `healthy`/`headline` when present and omits
  them when unknown; `GET /runs` carries them.
- **Backward-compat:** a summary with no verdict line yields a normal run with
  `healthy` absent and full prose intact.

## Files touched

`api/v1alpha1/run_types.go`, `api/v1alpha1/zz_generated.deepcopy.go` (regen),
`config/crd/bases/*run*.yaml` + `charts/kato/crds/*run*.yaml` (regen), the
summarizer in `internal/controller/`, `internal/store/store.go`,
`internal/server/server.go`, `openapi.yaml`, `ARCHITECTURE.md`, plus tests
alongside each.
{% endraw %}
