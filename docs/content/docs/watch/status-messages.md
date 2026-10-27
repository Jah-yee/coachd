---
title: Status messages
weight: 3
---

What the watch app's status line means. For pairing, see [pairing messages](../pairing/).

| Message | Meaning |
|---|---|
| Set the coachd URL in the Connect IQ app | The URL setting is unset or invalid (not `https://`). |
| Last sync `<HH:MM>` | Everything synced at that time. |
| Connect your phone | No Bluetooth connection to the phone (-104). Connect it, then Sync now. |
| Sync paused, try again in a minute | coachd's rate limit (429). What synced so far is kept. |
| Connection problem (`<code>`), try again | A transport or storage error: -2 (timed out waiting for the phone), -3 (waiting for the server), -300 (request timed out), -1004 (connection dropped), -1000 (watch storage full: delete some workouts or files), -1001 (https required). What synced so far is kept. |
| Sync failed (`<code>`) | Any other error. A positive code is coachd's HTTP status (404: device API off, or the workout left the set; 500: server error). Negative codes are Connect IQ's: -1002 is usually an Access login page instead of coachd, -400 a response the watch couldn't read. |
| Pair with coachd | No token: never paired, unpaired, the URL changed, or coachd revoked it. |

The errors in the "Connection problem" row, -104, 401 and 429 stop a sync. Any other error skips that one workout, the sync goes on, and the next sync retries it. coachd marks a fetched workout `delivery_failed` if it isn't confirmed within 15 minutes.
