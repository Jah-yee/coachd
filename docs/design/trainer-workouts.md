# coachd: trainer workouts on the watch

Status: design, 2026-10-26. Extends `zwift-workouts.md` and `coachd-design.md` §8 (Data out:
workouts). Tags as in the main design: **[owner]** = owner decision, **[call]** = designer's call,
**[builder]** = left to the implementer.

## 1. Intent

The owner may cancel Zwift and ride coachd's cycling workouts on the watch instead, with the watch
driving a smart trainer. Zwift stays an option: the agent can still plan a workout the runner
downloads as `.zwo`. **[owner]**

coachd already has power targets in % of FTP (`zwift-workouts.md`), and the FIT file already carries
them. Two things were missing for the trainer:

1. **Watts.** The owner asked for power targets in watts as well as in % of FTP. **[owner]**
2. **Indoor.** A cycling workout's FIT file says cycling with a generic sub-sport, so the watch
   starts it as an outdoor ride, whose activity has no smart-trainer controls. The coachd watch app
   starts a workout with `System.exitTo(workout.toIntent())`, so the FIT sport and sub-sport choose
   the watch activity.

## 2. Goals, non-goals, anti-goals

**Goals**
- A `power` target in watts (`low_w`, `high_w`), beside the % of FTP bounds. **[owner]**
- A workout sport `indoor_cycling` that the watch starts as an indoor ride. **[call]**

**Non-goals**
- Storing the runner's FTP. The FIT file carries % of FTP as it is, so the watch converts it with its
  own FTP when the ride starts, and a changed FTP applies to every workout already on the watch.
  Zwift does the same with its FTP. **[call]**, as in `zwift-workouts.md`.
- Garmin's Training API. coachd delivers through its own watch app and FIT files, not Garmin
  Connect.
- Controlling the trainer from coachd. The watch drives it.
- Power ranges in planned vs actual (seconds inside, below and above a power range). Still a
  separate follow-up.
- A watch app change. The device API sends the stored sport, which is `cycling`, so the watch shows
  "Ride" as before.

**Anti-goals**
- A target the watch reads differently from what the page shows. Watts and % of FTP never mix in one
  step.
- A Zwift file that silently converts watts to % of FTP with a guessed FTP.

## 3. Watts

`workout.Target` gains `low_w` and `high_w` (integers, `omitempty`, so existing canonical JSON and
content hashes are unchanged).

- A `power` step sets either `low_pct_ftp` and `high_pct_ftp` or `low_w` and `high_w`. Both pairs,
  or neither, is rejected with a reason.
- Bounds: 1 to 1500 W, `low ≤ high`. The owner suggested 0 to 1500; 0 W has no FIT encoding (see
  §5), so the floor is 1. **[call]**
- Only for `cycling` and `indoor_cycling`, as power in % of FTP.
- The page and the watch detail show a watt step as `power 200–220 W`.
- `get_activity` (planned steps) reads a watt target back as `type: power`, `low_w`, `high_w`.

## 4. Indoor cycling

A new workout sport `indoor_cycling`, for a ride on a smart trainer. **[call]**

- It follows every cycling rule: pace bounds, the distance estimate, power targets, Zwift export.
- It is stored as sport `cycling` (`store.SportCycling`), as ingest stores the runner's indoor rides.
  The stored workout JSON keeps `indoor_cycling`, and the FIT file and the page use it. `list_workouts`
  shows `sport: cycling` and the workout's own `sport: indoor_cycling`.
- The page shows the sport as "indoor cycling".

Why a sport and not a flag: the sport table already maps names to a FIT sport and sub-sport
(`trail_running` is running with sub-sport trail), so this is one row in each table.

## 5. FIT encoding

- `indoor_cycling` is FIT sport cycling with sub-sport `indoor_cycling` (6).
- A watt target is `target_type = power`, `target_value = 0`, `custom_target_value_low/high` = watts +
  1000. In the FIT profile, custom power values 0 to 1000 are % of FTP and values above 1000 are
  watts + 1000 (`typedef.WorkoutPowerWattsOffset`). 1000 itself would be 1000 % of FTP, so 0 W cannot
  be written, hence the 1 W floor.

## 6. Zwift

- `indoor_cycling` exports to Zwift like `cycling`.
- A watt target makes the workout not exportable: `.zwo` power is a fraction of FTP. The reason names
  the step, e.g. `steps[1].target: watt targets have no Zwift equivalent (Zwift takes % of FTP)`. The
  page shows it and the agent gets the existing "not exportable to Zwift" warning.

## 7. To check on the watch **[owner]**

coachd cannot test the watch's trainer behaviour. One short ride settles it:

1. The agent proposes an `indoor_cycling` workout with one 5-minute power step (in watts or % of
   FTP). Approve it and sync the watch app.
2. Pair the trainer as a sensor on the watch, then start the workout from the coachd app.
3. Check that the watch opens the indoor ride activity, that the trainer goes to ERG and holds the
   step's power, and, for % of FTP, that the watch's FTP setting matches Zwift's.

If the watch opens the outdoor ride, or the trainer does not follow, report it: the fix belongs in
the FIT encoding.

## 8. Tests

- Validation: watt bounds, low above high, a missing bound, both pairs, neither pair, a watt field on
  another target type, `indoor_cycling` accepted and planned as cycling, an unknown sport naming
  `indoor_cycling` in the reason.
- FIT: the sport table includes `indoor_cycling`; watt steps encode as watts + 1000 (1 W and 1500 W).
- `.zwo`: `indoor_cycling` exports; a watt step in a repeat gives the reason with its path.
- Planned steps: watts + 1000 decode as `low_w`/`high_w`; a range across 1000 decodes as `other`.
- Page: an indoor ride shows "indoor cycling" and the Zwift download; a watt step shows its line and
  the Zwift reason, with no download.
- The `tools/list` golden files pin the new schema fields and texts.
