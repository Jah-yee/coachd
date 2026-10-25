# coachd: planned vs actual

Status: design, 2026-10-25. Extends `coachd-design.md` §5 (Data in) and §8 (Data out: workouts).
Tags as in the main design: **[owner]** = owner decision, **[call]** = designer's call,
**[builder]** = left to the implementer.

## 1. Intent

coachd delivers an approved workout to the watch, and a day later it stores the activity the runner
recorded. Nothing connects the two. To review a run against its plan, the agent fetches the series
and lines it up with the steps by hand, and it cannot tell which activity carried out which
workout.

After this change:

1. The runner starts the delivered workout on the watch and runs it.
2. coachd ingests the activity. From its FIT file alone it computes, per executed step: the time,
   distance, heart rate, pace and power, and for a heart-rate or pace range target the seconds
   inside, below and above it.
3. coachd links the activity to the proposal it carried out.
4. The agent reads both in `get_activity`, and `list_workouts` shows which activities carried out
   each delivered workout.

The owner plans a week at a time (2026-10-25), so the agent reviews a week of planned workouts
against what was done. **[owner]**

## 2. What a Garmin activity file records

Checked on the owner's 2025 Garmin Coach runs (fēnix 7S Pro), 2026-10-25:

- An activity recorded from a structured workout holds the `workout` message (`wkt_name`,
  `num_valid_steps`) and every `workout_step` message, with the same fields as a workout file:
  duration type and value, target type, value and custom bounds, intensity, message index.
- Every lap has `wkt_step_index`, the message index of the step it belongs to. Auto-laps split a
  step into several laps with the same index. A repeat step never has a lap; each iteration of its
  body gives new laps with the body's indices. The lap after the last step (the runner keeps
  recording) has index 65535 (invalid).
- Activities recorded without a workout have no `workout` message and no `wkt_step_index`. Zwift
  activity files have one lap per Zwift block and no `wkt_step_index`.

So the activity file describes both the plan the watch followed and what happened. **[call]** The
numbers come from the activity file, not from the proposal. They are the same for a Garmin Coach or
Garmin Connect workout, which has no proposal.

## 3. Goals, non-goals, anti-goals

**Goals**
- Per-step numbers for any activity recorded from a structured workout on a Garmin watch.
- The link from an activity to the delivered proposal it carried out, and back.
- Pure and deterministic, numbers only, computed at ingest, like every other summary.

**Non-goals**
- Zwift-recorded rides. Their laps follow Zwift blocks but carry no step index, so matching laps to
  steps would be a heuristic. Possible later.
- Time in band for power and cadence targets. Power targets are in % of FTP, which coachd does not
  store, and running cadence units vary by device. Average power is reported.
- The runner's page. The agent presents the outcome. Possible later.
- A new MCP tool. The outcome extends `get_activity` and `list_workouts`.

**Anti-goals**
- Verdict words. No "completed", "missed", "compliant", "too fast". Seconds and counts only.
- A link that is a guess. The link needs the activity's recorded steps to equal what coachd
  encoded, exactly (§5).
- Garmin free text served as trusted. `wkt_name` from the file is `name_untrusted`.

## 4. Executed steps

Decoding an activity FIT file also collects the `workout` and `workout_step` messages and each
lap's `wkt_step_index`, elapsed time and average power. When the file has a `workout` message and
at least one lap with a valid step index, the decoded detail gets a workout:

- **Planned steps:** every `workout_step`, by message index, kept in FIT units.
- **Executed steps:** consecutive laps with the same valid step index form one executed step. A
  lap with an invalid index ends the run of the current step and belongs to none. The iteration
  counts executed steps of the same index, from 1.
  - Limit: a repeat whose body is a single step gives consecutive laps with the same index, so
    its iterations merge into one executed step. The executed step's `lap_count` shows it. **[call]**
- Per executed step:
  - start offset, from the activity start, as for laps;
  - duration: the sum of the laps' timer time; distance: the sum;
  - average heart rate, pace and power: the laps' averages weighted by timer time; maximum
    heart rate: the laps' maximum;
  - **time in target**, for a step whose target is a heart-rate range in bpm or a speed range:
    `measured_s`, `in_target_s`, `below_target_s` and `above_target_s`, counted on the
    full-resolution records from its first lap's start to its last lap's end (start plus elapsed
    time). Each record counts for the time to the next record, capped at 10 s so a pause does not
    count, and never past the step's end. **[call]** A record without the value counts nowhere;
    for pace that includes standing still (below 0.3 m/s).
    - Below and above refer to the target's own numbers: for heart rate, under `low_bpm` and over
      `high_bpm`; for pace, under `low_pace_s_per_km` (faster) and over `high_pace_s_per_km`
      (slower).
    - A heart-rate target in % of max, a zone target, or any other target has no time in target.

The workout is cached in `activity_detail` with the laps and series, so it is computed at ingest
and on the lazy rebuild in `get_activity`. The new columns ship with `DELETE FROM activity_detail`
per the cache rule; the cache refills newest first, 20 per FIT drain, and on every `get_activity`
miss. **[call]**

## 5. The link to a proposal

- **Fingerprint.** The SHA-256 of the planned steps' structure, in message-index order: per step,
  the duration type and value, and the target type with its bounds (custom low and high when the
  target value is 0, else the target value; nothing for an open target). Names, notes and
  intensity are left out: watches may truncate or rewrite them. **[call]**
- **coachd's side.** For a delivered proposal, coachd encodes its workout with the FIT encoder,
  decodes the steps back from those bytes, and takes the same fingerprint. The round trip means
  both sides are read by the same decoder.
- **Match.** An activity carried out a proposal when:
  - the proposal is `delivered`;
  - the fingerprints are equal;
  - the proposal's date is within 3 days of the activity's date, either way. **[call]** The runner
    may move a run a day or two.

  Among several matching proposals (the same easy run delivered twice in a week), the nearest date
  wins, then the earlier creation time. One proposal can have several activities: the runner did it
  twice.
- **Stored.** The activity's fingerprint is a column of `activity_detail`, so `list_workouts` finds
  candidate activities with one query. The link itself is computed when read: it is a pure function
  of stored rows, so a proposal delivered after the activity was ingested still links. **[call]**
- **Unverified:** that a watch running a workout downloaded by the coachd watch app writes the same
  step values into the activity as the file coachd sent. Garmin Coach runs show that the steps are
  copied. Check on the first run of a coachd workout (2026-10-26); if the values differ, the
  per-step numbers still work and only the link is missing. **[call]**

## 6. MCP

**`get_activity`** with detail `laps` or `series` gains `workout`, present only when the activity
recorded a structured workout:

| Field | Meaning |
|---|---|
| `proposal_id` | The delivered proposal this activity carried out (§5), or null |
| `name_untrusted` | `wkt_name` from the file; untrusted text |
| `steps` | Planned steps: `index`, `kind` (warmup, active, recovery, rest, cooldown, repeat), `duration` and `target` in the same shape and units as `propose_workout`, with type `other` for what that shape cannot express; a repeat step has `repeat_from_index` and `count` |
| `executed` | Executed steps (§4): `step_index`, `iteration`, `lap_count`, `start_offset_s`, `duration_s`, `distance_km`, `avg_hr_bpm`, `max_hr_bpm`, `avg_pace_s_per_km`, `avg_power_w`, `target_time` (null or `measured_s`, `in_target_s`, `below_target_s`, `above_target_s`) |

**`list_workouts`** gains `activity_ids` on each workout: the activities that carried it out, oldest
first; empty unless delivered.

The tool descriptions say how to read the two, without advice.

## 7. Telemetry

No new keys. The detail decode already runs inside the FIT drain's span and the tool spans.

## 8. Docs

- `coachd-design.md`: §5 data stored gains the executed steps; §8 gains the link.
- README: what the agent sees after a run.
- CHANGELOG `[Unreleased]`, under Added.

## 9. Tests

- Decode: planned steps; executed steps from laps, including auto-laps merged, a repeat's
  iterations, the trailing invalid-index lap; time in target for heart rate and pace with records
  below, inside and above; pauses capped; no workout without a `workout` message.
- Fingerprint: the activity built from coachd's encoded steps has the proposal's fingerprint; a
  changed duration or target changes it; names and notes do not.
- Link: nearest date, tie on creation time, the 3-day window, only delivered proposals, two
  activities for one proposal, and `list_workouts` agreeing with `get_activity`.
- Store: the detail golden JSON with a workout, the migration's cache reset.
- MCP: the new fields, golden tool lists.

## 10. Left to the builder

- Package layout.
- Rounding of output numbers, as for laps.
