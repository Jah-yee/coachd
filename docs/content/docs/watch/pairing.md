---
title: Pairing
weight: 1
---

The watch pairs once and keeps its device token. Pairing needs the watch's phone connection (Garmin Connect on the phone), as every sync does.

{{% steps %}}

### Start pairing on the watch

Open coachd on the watch. It shows "Pair with coachd", your server's host, a 6-digit code and "Confirm it on coachd's Devices page". The code is valid for 10 minutes.

### Confirm the code

Check the host is your coachd, then confirm the code on `<COACHD_PUBLIC_URL>/devices` (passkey session), or from a host shell:

```bash
docker compose exec coachd /coachd device pair <code> --label fenix
```

### Done

Within 3 seconds the watch picks up its token and starts the first sync.

{{% /steps %}}

If the code expires, the watch shows "Pairing expired — press START to retry"; press START for a new code.

The pairing in progress (its id, code and expiry) is kept on the watch, so closing and reopening the app within the 10 minutes resumes the same code and picks up a confirmation made meanwhile, instead of starting another pairing. It is forgotten once paired or expired.

## Pairing messages

| Message | Meaning |
|---|---|
| Connect your phone | No Bluetooth connection to the phone (error -104); the watch retries every 3 s. |
| Not coachd's device API (`<code>`): check the URL | coachd's page or Cloudflare Access answered instead of the device API (401, 403 or 404): a wrong URL (such as the MCP URL ending in `/mcp`), `COACHD_DEVICE_API` is off, or in Access mode the `/device/v1` [Bypass application](../#access-mode-only-add-a-bypass-application) is missing. |
| Too many pairing attempts, try again later | coachd's pairing limits (HTTP 429: too many pairings started, or too many polls); the watch retries after a minute. |
| Pairing failed (`<code>`) | Any other error, with the same codes as the [sync messages](../status-messages/); the watch retries every 3 s. |

## Manage devices

```bash
docker compose exec coachd /coachd device list
docker compose exec coachd /coachd device revoke <id>
```

Revoke a lost or replaced watch on the Devices page or with `device revoke <id>`; its token stops working at once.

## Changing the server

The token and the pairing in progress belong to the server they came from. When you change **coachd URL** to another server, the app forgets them, the workout map and the last sync before it sends anything, and shows pairing for the new host. A pairing answer that arrives from the old server afterwards is ignored. The app's workouts from the old server stay on the watch until the first sync after the new pairing removes them. Revoke the old device on the old coachd's Devices page.

Clearing the setting, or making it invalid, keeps the pairing: the app makes no request until the URL is valid again, and the same URL picks up where it left off.

## Unpairing

**Unpair** in the app's menu clears the token and the workout map (which of the app's workouts is which coachd workout). The workouts stay on the watch; the first sync after pairing again removes them as unmapped and downloads the desired set afresh. Revoke the old device on coachd's Devices page too.
