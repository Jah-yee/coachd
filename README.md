# coachd

[![CI](https://github.com/stefiix92/coachd/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/stefiix92/coachd/actions/workflows/ci.yml?query=branch%3Amain)
[![Latest release](https://img.shields.io/github/v/release/stefiix92/coachd)](https://github.com/stefiix92/coachd/releases/latest)

> A self-hosted gateway between your AI coach and your Garmin watch. It serves your Garmin data to any AI agent and puts the workout you agreed on onto your watch, but only after you approve it.

Ships as one Docker image that runs anywhere: a Raspberry Pi, a home server or a VPS. One instance serves one runner. Your Garmin key stays on your hardware.

Design: [`docs/design/coachd-design.md`](docs/design/coachd-design.md), plus the built-in auth mode in [`docs/design/builtin-auth.md`](docs/design/builtin-auth.md). Install guide: [`deploy/README.md`](deploy/README.md).

**Status:** early and built by one runner for their own training, but it works end to end: Garmin data in, MCP out, passkey-approved workouts to the watch. Expect rough edges. While the version is 0.x, a minor release may change configuration or behaviour, so read the [changelog](CHANGELOG.md) before upgrading.

<p align="center">
  <img src="docs/images/approve.png" width="250" alt="Approval page on a phone: an agent-proposed threshold workout with its steps, the agent's reason, a warning, and the Approve with passkey button">
  <img src="docs/images/consent.png" width="250" alt="Connect an app page: Claude asks for access, showing its web address, where the browser returns and what the app may do">
  <img src="docs/images/apps.png" width="250" alt="Connected apps page on a phone: each app with its access, when it connected and a Revoke button">
</p>
<p align="center"><sub>Approving a workout, connecting an app (built-in auth) and the connected apps, on a phone. Fake data from a test instance.</sub></p>

---

## Why

- Talking training through with an AI (reviews, how you feel, weather, niggles) works better than a black-box plan like Garmin's Daily Suggested Workouts.
- A plain AI chat can't see your watch data, forgets context between chats, and can't put a workout on your watch.
- An AI must never push anything to your watch on its own.

coachd is a **gateway, not a coach**:
- The AI agent and you decide the training.
- coachd provides the data, keeps the context, warns, and delivers only after your explicit approval.

## What it does

1. **Data in.** It pulls your data from the official [Garmin Connect Personal API](https://developer.garmin.com/gc-developer-program/personal/): activities with their original FIT files, sleep, HRV, resting HR, Body Battery, stress and more. It also computes simple totals (weekly volume, longest run, HRV against your normal range) so the agent doesn't do arithmetic. These are numbers, never verdicts.
   - An activity you delete in Garmin Connect disappears from coachd too, once Garmin confirms it is gone (404). This is a soft delete: the raw JSON and the original FIT file are kept.
2. **Context.** It stores the goal the agent sets and a few durable notes (injuries, availability, preferences), so any agent or new chat starts informed.
3. **Workout out.** The agent proposes a structured workout. coachd checks that it's well-formed and attaches warnings, such as "longer than 110% of your longest run in the last 30 days". Then:
   - it notifies your phone;
   - you approve with a passkey;
   - it delivers through a Connect IQ watch app, or as a FIT file download;
   - a cycling workout with time-based steps also downloads as a Zwift workout file (`.zwo`), with power targets in % of FTP.
4. **Your own rating.** The "How did you feel?" and RPE answers you give on the watch after a workout reach the agent too, read from the FIT file.
5. **Planned vs actual.** When you run a structured workout on the watch, coachd reads the steps back from the activity's FIT file: per executed step, the time, distance, heart rate, pace and power, and the seconds inside, below and above its heart-rate or pace range. It links the activity to the workout it delivered, so the agent can review a week of plans against what you did. This works for Garmin Coach and Garmin Connect workouts too, without the link.

There is no chat UI and no built-in LLM. You talk to Claude (or Claude Duet, ChatGPT, Gemini, Cursor, Open WebUI or a local model), and the agent calls coachd over **MCP**.

## What you need

- **Your own Garmin Connect Personal API key.** It is personal: use it only for your own data, never share it, and never host coachd for someone else with it (Garmin's Supplemental Terms §1). Each runner self-hosts their own instance.
- **A Docker host:** a Raspberry Pi 4/5, a home server or a small VPS (arm64 or amd64).
- **A way in for your agents:**
  - **Private lane:** Tailscale, for agents on your own devices (Claude Code, Cursor, local models), with scoped static tokens.
  - **Public lane** (claude.ai, ChatGPT, the approval page on your phone): a hostname you control, exposed through a Cloudflare Tunnel or a reverse proxy such as Caddy. coachd authenticates it in one of two modes:
    - **Access mode:** Cloudflare Access does the login and MFA, and its Managed OAuth signs in the AI apps.
    - **Built-in mode** (`COACHD_PUBLIC_AUTH=builtin`): coachd does it itself, with your passkey for the page and its own OAuth 2.1 server for the AI apps. No Cloudflare account needed.
- Garmin keys expire (30 days to 1 year). Renew yours on coachd's **Garmin key** page: paste the new key, and coachd checks it and switches without a restart.
- **Optional:** a post-run webhook (`COACHD_WEBHOOK_URL`) that tells your own automation (n8n, Home Assistant, an agent trigger) when a run arrives, with IDs only, so the agent can review it without waiting for the morning chat.
- **Optional:** a fēnix or Forerunner watch for the watch app (from the Connect IQ Store), and an ntfy topic for phone notifications.

Start with [`deploy/README.md`](deploy/README.md).

## Agent tools (MCP)

All of these tools exist today:

| Tool | Kind | Since |
|---|---|---|
| `get_status`, `get_daily_health`, `list_activities`, `get_activity`, `get_summaries` | read | M1 |
| `request_sync` | write (local only) | M1 |
| `get_goal`, `get_notes`, `list_workouts` | read | M2a |
| `set_goal`, `upsert_note`, `delete_note`, `propose_workout`, `cancel_proposal` | write (local only) | M2a |
| `send_message` (a short note shown on the watch app) | write (local only) | 0.4 |
| Approval page (passkey), ntfy notification, FIT download | runner only, not MCP | M2b |

- No tool can approve, deliver or withdraw a workout. Only you can, on coachd's approval page with a passkey.
- The approval page, ntfy notifications and FIT download are available (M2b). A proposal that nobody approves stays `pending` and expires at the end of its date.
- Agents can edit or delete only notes that agents wrote. Your own notes are read-only to them.
- A note can carry `valid_until`, its last day ("travelling until 6 Nov"). After that, `get_notes` leaves it out unless the agent asks for expired notes; the page shows it faded until someone deletes it.
- `get_activity` with detail `laps` shows the planned and executed steps of a structured workout, and `list_workouts` lists the activities that carried out each delivered workout. The link needs the steps the watch recorded to equal the workout coachd delivered, dated within 3 days of it.
- `send_message` stores up to 300 characters for the watch app, which shows it and, with background checks on, notifies you within about 15 minutes. A message is display-only: it cannot carry or change a workout, and it is sent nowhere else (not to ntfy).

## Approval flow

1. The agent calls `propose_workout`; coachd checks it and stores it as `pending`, with warnings.
2. coachd sends a minimal ntfy notice ("Workout for Tue 28 Oct ready to review") if ntfy is configured.
3. You open the approval page (behind Cloudflare Access, or signed in with your passkey in built-in mode), and read the workout, warnings and the agent's rationale (labelled as agent text).
4. You approve with a passkey (Face ID or fingerprint); decline is one tap after a passkey login.
   - When several workouts are waiting (a week's plan), **Review all** on the home page shows each one in full and approves all of them with one passkey prompt. It approves exactly the workouts shown, or none if any changed. To leave one out, decline it first.
5. The watch app picks it up on its next sync and confirms delivery. Without the watch app, download the FIT file from the page and copy it to the watch (`GARMIN/NewFiles`). For a cycling workout whose steps all have a time duration, **Download for Zwift** also offers a `.zwo` file (see below). The first download of either file marks it `delivered`.

### Enrol a passkey

Once, after the public lane works, from `deploy/` on the host:

```bash
docker compose exec coachd /coachd enroll
```

In Access mode, sign in at `<COACHD_PUBLIC_URL>/` through Access first, then open the printed link in
that browser within 10 minutes (Access's login redirect would drop the link's `#token`). In built-in
mode, just open the link. You can enrol more passkeys and revoke them on the page.

### Zwift workouts

A cycling workout whose steps all have a time duration can be downloaded for Zwift. Power targets are in % of FTP, so Zwift applies the FTP set in your Zwift profile; other targets (heart rate, cadence, an effort cue) become free-ride blocks with an on-screen message.

1. On the approved workout's page, click **Download for Zwift**.
2. Copy the `.zwo` file into `Documents/Zwift/Workouts/<your Zwift ID>/` on your Mac (`Documents\Zwift\Workouts\<your Zwift ID>\` on Windows). If the numbered folder is missing, start Zwift on that computer and sign in once.
3. Restart Zwift. The workout is under **Custom Workouts** in the workout list.

Zwift on Apple TV or iPad cannot import files: import on a Mac or PC first, and Zwift's cloud sync brings it to your other devices. A cycling workout with distance, open or heart-rate-bounded steps cannot be a Zwift workout; the page says which step, and the agent gets the same fact as a warning.

### ntfy

Create a topic with default access `deny-all` and an access token for coachd. Set `COACHD_NTFY_URL` to the topic URL, put the token in `deploy/secrets/ntfy_token` (the file must exist; empty is allowed) and subscribe your phone to the topic. See [`deploy/README.md`](deploy/README.md).

## Security

- **Public lane:** either Cloudflare Access (coachd validates the Access JWT on every request) or built-in auth (passkey sessions for the page, coachd's own OAuth 2.1 server with PKCE, rotating refresh tokens and reuse detection for agents). In both, every write on the page needs your passkey, and no agent credential can approve, deliver or withdraw a workout.
- **Private lane:** Tailscale with scoped tokens, for local clients.
- **No open ports to the network.** The only published port is coachd's public listener on loopback (`127.0.0.1:8080`), for a tunnel or reverse proxy on the host.
- **Data handling:**
  - Secrets live in Docker secret files.
  - Data sits on an encrypted disk.
  - The container is distroless, runs as non-root, and has a read-only filesystem.
  - Release images are multi-arch, signed with Sigstore (keyless, from the release workflow) and carry an SBOM and build provenance; you can also build the image yourself (see the install guide).
- **Logs, metrics, traces and profiles** carry IDs, timings and outcomes only, enforced by an allowlist with tests. Never health data, chat content or tokens.
- **Audit log** of every tool call, approval and authentication failure.

## Observability

OpenTelemetry flows to Grafana Alloy, which routes each signal:

| Signal | Stored |
|---|---|
| Metrics | VictoriaMetrics, on the host |
| Logs | VictoriaLogs, on the host |
| Traces | Grafana Cloud Tempo (optional) |
| Profiles | Grafana Cloud Pyroscope (optional) |

- **Viewing:** a local Grafana on the host (optional profile, reached over Tailscale), or Grafana Cloud reading the host stores through Private Data Source Connect.
- **Dashboards and alert rules:** kept in the repo.

## Stack

- **Core:** Go, the official MCP Go SDK, SQLite (WAL, pure Go), `muktihari/fit`, `go-webauthn`, and server-rendered HTML with no npm.
- **Watch app:** Connect IQ, written in Monkey C.
- **Packaging:** a multi-arch image (arm64 and amd64) plus `compose.yaml` with tailscale, Alloy, VictoriaMetrics and VictoriaLogs. cloudflared runs on the host, not in compose; the PDC agent comes with the Grafana Cloud override (`compose.cloud.yaml`).

## Roadmap

- **Done:**
  - Garmin ingest and backfill, summaries, MCP tools, both network lanes, telemetry.
  - Goal, notes, workout proposals with warnings, passkey approval, ntfy, FIT download.
  - Power targets for cycling, and Zwift workout downloads.
  - Connect IQ watch app with device pairing and delivery confirmation (Connect IQ Store, your coachd URL set on the phone; opt-in with `COACHD_DEVICE_API=on`).
  - Watch app v2: the next workout on the main screen, workout details with the agent's rationale, agent messages, background checks with notifications, and a glance (see [`watch/README.md`](watch/README.md)).
  - Backups and restore, a local Grafana profile, dashboards and alerts.
  - Built-in auth, so the public lane works without Cloudflare.
  - Planned vs actual: executed workout steps and their link to the delivered workout.
- **Next:** a smoother first-time install, and wider live testing of AI clients against built-in mode. Changes are listed in [`CHANGELOG.md`](CHANGELOG.md).

## Known risks

- **Garmin Personal API:**
  - Launched 2026-10-21; rate limits are undocumented and may change.
  - The key is personal and for your own use only (Supplemental Terms §1). Hosting coachd for other people would need Garmin's Developer Program.
- **No official self-serve way to push workouts to the watch.** Hence the Connect IQ app, with FIT files as the fallback.
- **The spike warning** rests on a single cohort study (Frandsen 2025). It warns and never blocks.

## Disclaimer

Not affiliated with or endorsed by Garmin. Not medical advice. If something hurts, stop and see a professional.

## Contributing and security

- Contributions are welcome: see [`CONTRIBUTING.md`](CONTRIBUTING.md). Commits need a DCO sign-off.
- Found a vulnerability? Report it privately, as described in [`SECURITY.md`](SECURITY.md). Please don't open a public issue.

## Support

coachd is free and built in spare time. If it helps your training, you can [buy me a coffee on Ko-fi](https://ko-fi.com/stefiix92).

## License

[GNU Affero General Public License v3.0](LICENSE) (AGPL-3.0-only). You may use, modify and self-host coachd freely. If you run a modified version as a service for others, you must publish your changes under the same license.
