# coachd: watch app v2, a better screen and agent messages

Status: design, 2026-10-25. Extends `coachd-design.md` §8 (Delivery, Connect IQ watch app) and §9
(Agent interface). Tags as in the main design: **[owner]** = owner decision, **[call]** = designer's
call, **[builder]** = left to the implementer.

## 1. Intent

The watch app is a sync tool today. Its screen says "3 workouts" and "Last sync 07:12"; the workout
itself is two menus deep, with only its name and date, and nothing reaches the watch until the
runner opens the app. **[owner]** asked for a better UI and, possibly, notifications from the agent.

After this change:

1. The app's main screen shows the next workout: the day, its name, the sport and how long it is.
2. Opening a workout shows its steps and what the agent said it is for, and starts it.
3. The agent can send the runner a short message ("Easy today, legs are still heavy from Sunday").
   It shows up on the watch.
4. Every 15 minutes the watch checks coachd in the background and notifies the runner of a new
   message or a new workout to sync.
5. On fēnix 7 and later, the glance shows the next workout.

## 2. Goals, non-goals, anti-goals

**Goals**
- Main screen built around the next workout; the sync state becomes a footer. **[owner, "better UI"]**
- A workout detail screen: steps, the agent's rationale and notes, START to begin. **[call]**
- A `send_message` MCP tool and a messages list on the watch. **[owner, "notifications from the
  agent"; the shape is a call]**
- A background check with a native notification (watches with Connect IQ API 5.1: fēnix 7 and later
  in the app's device list) or an "open coachd?" prompt (the others). **[call]**
- A glance on fēnix 7 and later. **[call]** Older watches in the device list cannot show a watch
  app's glance; the build warns that the `:glance` annotation is ignored for them.

**Non-goals**
- Downloading workouts in the background. The background process cannot store FIT workouts; it
  only notifies, and the runner opens the app to sync.
- Replies, read receipts or any watch-to-agent channel. Messages go one way.
- Messages on the web page or through ntfy. ntfy carries only minimal text (§10 of the main design)
  and agent messages will often mention health values.
- A message history tool for agents (`list_messages`). Possible later.
- Icons per sport, colour themes, settings beyond one on/off switch.

**Anti-goals**
- Any agent path to a workout on the watch. A message is display-only text: it cannot carry,
  start, approve, change or withdraw a workout, and the device API stays read-only apart from the
  existing acknowledgement.
- Message text, rationale or notes in logs, metric labels or spans (the redaction allowlist holds).
- A notification storm: at most one notification per background check.
- A watch that gets slower or stops syncing on older models (128 KB app memory, 32 KB background
  memory).

## 3. Agent messages (server)

**MCP tool `send_message`** (`auth.ScopeWrite`, a local write):

| Field | Meaning |
|---|---|
| `text` | 1 to 300 characters, checked like note text (no control characters but newline) |

- Returns `{"message": {"id", "text", "created_at"}}`. **[call]**
- Description, for agents: shown on the runner's watch the next time it checks (within about 15
  minutes when background checks are on), never sent anywhere else; keep it short; it cannot
  change workouts.
- Storage: table `messages (id, text, created_by, created_at)`, `created_by` as for notes
  (`agent:<lane>:<client>`). After each insert only the newest 20 rows are kept. **[call]**
- The text is never logged or traced; the tool call is logged like every other (tool name and
  outcome).

**Device API `GET /device/v1/messages`** (device token, the per-device rate limit):

```json
{"messages": [{"id": 12, "at": "2026-10-25T07:02:00+02:00", "text": "..."}]}
```

Newest first, at most 5, created within the last 7 days. **[call]** `at` is RFC 3339 in
`COACHD_TIMEZONE`. Read state lives on the watch only.

## 4. Workout detail (server)

**Device API `GET /device/v1/workouts/{id}`**: 404 unless the id is in the current desired set
(as for `/fit`). It records nothing: no status change, no fetch.

```json
{"id": "...", "date": "2026-10-28", "name": "Tempo 5x1 km", "sport": "running",
 "duration_s": 3300, "distance_m": 9000, "partial": false,
 "steps": ["Warm-up · 15:00 · HR 120–140 bpm", "Repeat 5×", "  Active · 1 km · pace 4:05–4:15 /km", "..."],
 "notes": "...", "why": "..."}
```

- `name` is the FIT `wkt_name` (as in the list), `sport` the stored sport.
- `duration_s` and `distance_m` are the validated plan's known part (`workout.Plan`); `partial` is
  true when open or heart-rate-bounded steps make the duration unknown. **[call]**
- `steps` are the approval page's lines (`Describe`, moved from `web` to `workout` so both use one
  formatter), indented two spaces per level, at most 40 lines (the 40th says "+N more"), each cut
  at 120 characters. **[call]**
- `notes` is the workout's notes and `why` the proposal's rationale, each cut at 500 characters
  with "…". Both are agent text; the watch labels them so. **[call]**

The watch fetches the detail once per workout, during the sync, after the FIT downloads (getting
the workouts onto the watch comes first). A proposal never changes after `pending`, so the stored
detail stays valid until the workout leaves the set.

## 5. The watch app

**Main screen** (replaces the status view):
- Header: "coachd". Footer: the sync state (Syncing…, an error, or "Last sync HH:MM"), in grey.
- Body: the next workout on the watch (the first of the desired set, which is dated today or
  later): "Today" / "Tomorrow" / "Wed 29", the name (up to two lines), and "Run · 55 min · 9 km"
  ("~55 min+" when partial). Without one: "No workouts planned".
- Under it, when there are unread messages: "1 new message" (or "N new messages").
- START opens the menu, as today.

**Menu:** the desired workouts from today on, in date order (label: name; sub-label: day and
duration), then "Messages" (sub-label: "N new messages"), "Sync now", "Unpair". Selecting a
workout opens its detail.

**Workout detail:** a scrollable text screen (UP/DOWN scroll a page): day and date, name, sport
line, the step lines, "Notes (agent)" and "Why (agent)". START asks the system to open the workout
in the native player (the existing behaviour); a workout not on the watch yet says "Sync now"
instead. Without a stored detail (an older coachd), it shows the name and date only.

**Messages:** a menu of the stored messages, newest first (label: first words; sub-label: day and
time). Selecting one opens it in the same scrollable text screen, labelled "From your agent".
Opening a message marks it and every older one read (one stored number: the highest read id).

**Sync:** after the desired set and the FIT downloads and acks as today, the sync fetches the
details of workouts it does not have, then `GET /device/v1/messages`. A 404 on detail or messages
(a coachd without this change) is not an error: the sync goes on without them. Errors that stop a
sync (`SyncModel.stopsSync`) stop it; any other error on a detail or the messages is ignored, and
the next sync asks again. Details of workouts that left the set are deleted. A complete sync
leaves the status empty, so the footer shows "Last sync HH:MM".

**Background check** **[call]**:
- Registered while paired and the new phone setting **Notifications** is on (default on; sideloaded
  builds have no settings, so it is on). Every 15 minutes, a temporal event.
- It fetches the desired set and the messages. If there is a message newer than both the highest
  read id and the highest id already notified, it notifies with that message. Otherwise, if the set
  holds workouts that are neither on the watch nor already notified, it notifies "New workout:
  <day> <name>" (the first one; "+N more" when several). One notification per check.
- API 5.1 watches: `Notifications.showNotification` (title "coachd", subtitle "From your agent" or
  "New workout", the text as body, no actions, replacing coachd's previous notification). Older
  watches: `Background.requestApplicationWake` with the text cut to 250 bytes; confirming opens
  the app, which syncs.
- What was notified is kept in storage (3.2+ watches write it from the background; on 3.1 watches
  the background passes it to the app through `Background.exit` and reads its own unconsumed data
  back with `Background.getBackgroundData`). Any error (no phone, 401, 429, a timeout) ends the
  check silently; the next one tries again.
- The background never downloads, acknowledges or removes anything.

**Glance** (fēnix 7 and later): "COACHD", then the next workout's day and name, then its summary
or "N new messages". It reads only what the app stored.

## 6. Security and privacy

- The device API gains two read-only GETs behind the device token. `TestNoRunnerOnlyPaths` keeps
  the device API from writing messages or proposals.
- Agent text (message, rationale, notes, effort cues) reaches the watch through the phone, the same
  path workout names take today. It is never logged or traced; tests check the log output.
- Prompt injection: a Garmin activity name could steer an agent into sending an odd message. The
  watch labels every message as the agent's, cannot follow links, and a message cannot act on
  anything. coachd keeps at most 20 and the watch shows at most 5.
- Permissions on the watch: `Background` and `Notifications` are added; the Store listing says
  what they are for.

## 7. Left to the builder

- Fonts, spacing and colours per screen size, as long as nothing is clipped on the round 208 px to
  466 px screens in `manifest.xml`.
- How the scrollable text screen pages.
- The exact background memory layout, as long as it fits 32 KB. (Built: the background image is
  about 12.5 KB on the 32 KB watches and the glance about 11 KB of 64 KB.)
