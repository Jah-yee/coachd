# coachd: post-run webhook

Status: design, 2026-10-25. Extends `coachd-design.md` §5 (Data in) and §11 (Observability). Tags
as in the main design: **[owner]** = owner decision, **[call]** = designer's call, **[builder]** =
left to the implementer.

## 1. Intent

The runner's conversation with the agent starts in the morning. A run done at 18:00 waits for the
next morning's chat to be reviewed. Planned vs actual (`workout-outcome.md`) makes a review cheap,
but nothing tells anyone that a run arrived.

coachd has no LLM and does not start conversations. **[owner]** What it can do is say "an activity
arrived" to whatever the runner already uses for automation: n8n, Home Assistant, or an agent
platform's trigger. That automation wakes the agent. The owner agreed on 2026-10-25, on condition
that events carry IDs only. **[owner]**

## 2. Goals, non-goals, anti-goals

**Goals**
- One HTTP POST per recent activity, once its FIT file is handled, to a URL the runner configures.
- IDs only, so no health data leaves the host through it.

**Non-goals**
- Faster ingestion. Garmin has no webhooks, and coachd keeps polling conservatively. A watch ping
  on activity completion that triggers an earlier sync needs the watch app's background
  activity-completed event. It is possible later.
- Events for proposals, approvals or deliveries.
- Starting a conversation itself.

**Anti-goals**
- Health values, names, notes or any free text in an event.
- A flood: a backfill of two years of activities must send nothing.
- The URL or token in logs.

## 3. When

After the FIT drain stores an activity's FIT file and its detail, or learns from Garmin that it has
none (404), and only if the activity started within the last 48 hours. **[call]** One event per
activity, since each activity's FIT is handled once. A download that failed sends nothing; when the
retry succeeds, the event follows. Manual entries never get a FIT file and send nothing.

## 4. The event

`POST <COACHD_WEBHOOK_URL>`, `Content-Type: application/json`, optional `Authorization: Bearer
<token>`:

```json
{"event":"activity.stored","activity_id":24745625144,"date":"2026-10-26","sport":"running","proposal_id":null}
```

`proposal_id` is the delivered proposal the activity carried out (`workout-outcome.md` §5), looked up
when the event is queued.

## 5. Delivery

As ntfy: one worker, a queue of 32 that drops when full, three attempts (now, after 5 s, after
25 s). A 2xx is sent, a 4xx other than 429 is rejected at once, anything else is retried. The retry
loop is shared with ntfy. The span is `activity.webhook` with `activity_id` and `outcome`. The
counter is `coachd.webhook.sends{outcome}`. No new keys.

## 6. Configuration

- `COACHD_WEBHOOK_URL`: an absolute http(s) URL without user info. Empty means off. Plain http is
  allowed for a LAN receiver, with a warning at start. **[call]**
- `COACHD_WEBHOOK_TOKEN_FILE`: optional, a secret file. It is not mounted by default, so upgrading
  needs no new file: a compose override adds it.

## 7. Tests

- Webhook: the body and headers, the proposal link, retry on 5xx and 429, reject on 4xx, a full
  queue drops, no URL or token in logs, allowlisted span keys.
- Ingest: a recent stored activity is told after its detail; an old one is not; an unavailable FIT
  is told; a failed download is not; a second drain repeats nothing.
- Config: valid and invalid URLs, the token file, a token path without a URL.
