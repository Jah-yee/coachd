---
title: Pairing
weight: 1
---

1. Open coachd on the watch. It shows your server's host and a 6-digit code, valid for 10 minutes.
2. Check the host is yours, then confirm the code on `<COACHD_PUBLIC_URL>/devices`, or on the host:

   ```bash
   docker compose exec coachd /coachd device pair <code> --label fenix
   ```

3. Within 3 seconds the watch picks up its token and syncs.

The watch needs its phone connection (Garmin Connect on the phone) for pairing and every sync. Reopening the app within the 10 minutes resumes the same code. If it expires, press START for a new one.

| Message | Meaning |
|---|---|
| Connect your phone | No Bluetooth connection to the phone (-104); retries every 3 s. |
| Not coachd's device API (`<code>`): check the URL | Something other than the device API answered (401, 403 or 404): a wrong URL (such as one ending in `/mcp`), `COACHD_DEVICE_API` off, or the Access [Bypass application](../#access-mode-only-add-a-bypass-application) missing. |
| Too many pairing attempts, try again later | coachd's pairing limits (429); retries after a minute. |
| Pairing failed (`<code>`) | Any other error, with the codes of the [sync messages](../status-messages/); retries every 3 s. |

## Devices

Revoke a lost or replaced watch on the Devices page or on the host; its token stops working at once.

```bash
docker compose exec coachd /coachd device list
docker compose exec coachd /coachd device revoke <id>
```

## Changing the server

A watch's token belongs to the server that issued it. Change **coachd URL** to another server and the app forgets its token and workout map and pairs with the new host; the old server's workouts are removed by the first sync after pairing. Revoke the watch on the old server. Clearing the setting keeps the pairing.

## Unpairing

**Unpair** in the app's menu clears the token. The app's workouts stay on the watch until the next pairing's first sync replaces them. Revoke the watch on the Devices page too.
