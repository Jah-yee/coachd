---
title: Status messages
weight: 3
---

What the watch app's status line means, and what to do about it. For messages during pairing, see [pairing messages](../pairing/#pairing-messages).

| Message | Meaning |
|---|---|
| Set the coachd URL in the Connect IQ app | coachd URL is unset or invalid (not `https://`, or odd characters). |
| Syncing… | A sync is running. |
| Last sync `<HH:MM>` | The last sync that brought everything over (shown when no other message is). |
| Connect your phone | No Bluetooth connection to the phone (error -104). Connect it, then Sync now. |
| Sync paused, try again in a minute | coachd's rate limit (HTTP 429). Whatever synced so far is kept. |
| Connection problem (`<code>`), try again | A transport or storage error that would hit every later request too: -2 (timed out waiting for the phone), -3 (timed out waiting for the server), -300 (request timed out), -1004 (connection dropped), -1000 (the watch's storage is full: delete some workouts or files first) or -1001 (https required). Whatever synced so far is kept. |
| Sync failed (`<code>`) | Any other error. A positive code is coachd's HTTP status (404: the device API is off or the workout left the set; 500: a server error). A negative code is a Connect IQ error, such as -1002 (unexpected response type, for example a Cloudflare Access login page instead of coachd) or -400 (a response the watch could not read). |
| Pair with coachd | The watch has no token: never paired, unpaired, the URL changed, or coachd revoked it (a 401 clears the token). |

## Which errors stop a sync

Only -104, 401, 429 and the transport and storage errors above (-2, -3, -300, -1000, -1001, -1004) stop a sync.

Any other error on one workout's download or acknowledgement skips that workout and the sync goes on with the next one. At the end the message shows the first such code, and the next sync tries the skipped workouts again.

A workout downloaded but not acknowledged is removed and delivered again on the next sync. coachd marks a delivery `delivery_failed` when it sees no acknowledgement within 15 minutes.
