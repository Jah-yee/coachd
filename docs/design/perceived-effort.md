# coachd: the runner's own post-activity rating

Status: design, 2026-10-25. Extends `coachd-design.md` §5 (Data in). Tags as in the main design:
**[owner]** = owner decision, **[call]** = designer's call, **[builder]** = left to the implementer.

## 1. Intent

After a workout, Garmin watches ask "How did you feel?" (five faces) and "How hard was it?" (RPE,
0 to 10). The answers are written to the activity's FIT file, in the session message's
`workout_feel` (0 to 100) and `workout_rpe` (0 to 100, ten times the RPE). The Personal API's
activity JSON does not carry them (checked on the owner's 230 activities, 2026-10-25). The FIT
files do: 51 of them.

The rating is the runner's own report on the run, which the agent asks for in chat today. Reading
it from the FIT file costs nothing and stays Garmin data. Structured check-ins are still out of
scope: coachd stores no ratings of its own.

## 2. Design

- Decoding a FIT file also reads the first session's `workout_feel` and `workout_rpe`. Values
  above 100, and the invalid sentinel, become null.
- They are stored on the activity's row (`perceived_feel`, `perceived_rpe`), in the transaction that
  stores the activity detail. They are written only there: Garmin re-listing an activity leaves them
  untouched. **[call]** Migration 0013 resets the detail cache, so they fill in from the stored FIT
  files: newest first, 20 per FIT drain, and on each `get_activity` miss.
- `list_activities` and `get_activity` show `perceived: {feel_pct, rpe}`, null until the FIT file is
  read or when the runner gave none. RPE is shown 0 to 10, rounded to one decimal.
- They sit beside `garmin_derived`, not under it: they are the runner's own answers, not Garmin's
  estimates.
- Health values: they never go to logs, metrics or traces.

## 3. Tests

- Decode: both values, the bounds, not asked, out of range.
- Store: written with the detail, kept by an upsert, cleared by a detail without them.
- MCP: unknown before the FIT file is read; set after `get_activity` reads it; shown by
  `list_activities`.
