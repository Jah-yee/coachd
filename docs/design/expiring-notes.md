# coachd: notes that expire

Status: design, 2026-10-25. Extends `coachd-design.md` §7 (Context: goal and notes). Tags as in the
main design: **[owner]** = owner decision, **[call]** = designer's call, **[builder]** = left to the
implementer.

## 1. Intent

Notes are meant to be durable facts, but agents also write facts with an end: "felt unwell the
week of 19 Oct, no training", "travelling 2 to 6 Nov". The owner's first notes show it
(2026-10-24). Every new chat reads them as if still true, and the 50-note cap fills with stale
entries that only a cleanup removes.

After this change a note can carry `valid_until`, the last day it holds. After that day,
`get_notes` leaves it out, unless the agent asks for expired notes too.

## 2. Goals, non-goals, anti-goals

**Goals**
- An optional `valid_until` on notes, set by `upsert_note`.
- `get_notes` shows live notes by default, counts the expired ones, and lists them on request.

**Non-goals**
- Deleting expired notes automatically. They stay until an agent or the runner deletes them: a
  note's history may matter to the agent, and coachd never decides that it does not. **[call]**
- A date field in the runner's note form. Runner notes stay durable; the page shows an agent note's
  date and marks it expired. **[call]**
- Structured check-ins. Still out of scope.

**Anti-goals**
- A note hidden without trace. `expired_count` says how many are hidden.

## 3. The field

- `valid_until`: `YYYY-MM-DD` in the runner's timezone, from today up to 366 days ahead. **[call]**
  Omitted, the note has no end.
- A note is expired once today is after `valid_until`. The note still holds on its last day.
- `upsert_note` with `id` replaces category, text and `valid_until`; omitting it there makes the
  note durable. The runner editing a note on the page keeps its `valid_until`.
- Expired notes still count toward the 50-note cap. When the cap is reached, the error says how many
  agent notes are expired, so the agent can delete them first.

## 4. MCP

- `upsert_note` gains `valid_until`.
- `get_notes` gains the input `include_expired` (default false). Its output gains `expired_count`.
  Each note gains `valid_until` (null when none) and `expired`.
- The descriptions tell the agent to use `valid_until` for facts that stop being true, and to omit
  it for durable facts. No advice beyond that.

## 5. Page

- A note with an end shows "until Fri 31 Oct".
- An expired note shows an "expired" pill and sorts after the live notes of its category.

## 6. Tests

- Store: the column round-trips, an agent update replaces it, a runner update keeps it, and the
  migration leaves existing notes durable.
- MCP: the date bounds; live versus expired on the boundary day; `include_expired`; `expired_count`;
  the full-cap message; goldens.
- Web: the label and the pill.
