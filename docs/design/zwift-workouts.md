# coachd: power targets and Zwift workouts

Status: design, 2026-10-25. Extends `coachd-design.md` §8 (Data out: workouts). Tags as in the main
design: **[owner]** = owner decision, **[call]** = designer's call, **[builder]** = left to the
implementer.

## 1. Intent

The owner trains for a trail ultra with two runs a week and Zwift on the other days. The agent
already plans those Zwift days, but coachd can only deliver to the watch, and its workouts have no
power target. So a planned Zwift session reaches Zwift only if the runner rebuilds it by hand in
Zwift's workout editor.

After this change:

1. The agent proposes a cycling workout with power targets in % of FTP.
2. The runner approves it with a passkey, as today.
3. On the proposal page the runner clicks **Download for Zwift** and gets a `.zwo` file.
4. They copy it into `~/Documents/Zwift/Workouts/<Zwift ID>/` on their Mac and restart Zwift. The
   workout appears under Custom Workouts.

The same power targets also go into the FIT file, so the watch shows them on an outdoor ride with a
power meter.

## 2. Goals, non-goals, anti-goals

**Goals**
- A `power` step target in % of FTP, for cycling. **[owner, via the chosen feature]**
- A `.zwo` download for approved cycling workouts, by hand, beside the FIT download. **[owner]**
- Power targets in the FIT file for the watch. **[call]**

**Non-goals**
- Any Zwift account link, Zwift API, or a sync through intervals.icu or another platform. Zwift's
  Training API is invite-only for platforms, and the unofficial API needs the runner's Zwift
  password. **[owner]** chose the manual download.
- Power or cadence in ride analysis (`get_activity` laps and series). A separate follow-up.
- Storing the runner's FTP. Targets are relative; Zwift and the watch each apply their own FTP.
- Running power, and power targets for any sport but cycling.
- Zwift run workouts (`.zwo` with pace targets).
- Ramps (a target that moves from one value to another over a step). The model has bands, not
  ramps.

**Anti-goals**
- A Zwift export that silently changes the workout. A step that cannot be expressed in Zwift is
  either converted by a rule this document states, or the export is not offered.
- Any delivery path that skips passkey approval. The `.zwo` download follows the FIT download's rules
  exactly.
- Coaching logic. Exportability is a format fact, stated as a warning, never a block.

## 3. The power target

A new target type `power` in `workout.Target`, which is also the MCP input schema:

| Field | Meaning |
|---|---|
| `type` | `power` |
| `low_pct_ftp` | lower bound, integer % of FTP |
| `high_pct_ftp` | upper bound, integer % of FTP |

- Bounds: 20 to 300 % of FTP, `low ≤ high`. Same error style as the other ranges. **[call]**
- Only for sport `cycling`. Any other sport is rejected with a reason ("power targets are only
  supported for cycling"). **[call]**
- The fields are `omitempty`, so the canonical JSON and content hash of every existing proposal are
  unchanged.
- The schema text for cadence says it is rpm for cycling (it already is; the FIT cadence target is
  rpm on a bike). **[call]**
- The page shows a power step as `power 88–92% FTP`.

## 4. FIT encoding

`fitworkout` writes a power target as `target_type = power`, `target_value = 0`,
`custom_target_value_low/high` = the percentages. In the FIT profile, custom power values 0–1000 are
% of FTP (above 1000 they are watts + 1000), so the values go in unchanged. The watch fetches the FIT
file from the device API, so the watch app needs no change.

## 5. The `.zwo` file

A new pure package (like `fitworkout`: no I/O, no store) encodes a validated workout as Zwift's
workout XML, and also answers whether a workout can be exported and, if not, why. Both the
approval page and the warning (§7) use that one answer. **[call]**

**Exportable** when the sport is `cycling` and every leaf step has a `time` duration. Distance, open
and heart-rate-bounded durations have no Zwift equivalent. The reason names the first such step in
the same path style as validation errors, e.g. `steps[2].duration: distance durations have no Zwift equivalent`.

**Document**
- `<workout_file>` with `<author>coachd</author>`, `<name>` (the workout name), `<description>` (the
  workout notes), `<sportType>bike</sportType>`, and `<workout>` holding the steps.
- Text is XML-escaped with `encoding/xml`.
- **Safe text** **[call]**: the owner cannot try files in Zwift before release **[owner]**, so every
  text the file carries (name, description, text events) uses the most conservative form, because
  Zwift advises against special characters in names and documents no limits:
  - accents are removed (`Zóna` → `Zona`); any other character outside ASCII letters, digits, space
    and `- _ . , ( ) % / : +` becomes a space; runs of spaces collapse and the ends are trimmed;
  - the name is cut to 50 characters and each text event to 80;
  - ranges use `-`, not an en dash, and joined texts use ` - `, not ` · `.
- Output is deterministic: the same workout gives the same bytes.

**Steps** **[call]**

| coachd step | Zwift element |
|---|---|
| power target | `SteadyState` with `Power` = midpoint of the band ÷ 100 (e.g. 88–92 % → `0.90`) |
| cadence target | `FreeRide` with a text event `cadence 85-95 rpm` |
| heart-rate, pace, effort, `none` or no target | `FreeRide` for the step's duration, with a text event naming the target (`HR 130-140 bpm`, `pace 2:00-2:30 /km`, the effort cue); no text event for `none` |

- Every kind (warm-up, active, recovery, rest, cool-down) maps the same way. No `Warmup`/`Cooldown`
  ramp elements, because the model has no ramps.
- A step has one target, so it never carries both power and cadence. A cadence `FreeRide` does not
  set Zwift's `Cadence` attribute: whether Zwift honours it there is undocumented and cannot be
  tried before release. **[call]**
- Pace targets stay allowed for cycling (validation already accepts them); in Zwift they are text
  only, like heart rate, and do not block the export. **[call]**
- Each step gets at most one text event, at its start (`timeoffset="0"`): the target text and the
  step notes joined with " - ", as `fitworkout` joins cue and notes. **[call]** Notes and cues are
  agent-written. The FIT file already carries them to the watch, so showing them in Zwift adds no
  new exposure; on the page they stay labelled as agent text.
- **Repeats:** a repeat with exactly two leaf children, both with power targets and no notes,
  becomes `IntervalsT` (`Repeat`, `OnDuration`, `OffDuration`, `OnPower`, `OffPower`), so Zwift shows
  its interval counter. Every other repeat, and every nested repeat, is written out step by step,
  as `fitworkout` already unrolls nested repeats.

## 6. Download and delivery

- `POST /p/{id}/zwo`, beside `POST /p/{id}/fit`, with the same rules: a passkey session and CSRF
  protection; the stored workout's content hash must match the approved hash; allowed in
  `approved`, `delivering`, `delivery_failed` and `delivered`; nothing stored, the file is encoded
  on demand.
- **Delivery:** the first download of either file (FIT or `.zwo`), or the watch's acknowledgement,
  moves the proposal to `delivered`. Later downloads serve the same bytes with no transition. Same
  audit entry, actor `runner`. **[call]**
- File name `coachd-<date>-<first 8 of id>.zwo`. Content type and disposition as the FIT download
  (attachment). **[builder]** picks the content type.
- The FIT handler and the new one share one download path that differs only in the encoder, name
  and content type. **[call]**
- The page shows **Download for Zwift** only for an exportable cycling workout. For a cycling
  workout that is not exportable it shows the reason on one line instead.
- Telemetry: the delivery span and the deliveries metric gain a `format` attribute (`fit` or `zwo`),
  declared in the redaction allowlist. **[call]**

## 7. Warning

A new deterministic warning on `propose_workout` for a cycling workout that is not exportable,
code `zwift_not_exportable`, message `not exportable to Zwift: steps[2].duration: distance durations
have no Zwift equivalent`. It never blocks and holds no advice, so the agent can re-propose with time
durations if it wants Zwift. **[call]**

## 8. Docs

- `coachd-design.md` §8: the target row gains power, the sanity checks gain the power bounds, the
  warnings gain the Zwift warning, and delivery gains the `.zwo` download.
- README: how to import a `.zwo` into Zwift on a Mac or PC (the folder, restart Zwift, Custom
  Workouts), and that Apple TV and iPad get it only through Zwift's cloud sync.
- CHANGELOG `[Unreleased]`, under Added, saying the `.zwo` files were not tried in Zwift before
  release.

## 9. Tests

- Validation: power bounds, low > high, wrong sport, fields set on other target types.
- FIT: a power step encodes as a power target with the percentages.
- `.zwo`: golden files for steady power, free ride with each kind of text event, cadence,
  `IntervalsT`, an unrolled repeat, a nested repeat, notes; the exportability reason for each
  blocking duration type; deterministic bytes.
- Web: the download needs a session and CSRF; the hash check; the first download delivers and
  later ones do not; the button and the reason line on the page.
- Warning: present for a non-exportable cycling workout, absent for an exportable one and for other
  sports.

## 10. Left to the builder

- Package name and file layout.
- The `.zwo` content type.
- Exact message wording, within the no-verdict rule.
