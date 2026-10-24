# coachd watch app

A Connect IQ watch app for the fēnix 7S Pro. It pairs with coachd once, then copies the
workouts you approved onto the watch and tells coachd it has them. It only reads coachd's
device API (`/device/v1/*`): it cannot approve, change or withdraw anything.

Server setup (`COACHD_DEVICE_API=on`, the separate Cloudflare Access application with a Bypass
policy for `/device/v1`, pairing and revoking from the host) is in
[`deploy/README.md`](../deploy/README.md), section 10.

## Prerequisites

- **Connect IQ SDK 9.3.0** unpacked at `~/.cache/connectiq/sdk-9.3.0`, or point `CIQ_SDK` at
  another SDK directory, such as one the SDK Manager installed under
  `~/Library/Application Support/Garmin/ConnectIQ/Sdks/`. The builds use
  `$CIQ_SDK/bin/monkeyc`, which runs from the Linux or the macOS SDK alike; the simulator
  (needed only for the tests) runs only from the macOS SDK.
- **Java 17** on `PATH` (monkeyc is a Java program), for example
  `brew install openjdk@17` and `export PATH="/opt/homebrew/opt/openjdk@17/bin:$PATH"`.
- **The `fenix7spro` device file.** Device files come from the Connect IQ SDK Manager and need
  a Garmin developer login. Open the SDK Manager, sign in and download the fēnix 7S Pro
  device; it lands in `~/Library/Application Support/Garmin/ConnectIQ/Devices/fenix7spro`.
  Until it is there, `./build.sh` stops with exit code 2 and "fenix7spro device file
  missing". `CIQ_DEVICE=fenix6pro ./build.sh` builds for `fenix6pro` instead, as a compile
  check only: that `.prg` does not run on the 7S Pro. `./build.sh test` falls back to
  `fenix6pro` on its own. The warning
  `Invalid device id found in the application manifest: 'fenix7spro'` means the device file
  is missing.

## Build

```bash
cd watch
COACHD_PUBLIC_URL=https://coachd.example.com ./build.sh        # bin/coachd.prg
COACHD_PUBLIC_URL=https://coachd.example.com ./build.sh test   # bin/coachd-test.prg
```

- `COACHD_PUBLIC_URL` is coachd's public URL (the same value as in `deploy/.env`). It must be
  `https://`; trailing slashes are dropped. `build.sh` compiles it into `source/Config.mc`
  (generated from `source/Config.mc.example`, git-ignored), so rebuild after changing it.
- `CIQ_DEVICE` (`fenix7spro` or `fenix6pro`) picks the device; unset, a release build needs
  the `fenix7spro` device file and a test build uses it when present, else `fenix6pro`.
- Builds use strict type checking (`-l 3`) and show warnings (`-w`).
- The first build creates the developer key `.secrets/ciq-developer.der` at the repo root
  (RSA 4096, PKCS#8 DER, mode 0600, git-ignored). Every build is signed with it. Keep it and
  back it up: updates of the sideloaded app should be signed with the same key, or the watch
  may treat the update as another app and lose the pairing.

## Sideload

The fēnix 7S Pro is an MTP device, so the Mac needs [OpenMTP](https://openmtp.ganeshrvel.com/)
or Android File Transfer. Quit Garmin Express first.

1. Connect the watch with its USB cable and open it in OpenMTP.
2. Copy `watch/bin/coachd.prg` into `GARMIN/APPS/`.
3. Disconnect the watch. The app appears in the apps list as "coachd".

To update, copy the new `coachd.prg` over the old one. If the app does not start, look in
`GARMIN/APPS/LOGS/CIQ_LOG.YML` on the watch.

## Pairing

1. Open coachd on the watch. It shows "Pair with coachd", a 6-digit code and
   "Confirm it on coachd's Devices page". The code is valid for 10 minutes.
2. Confirm it on `<COACHD_PUBLIC_URL>/devices` (passkey session), or from a host shell with
   `docker compose exec coachd /coachd device pair <code> --label fenix`.
3. Within 3 seconds the watch picks up its token and starts the first sync.

If the code expires, the watch shows "Pairing expired — press START to retry"; press START
for a new code. The watch needs its phone connection (Garmin Connect on the phone) for
pairing and every sync.

The pairing in progress (its id, code and expiry) is kept on the watch, so closing and
reopening the app within the 10 minutes resumes the same code and picks up a confirmation
made meanwhile, instead of starting another pairing. It is forgotten once paired or expired.

| Message | Meaning |
|---|---|
| Connect your phone | No Bluetooth connection to the phone (error -104); the watch retries every 3 s. |
| Too many pairing attempts, try again later | coachd's pairing limits (HTTP 429: too many pairings started, or too many polls); the watch retries after a minute. |
| Pairing failed (`<code>`) | Any other error, with the same codes as the sync messages below; the watch retries every 3 s. |

## Using it

The app syncs every time it opens. START opens the menu:

- **Sync now** syncs again.
- **Workouts** lists the synced workouts in date order (coachd's name, then the date).
  Selecting one opens it in the watch's workout player; the watch asks before it leaves the
  app. The list works without the phone.
- **Unpair** clears the token and the workout map (which of the app's workouts is which
  coachd workout). The workouts stay on the watch; the first sync after pairing again removes
  them as unmapped and downloads the desired set afresh. Revoke the old device on coachd's
  Devices page too.

A sync fetches the desired set (approved, delivering, delivery_failed and delivered workouts from today to 14 days ahead),
removes the app's workouts that left it (withdrawn, replaced or past), downloads the missing
ones one at a time and acknowledges each, which marks it `delivered` in coachd. The app only
ever removes workouts it downloaded itself.

Pick one delivery path per workout. A FIT file downloaded from the approval page and copied
over USB is a separate workout on the watch, and the app still downloads its own copy, so the
watch ends up with two. The app removes only its own copy when a workout leaves the set; a USB
copy has to be deleted by hand.

### Status messages

| Message | Meaning |
|---|---|
| Syncing… | A sync is running. |
| `<N>` workouts / 1 workout | The sync finished: this many workouts from coachd are on the watch. |
| Last sync `<HH:MM>` | The last sync that brought everything over. |
| Connect your phone | No Bluetooth connection to the phone (error -104). Connect it, then Sync now. |
| Sync paused, try again in a minute | coachd's rate limit (HTTP 429). Whatever synced so far is kept. |
| Connection problem (`<code>`), try again | A transport or storage error that would hit every later request too: -2 (timed out waiting for the phone), -3 (timed out waiting for the server), -300 (request timed out), -1004 (connection dropped), -1000 (the watch's storage is full: delete some workouts or files first) or -1001 (https required). Whatever synced so far is kept. |
| Sync failed (`<code>`) | Any other error. A positive code is coachd's HTTP status (404: the device API is off or the workout left the set; 500: a server error). A negative code is a Connect IQ error, such as -1002 (unexpected response type, for example a Cloudflare Access login page instead of coachd) or -400 (a response the watch could not read). |
| Pair with coachd | The watch has no token: never paired, unpaired, or coachd revoked it (a 401 clears the token). |

Only -104, 401, 429 and the transport and storage errors above (-2, -3, -300, -1000, -1001,
-1004) stop a sync. Any other error on one workout's download or acknowledgement skips that
workout and the sync goes on with the next one; at the end the message shows the first such
code, and the next sync tries the skipped workouts again. A workout downloaded but not
acknowledged is removed and delivered again on the next sync. coachd marks a delivery
`delivery_failed` when it sees no acknowledgement within 15 minutes.

## Tests

The `(:test)` suite in `test/SyncModelTest.mc` covers `SyncModel`, the pure part of the sync
(what to download, remove and forget, and which errors stop a sync), and `PairingModel`
(whether a stored pairing is resumed, and its expiry). It runs only in the Connect IQ simulator, which needs
its GUI and the macOS SDK (from the SDK Manager):

```bash
export CIQ_SDK="$HOME/Library/Application Support/Garmin/ConnectIQ/Sdks/<macOS SDK 9.3.0 directory>"
COACHD_PUBLIC_URL=https://coachd.example.com ./build.sh test
"$CIQ_SDK/bin/connectiq"                                       # starts the simulator
"$CIQ_SDK/bin/monkeydo" bin/coachd-test.prg fenix7spro -t      # or fenix6pro
```

Pass the device `build.sh` built for (it prints it). The results list the thirteen tests and end
with `PASSED`.

`build.sh test` adds `test.jungle`, which puts `test/` on the source path; the app build
leaves it out.
