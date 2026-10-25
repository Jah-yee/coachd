# Power targets and Zwift workouts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Agents can propose cycling workouts with % FTP power targets; the runner downloads an approved one as a Zwift `.zwo` file beside the FIT file, and the FIT file carries the power targets to the watch.

**Architecture:** `workout` gains a `power` target (cycling only). `fitworkout` encodes it. A new pure package `internal/zwiftworkout` (sibling of `fitworkout`) owns both "can this be a Zwift workout, and if not why" and the XML encoding; the proposal service turns its answer into a warning and the web page into a button or a reason line. The web FIT handler becomes one shared download path serving both formats.

**Tech Stack:** Go 1.27 stdlib (`encoding/xml`, `net/http`, `html/template`), `golang.org/x/text/unicode/norm` (already in the module graph, becomes direct), `muktihari/fit`, OpenTelemetry.

**Spec:** `docs/design/zwift-workouts.md`

## Global Constraints

- Power bounds 20–300 % of FTP, `low ≤ high`, sport `cycling` only; reason text "power targets are only supported for cycling".
- New `Target` fields are `omitempty`: canonical JSON and content hash of every existing workout unchanged.
- `.zwo` text (name, description, text events): accents removed via NFD; any other character outside ASCII letters, digits, space and `- _ . , ( ) % / : +` becomes a space; space runs collapse; ends trimmed; name cut to 50 characters, text events to 80; ranges use `-`; joined texts use ` - `.
- No `Cadence` attribute on `FreeRide`; no `Warmup`/`Cooldown` ramps; one text event per step at `timeoffset="0"`.
- `.zwo` download follows the FIT download's rules exactly (session, CSRF, hash check, statuses, first download of either format delivers).
- No verdict words in any warning or page text (`TestNoVerdictWords` list: should, ready, too, risky, recommend, safe, dangerous, must, need).
- Telemetry: only `format` (`fit`|`zwo`) is added to `AllowedKeys`.
- Builds stay cgo-free; `gofmt -l .` prints nothing; `go vet ./...` clean.
- Commits: `git commit -s` (DCO), author is the existing git config (noreply).

## Review Focus

1. A name or note made only of emoji or non-Latin script folds to an empty string → the `.zwo` name falls back to `coachd workout`, an empty text event is omitted (Task 3 test `TestSafeTextFallbacks`).
2. A power band with an odd sum (88–91 %) → `Power="0.895"`, exact and deterministic (Task 3 `TestEncodeSteadyState`).
3. Downloading `.zwo` first and FIT second (or the reverse) → exactly one `proposal_delivered` audit row; the second is a `redownload` (Task 5 `TestZwoThenFITDeliversOnce`).
4. A cycling workout approved before this release with a distance step → page shows the reason line, POST `/p/{id}/zwo` answers 409 with no transition (Task 5 `TestZwoNotExportable`).
5. Existing workouts without power → canonical JSON byte-identical (Task 1 `TestCanonicalJSONUnchangedWithoutPower`).

---

### Task 1: Power target in the model and validation

**Files:**
- Modify: `internal/workout/workout.go` (Target), `internal/workout/validate.go` (checkTarget)
- Modify: `internal/web/describe.go` (describeTarget)
- Modify: `internal/mcpserver/testdata/tools_list_*.json` (regenerated)
- Test: `internal/workout/workout_test.go`, `internal/web/describe_test.go`

**Interfaces:**
- Produces: `workout.Target.LowPctFTP, HighPctFTP *int` (JSON `low_pct_ftp`, `high_pct_ftp`, omitempty); target type `"power"`.

- [ ] **Step 1: Failing tests** in `workout_test.go`:
  - `TestPowerTarget`: cycling step `target {type: power, low 88, high 92}` validates; table of rejects with exact reasons, path `steps[0].target`:
    - running sport → `steps[0].target: power targets are only supported for cycling`
    - low 10 → `steps[0].target: low_pct_ftp 10 % FTP below 20`
    - high 301 → `steps[0].target: high_pct_ftp 301 % FTP above 300`
    - low 95 high 90 → `steps[0].target: low_pct_ftp 95 is above high_pct_ftp 90; low must not exceed high`
    - missing high → `steps[0].target: high_pct_ftp is required`
    - `low_pct_ftp` on an hr target → `steps[0].target: low_pct_ftp set for type=hr`
    - unknown type → message lists `none|hr|pace|cadence|effort|power`
  - `TestCanonicalJSONUnchangedWithoutPower`: an hr-target workout's `CanonicalJSON` contains no `pct_ftp`, and equals the bytes it produced before the change (hard-code the expected JSON string).
  - `describe_test.go`: power step reads `Active · 5:00 · power 88–92% FTP`.
- [ ] **Step 2:** `go test ./internal/workout ./internal/web -run 'Power|Canonical|Describe'` → FAIL.
- [ ] **Step 3: Implement.** Target fields with jsonschema text `Only for type power (cycling only): lower bound in % of FTP` / `upper bound in % of FTP`; type tag `none|hr|pace|cadence|effort|power`; cadence tags append ` (rpm for cycling)`. `checkTarget`: add `power` to the type switch and the `other()` table; power case checks `r.sport == store.SportCycling` first, then `pair(..., 20, 300, "% FTP")`. Constants `minPowerPct = 20`, `maxPowerPct = 300`. `describeTarget`: `"power " + num(lo) + "–" + num(hi) + "% FTP"`.
- [ ] **Step 4:** `go test ./internal/workout ./internal/web` → PASS. Then `go test ./internal/mcpserver -run 'ToolsListSchemasUnchanged|ToolResultsEquivalent' -update`, inspect `git diff internal/mcpserver/testdata` (only target schema text/fields and the type-list message change), then `go test ./internal/mcpserver` → PASS.
- [ ] **Step 5: Commit** `feat(workout): power target in % of FTP for cycling`.

### Task 2: Power in the FIT file

**Files:** Modify `internal/fitworkout/encode.go` (leafStep); Test `internal/fitworkout/encode_test.go`

**Interfaces:** Consumes Task 1 fields.

- [ ] **Step 1: Failing test** `TestEncodePower`: cycling step power 88–92 decodes as `TargetType == typedef.WktStepTargetPower`, `TargetValue == 0`, `CustomTargetValueLow == 88`, `CustomTargetValueHigh == 92`.
- [ ] **Step 2:** `go test ./internal/fitworkout -run Power` → FAIL ("unknown target").
- [ ] **Step 3:** `case "power"` in `leafStep`, same shape as cadence (values unchanged; FIT custom power 0–1000 = % FTP).
- [ ] **Step 4:** `go test ./internal/fitworkout` → PASS.
- [ ] **Step 5: Commit** `feat(fitworkout): encode power targets`.

### Task 3: `internal/zwiftworkout` package

**Files:**
- Create: `internal/zwiftworkout/encode.go`, `internal/zwiftworkout/encode_test.go`, `internal/zwiftworkout/testdata/*.zwo`
- Modify: `go.mod` (`golang.org/x/text` becomes direct via `go mod tidy`)

**Interfaces:**
- Produces:
  - `func Unsupported(w workout.Workout) string` — `""` when exportable; otherwise the first reason: `sport: only cycling workouts export to Zwift`, or `<path>.duration: <type> durations have no Zwift equivalent` for the first leaf in depth-first order whose duration is not `time` (path style as validation: `steps[2]`, `steps[1].steps[0]`, `steps[1].steps[0].steps[1]`).
  - `func Encode(w workout.Workout) ([]byte, error)` — w must pass `workout.Validate`; error if `Unsupported(w) != ""`.
  - `func FileName(date civil.Date, id string) string` — `coachd-<date>-<first 8 of id>.zwo`.
  - `const ContentType = "application/xml"`.

Document shape (`xml.MarshalIndent(..., "", "  ")`, no XML declaration, trailing `\n`):

```xml
<workout_file>
  <author>coachd</author>
  <name>Sweet spot 3x10</name>
  <description>Steady</description>
  <sportType>bike</sportType>
  <workout>
    <FreeRide Duration="600">
      <textevent timeoffset="0" message="HR 120-135 bpm - spin easy"></textevent>
    </FreeRide>
    <IntervalsT Repeat="3" OnDuration="600" OffDuration="300" OnPower="0.9" OffPower="0.55"></IntervalsT>
    <SteadyState Duration="300" Power="0.895"></SteadyState>
  </workout>
</workout_file>
```

Step mapping: power → `SteadyState Duration Power` (Power = `strconv.FormatFloat(float64(lo+hi)/200, 'f', -1, 64)`); any other target or none → `FreeRide Duration`. Text event message = target text (`HR 130-140 bpm`, `pace 2:00-2:30 /km`, `cadence 85-95 rpm`, the effort cue; nothing for power/none/nil) and step notes joined with ` - `, then safe text cut to 80; omitted when empty. A top-level repeat whose children are exactly two leaves, both power targets, no notes on either child or on the repeat → `IntervalsT Repeat OnDuration OffDuration OnPower OffPower` (first child = On). Every other repeat, and every nested repeat, is unrolled step by step. A repeat's own notes are not written (as in the FIT file). Name: safe text cut to 50, fallback `coachd workout` when empty. Description: workout notes as safe text (no cut).

- [ ] **Step 1: Failing tests** (golden files compared byte for byte, `-update` flag rewrites them as in `internal/mcpserver/golden_test.go`):
  - `TestEncodeSteadyState` (golden `steady.zwo`: 88–92 → `0.9`, 88–91 → `0.895`).
  - `TestEncodeFreeRideText` (golden `freeride.zwo`: hr, pace, cadence, effort, none, nil target, and notes joined with ` - `).
  - `TestEncodeIntervalsT` (golden `intervals.zwo`), `TestEncodeUnrolledRepeat` (child with notes → unrolled), `TestEncodeNestedRepeat` (golden `nested.zwo`).
  - `TestUnsupportedReasons`: running → `sport: only cycling workouts export to Zwift`; `steps[1].duration: distance durations have no Zwift equivalent`; `steps[0].steps[1].duration: open durations have no Zwift equivalent`; nested `steps[0].steps[0].steps[0].duration: hr_above durations have no Zwift equivalent`; all-time cycling → `""`. `Encode` of each returns an error.
  - `TestSafeTextFallbacks`: `safeText("Zóna 2 – ľahko", 50)` = `Zona 2 lahko`; `"🚴💪"` → name `coachd workout`; text `a<b>&"c"` → `a b c`; a 100-char note cut to 80 with no trailing space; newline/tab → space.
  - `TestEncodeDeterministic`: two encodes byte-equal. `TestFileName`: `coachd-2026-10-24-abcdefgh.zwo`.
- [ ] **Step 2:** `go test ./internal/zwiftworkout` → FAIL (package missing).
- [ ] **Step 3: Implement** with `encoding/xml` structs (elements in a `[]any` under `<workout>`), `norm.NFD` + drop `unicode.Mn` for accent removal, `fitworkout`-style step flattening.
- [ ] **Step 4:** `go test ./internal/zwiftworkout -update`, read every golden file by eye against the shape above, then `go test ./internal/zwiftworkout` → PASS; `go mod tidy && CGO_ENABLED=0 go build ./cmd/coachd`.
- [ ] **Step 5: Commit** `feat(zwiftworkout): encode cycling workouts as Zwift .zwo`.

### Task 4: `zwift_not_exportable` warning

**Files:** Modify `internal/guidance/guidance.go`, `internal/proposals/service.go` (warningInputs or its caller); Test `internal/guidance/guidance_test.go`, `internal/proposals/*_test.go`

**Interfaces:**
- Consumes: `zwiftworkout.Unsupported`.
- Produces: `guidance.WarningInputs.ZwiftUnsupported string`; warning `{Code: "zwift_not_exportable", Message: "not exportable to Zwift: " + reason}` emitted after `above_max_hr`, before `stale_data`.

- [ ] **Step 1: Failing tests:** guidance `TestZwiftNotExportable` (present with the exact message when set; absent when empty); add it to `TestWarningsDeterministicOrder`'s want list and to `TestNoVerdictWords`' inputs. Proposal service test: a cycling workout with a distance step returns the warning; an all-time cycling workout and a running workout with a distance step do not.
- [ ] **Step 2:** `go test ./internal/guidance ./internal/proposals` → FAIL.
- [ ] **Step 3:** Service sets `ZwiftUnsupported = zwiftworkout.Unsupported(w)` only when `plan.Sport == store.SportCycling` (guidance stays pure; update the `Warnings` doc comment's order list).
- [ ] **Step 4:** `go test ./internal/guidance ./internal/proposals` → PASS.
- [ ] **Step 5: Commit** `feat(proposals): warn when a cycling workout cannot export to Zwift`.

### Task 5: `.zwo` download on the proposal page

**Files:**
- Modify: `internal/web/proposal.go` (fit → shared `download`), `internal/web/gate.go` (route), `internal/web/templates/proposal.html`
- Modify: `internal/telemetry/redact.go` (`AllowedKeys` + `"format"`), `internal/telemetry/redact_test.go` (expected list), `internal/telemetry/metrics.go` (Deliveries description `Workout downloads by outcome; web downloads also by format`)
- Test: `internal/web/proposal_test.go`

**Interfaces:**
- Consumes: `fitworkout.Encode/FileName`, `zwiftworkout.Encode/FileName/Unsupported/ContentType`.
- Produces: route `POST /p/{id}/zwo` behind `requireSession`; `proposalPage.Zwift bool`, `proposalPage.ZwiftNote string`.

Shared path: `func (s *Server) download(f fileFormat) http.HandlerFunc`, with `fileFormat{name, contentType, failMsg string; fileName func(civil.Date, string) string; encode func(workout.Workout, store.Proposal) ([]byte, error); unsupported func(workout.Workout) string}` (nil `unsupported` for FIT). Order: status check (409) → hash check → `unsupported` → 409 via `renderProposal` with `"not exportable to Zwift: " + reason` → encode (500 `failMsg`; Zwift: `could not encode this workout for Zwift`) → transition exactly as today. Span `proposal.deliver` and metric carry `format`; `fitFailed` becomes `downloadFailed` and logs `"format"`.

Page: when `v.Deliver` and the stored workout decoded and sport is cycling: `Zwift = Unsupported(w) == ""`, else `ZwiftNote = "Not exportable to Zwift: " + reason`. Template, right after the FIT form: a `Download for Zwift` form button to `/p/{{.ID}}/zwo` (same icon, not `primary`) when `.Zwift`; `{{with .ZwiftNote}}<p class="hint">{{.}}</p>{{end}}`.

- [ ] **Step 1: Failing tests:**
  - Helper `deliveries(t)` keys points `outcome + "/" + format` and requires exactly those two attributes; update existing assertions (`delivered/fit`, `redownload/fit`) everywhere it is used.
  - `TestZwoDownload`: approved all-time cycling proposal with power; POST `/p/{id}/zwo` → 200, `Content-Type: application/xml`, `Content-Disposition: attachment; filename="coachd-<date>-<id8>.zwo"`, `checkPageHeaders(..., "no-store")`, body equals `zwiftworkout.Encode(w)`; status `delivered`, one audit row (client `passkey:1`, actor `runner`); second POST same bytes, `deliveries == {delivered/zwo:1, redownload/zwo:1}`; span has `format=zwo`.
  - `TestZwoThenFITDeliversOnce`: zwo then fit → one `proposal_delivered` row, `{delivered/zwo:1, redownload/fit:1}`.
  - `TestZwoNotExportable`: approved cycling proposal with a distance step: page contains `Not exportable to Zwift: steps[` and no `/zwo` form; POST `/p/{id}/zwo` → 409 body contains `not exportable to Zwift`, status stays approved, no audit row. Running proposal: no Zwift button, no note.
  - `TestZwoPendingConflict` (pending/declined/withdrawn → 409), `TestZwoIntegrity` (tampered content → 500 hash message), and without a session/CSRF the route is refused (the existing gate walk covers the route automatically; add the CSRF case like the FIT one if one exists).
  - Telemetry: `redact_test` expected list includes `format`.
- [ ] **Step 2:** `go test ./internal/web ./internal/telemetry` → FAIL.
- [ ] **Step 3: Implement** as above.
- [ ] **Step 4:** `go test -race ./...` → PASS; `go vet ./...`; `gofmt -l .` empty.
- [ ] **Step 5: Commit** `feat(web): download approved cycling workouts for Zwift`.

### Task 6: Docs

**Files:** `docs/design/coachd-design.md` §8, `docs/design/zwift-workouts.md` (§5 reason example matches the shipped text), `README.md`, `CHANGELOG.md`

- [ ] **Step 1:** §8: target row adds `power range (% of FTP, cycling only)`; sanity checks add `power 20–300 % of FTP, cycling only`; warnings add **Not exportable to Zwift**; approval/delivery notes add the `.zwo` download and that the first download of either file delivers.
- [ ] **Step 2:** README approval flow step 5 mentions Download for Zwift; new subsection "Zwift workouts": copy the `.zwo` into `~/Documents/Zwift/Workouts/<your Zwift ID>/` (Mac) or `Documents\Zwift\Workouts\<your Zwift ID>\` (Windows), restart Zwift, find it under Custom Workouts; Apple TV and iPad get it only through Zwift's cloud sync after it is imported on a Mac or PC; only all-time cycling workouts export; power is % of FTP, so Zwift applies your Zwift FTP.
- [ ] **Step 3:** CHANGELOG `[Unreleased]` → `### Added`: power targets (MCP, FIT), Download for Zwift, `zwift_not_exportable` warning, `format` telemetry attribute; note "The `.zwo` files were not tried in Zwift before this release; report a file Zwift does not list."
- [ ] **Step 4: Commit** `docs: power targets and Zwift workouts`.

### Task 7: Review and release

- [ ] **Step 1:** One fresh reviewer on the whole branch diff against `main` with the spec; fix findings; `go test -race ./...`, `go vet ./...`, `gofmt -l .`, `CGO_ENABLED=0 go build ./cmd/coachd`.
- [ ] **Step 2:** CHANGELOG `[Unreleased]` content moves under `## [0.3.0] - 2026-10-25` (empty `[Unreleased]` stays). Commit `docs: changelog 0.3.0 dated 2026-10-25`.
- [ ] **Step 3:** Push `zwift-workouts`, open PR to `main`, wait for CI green, merge with a merge commit (as earlier releases).
- [ ] **Step 4:** On updated `main`: `git tag -a v0.3.0 -m "coachd 0.3.0"`, push the tag, watch the `release` workflow to success; confirm the GitHub release notes are the 0.3.0 section.
