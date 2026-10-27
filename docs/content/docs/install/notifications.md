---
title: ntfy and webhook
weight: 8
---

Both are optional. ntfy tells your phone when a workout is ready to review; the post-run webhook tells your own automation when a run arrives.

## ntfy

coachd sends a minimal [ntfy](https://ntfy.sh) notice ("Workout for Tue 28 Oct ready to review") when an agent proposes a workout. The body carries only the date, never workout or health data, and its **Review** action opens `<COACHD_PUBLIC_URL>/p/<id>`. In built-in mode coachd also tells you when an app connects, and when it revokes a connection because a token was reused.

ntfy needs the public lane with the page ([Access](../cloudflare-access/) or [built-in](../builtin-auth/) mode) and `COACHD_PUBLIC_URL`.

{{% steps %}}

### Create a protected topic

Create a topic with default access `deny-all` and an access token for coachd, and subscribe your phone to it.

### Set the topic URL

```bash {filename=".env"}
COACHD_NTFY_URL=https://ntfy.sh/<secret-topic>
```

### Write the token file

The token goes in `secrets/ntfy_token`. The file **must exist** because compose mounts it as a secret, even with ntfy unused; it may be empty (a public topic, or ntfy unused). Create it in **one** of two ways:

Either empty (no token):

```bash
umask 077
: > secrets/ntfy_token
```

Or holding the real token (`printf '%s'`: no trailing newline):

```bash
umask 077
read -rs NTFY_TOKEN                     # hidden prompt: stays out of shell history
printf '%s' "$NTFY_TOKEN" > secrets/ntfy_token
```

Then, either way, hand it to coachd (same ownership and mode as `garmin_api_key`):

```bash
docker run --rm --network none -v "$PWD/secrets:/s" --entrypoint sh \
  tailscale/tailscale:v1.104.2@sha256:b11583ad2e93e4671128538c88cf11f460444a5d7e449311dd1933a400ab849d \
  -c 'chown 65532:65532 /s/ntfy_token && chmod 0400 /s/ntfy_token'
```

coachd reads the file only when `COACHD_NTFY_URL` is set.

{{% /steps %}}

To change the token later (rotating it, or filling an empty file), see [changing a secret](../secrets/#changing-a-secret), then run `docker compose up -d --force-recreate coachd`: coachd reads the token only at start.

## Post-run webhook

Set `COACHD_WEBHOOK_URL` and coachd POSTs a small JSON event to your own automation when a recent activity (started in the last 48 hours) is stored, after its FIT file and per-step numbers are in. It carries IDs, a date and a sport only:

```json
{"event":"activity.stored","activity_id":24745625144,"date":"2026-10-26","sport":"running","proposal_id":"cc7b5c9c..."}
```

`proposal_id` is the delivered workout the activity carried out, or `null`.

Point it at an n8n or Home Assistant webhook, or anything that can start your agent. The agent then calls `get_activity` with detail `laps` to review the run, and can `send_message` to the watch.

- coachd retries 5xx and network errors twice (after 5 s and 25 s) and gives up on other 4xx. `coachd_webhook_sends_total` counts outcomes.
- It does not poll Garmin any more often: an activity reaches coachd on the next hourly sync, or sooner when the agent calls `request_sync`.
- Plain `http://` works for a receiver on your LAN; coachd logs a warning, because a token would travel in clear text.

Treat the URL as a secret (an unguessable path). For a bearer token as well, put it in a secret file (owned `65532:65532`, mode `0400`, as for `ntfy_token`) and add a compose override, since compose does not mount it by default:

```yaml {filename="compose.override.yaml"}
services:
  coachd:
    environment:
      COACHD_WEBHOOK_TOKEN_FILE: /run/secrets/webhook_token
    secrets: [webhook_token]
secrets:
  webhook_token:
    file: ./secrets/webhook_token
```
