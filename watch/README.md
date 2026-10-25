# coachd watch app

A Connect IQ watch app for fēnix and Forerunner watches (the models in `manifest.xml`). It pairs
with your coachd once, then copies the workouts you approved onto the watch and tells coachd it
has them. It shows the next workout, each workout's steps and the agent's reasons, and the short
messages your agent sends with `send_message`, and it notifies you of new ones. It only reads
coachd's device API (`/device/v1/*`): it cannot approve, change or withdraw anything.

Server setup (`COACHD_DEVICE_API=on`, and in Access mode the separate Cloudflare Access
application with a Bypass policy for `/device/v1`; pairing and revoking from the host) is in
[`deploy/README.md`](../deploy/README.md), section 10.

Tested with Android phones; iPhone not yet verified.

## Install

1. Install **coachd** from the Connect IQ Store, in the Connect IQ app on your phone or on the
   web.
2. In the Connect IQ app, open coachd's settings and set **coachd URL** to your coachd's public
   URL, the same value as `COACHD_PUBLIC_URL` in `deploy/.env`, for example
   `https://coachd.example.com`.
   - It must be `https://`. Surrounding spaces, a capitalised `Https://` and trailing slashes are
     fine, and a bare host such as `coachd.example.com` means `https://coachd.example.com`.
   - Until a valid URL is set, the watch shows "Set the coachd URL in the Connect IQ app" and
     makes no request.

## Pairing

1. Open coachd on the watch. It shows "Pair with coachd", your server's host, a 6-digit code and
   "Confirm it on coachd's Devices page". The code is valid for 10 minutes.
2. Check the host is your coachd, then confirm the code on `<COACHD_PUBLIC_URL>/devices`
   (passkey session), or from a host shell with
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
| Not coachd's device API (`<code>`): check the URL | coachd's page or Cloudflare Access answered instead of the device API (401, 403 or 404): a wrong URL (such as the MCP URL ending in `/mcp`), `COACHD_DEVICE_API` is off, or in Access mode the `/device/v1` Bypass application is missing. |
| Too many pairing attempts, try again later | coachd's pairing limits (HTTP 429: too many pairings started, or too many polls); the watch retries after a minute. |
| Pairing failed (`<code>`) | Any other error, with the same codes as the sync messages below; the watch retries every 3 s. |

## Changing the server

The token and the pairing in progress belong to the server they came from. When you change
coachd URL to another server, the app forgets them, the workout map and the last sync before it
sends anything, and shows pairing for the new host. A pairing answer that arrives from the old
server afterwards is ignored. The app's workouts from the old server stay on the watch until the
first sync after the new pairing removes them. Revoke the old device on the old coachd's Devices
page.

Clearing the setting, or making it invalid, keeps the pairing: the app makes no request until the
URL is valid again, and the same URL picks up where it left off.

## Using it

The app syncs every time it opens. Its main screen shows the next workout (Today, Tomorrow or
the weekday, its name, and sport, duration and distance), "N new messages" when the agent sent
something you have not read, and the sync status or "Last sync HH:MM" at the bottom.

START opens the menu:

- **The workouts** from today on, in date order (the name; the day and duration). Selecting one
  shows its steps, notes and why the agent proposed it (both labelled as agent text); UP and DOWN
  scroll. START there opens it in the watch's workout player; the watch asks before it leaves the
  app. A workout not downloaded yet says "Sync now" instead.
- **Messages** lists the agent's messages from the last 7 days (at most 5), newest first.
  Opening one marks it and every older one read.
- **Sync now** syncs again.
- **Unpair** clears the token and the workout map (which of the app's workouts is which
  coachd workout). The workouts stay on the watch; the first sync after pairing again removes
  them as unmapped and downloads the desired set afresh. Revoke the old device on coachd's
  Devices page too.

Everything on the screens comes from what the last sync stored, so it works without the phone.

A sync fetches the desired set (approved, delivering, delivery_failed and delivered workouts from today to 14 days ahead),
removes the app's workouts that left it (withdrawn, replaced or past), downloads the missing
ones one at a time and acknowledges each, which marks it `delivered` in coachd. Then it fetches
the details of workouts it has none for, and the messages. The app only ever removes workouts it
downloaded itself. With a coachd older than 0.4 there are no details or messages, and the screens
show names and dates only.

### Notifications

While paired, the app checks coachd in the background every 15 minutes (through the phone, like
a sync). When the agent sent a new message, or a workout you approved is not on the watch yet,
the watch tells you: fēnix 7 and later show a notification, older watches ask whether to open
coachd. One check notifies at most one thing, and never the same thing twice. The check never
downloads: open the app to sync. Turn it off with **Notifications** in coachd's settings in the
Connect IQ app.

On fēnix 7 and later, coachd's glance shows the next workout and unread messages.

Pick one delivery path per workout. A FIT file downloaded from the approval page and copied
over USB is a separate workout on the watch, and the app still downloads its own copy, so the
watch ends up with two. The app removes only its own copy when a workout leaves the set; a USB
copy has to be deleted by hand.

### Status messages

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

Only -104, 401, 429 and the transport and storage errors above (-2, -3, -300, -1000, -1001,
-1004) stop a sync. Any other error on one workout's download or acknowledgement skips that
workout and the sync goes on with the next one; at the end the message shows the first such
code, and the next sync tries the skipped workouts again. A workout downloaded but not
acknowledged is removed and delivered again on the next sync. coachd marks a delivery
`delivery_failed` when it sees no acknowledgement within 15 minutes.

## Development

Building it yourself is for development and for the Store uploads; runners install from the
Store.

### Prerequisites

- **Connect IQ SDK 9.3.0** unpacked at `~/.cache/connectiq/sdk-9.3.0`, or point `CIQ_SDK` at
  another SDK directory, such as one the SDK Manager installed under
  `~/Library/Application Support/Garmin/ConnectIQ/Sdks/`. The builds use
  `$CIQ_SDK/bin/monkeyc`, which runs from the Linux or the macOS SDK alike; the simulator
  (needed only for the tests) runs only from the macOS SDK.
- **Java 17** on `PATH` (monkeyc is a Java program), for example
  `brew install openjdk@17` and `export PATH="/opt/homebrew/opt/openjdk@17/bin:$PATH"`.
- **Device files.** They come from the Connect IQ SDK Manager and need a Garmin developer login:
  sign in and download the devices; they land in
  `~/Library/Application Support/Garmin/ConnectIQ/Devices/`. A sideload or test build needs its
  one device (default `fenix7spro`; a test build falls back to `fenix6pro`). A Store build needs
  every device in `manifest.xml` and lists the missing ones.

### Build

```bash
cd watch
COACHD_PUBLIC_URL=https://coachd.example.com ./build.sh        # bin/coachd.prg, sideload
COACHD_PUBLIC_URL=https://coachd.example.com ./build.sh test   # bin/coachd-test.prg
./build.sh store                                               # bin/coachd.iq, the public Store app
./build.sh store-beta                                          # bin/coachd-beta.iq, the Store beta
```

- **Two app ids.** The public id in `manifest.xml` ("coachd") is used only by `store`. Sideload,
  test and `store-beta` builds use a dev id ("coachd dev"), so they sit next to the Store install
  instead of replacing it; a Store beta cannot use the public app's id. `build.sh` writes the dev
  manifest and `bin/dev.jungle` on every build and never edits tracked files.
- **The URL.** Sideloaded apps get no phone settings, so sideload and test builds compile
  `COACHD_PUBLIC_URL` into `source/Config.mc` (generated from `source/Config.mc.example`,
  git-ignored) as the setting's default; it must be `https://`. Store builds take no URL and
  embed none (`build.sh` refuses a set `COACHD_PUBLIC_URL`).
- `CIQ_DEVICE` picks the device of a sideload or test build: any product in `manifest.xml`.
- Builds use strict type checking (`-l 3`) and show warnings (`-w`). Store builds warn, for each
  watch without watch-app glances (fēnix 5 and 6, the Forerunners), that the `:glance`
  annotation is ignored; that is expected.
- The first build creates the developer key `.secrets/ciq-developer.der` at the repo root
  (RSA 4096, PKCS#8 DER, mode 0600, git-ignored). Every build is signed with it, Store uploads
  included. Keep it and back it up: an update signed with another key is another app.

### Supported devices

`./devices.sh` lists the installed fēnix and Forerunner device files that run watch apps at API
level 3.0 or later and have app workouts and FIT downloads, which the sync needs. Paste its output
into `<iq:products>` in `manifest.xml` after downloading new devices.

### Sideload

The watch is an MTP device, so the Mac needs [OpenMTP](https://openmtp.ganeshrvel.com/)
(`brew install --cask openmtp`) or Android File Transfer.

1. Quit Garmin Express, and any app that grabs USB devices: Chrome with a WebUSB tab or the
   ChatGPT desktop app take the watch for themselves, and OpenMTP then sees nothing.
2. Connect the watch with its USB cable, directly to the Mac: a dock or hub may pass only power.
   Check that the Mac sees it (Garmin's USB vendor id is 0x091e):
   `ioreg -p IOUSB -l -w0 | grep -c '"idVendor" = 2334'` prints 1. If it does but OpenMTP still
   shows nothing, `killall ptpcamerad` and press Refresh.
3. Open the watch in OpenMTP and copy `watch/bin/coachd.prg` into `GARMIN/APPS/`.
4. Disconnect the watch. The app appears in the apps list as "coachd dev".

To update, copy the new `coachd.prg` over the old one. A `coachd.prg` from a build before the
Store release used the public id: it is a separate app, so delete it from `GARMIN/APPS/`. If the
app does not start, look in `GARMIN/APPS/LOGS/CIQ_LOG.YML` on the watch.

### Store upload

The listing text and the upload checklist (beta first, the public app only after the owner
approves the beta) are in [`STORE.md`](STORE.md).

### Tests

The `(:test)` suite covers the pure parts of the sync and the background check (`SyncModel`:
what to download, remove and forget, which errors stop a sync, parsing coachd's answers, what to
notify, dates), `PairingModel` (whether a stored pairing is resumed, and its expiry),
`ServerModel` (normalising the URL setting and when to forget a server) and `Fmt` (durations,
distances, day labels), plus `CoachdApp.reconcile`, the default URL, the 404 message and a stale
pairing answer against the simulator's storage. Smoke tests draw every screen and the glance into
an offscreen bitmap, and run the background check's notification on canned answers. It runs only in the Connect IQ simulator, which needs its GUI and the macOS
SDK (from the SDK Manager):

```bash
export CIQ_SDK="$HOME/Library/Application Support/Garmin/ConnectIQ/Sdks/<macOS SDK 9.3.0 directory>"
COACHD_PUBLIC_URL=https://coachd.example.com ./build.sh test
"$CIQ_SDK/bin/connectiq"                                       # starts the simulator
"$CIQ_SDK/bin/monkeydo" bin/coachd-test.prg fenix7spro -t      # or the device build.sh printed
```

The results list the 40 tests and end with `PASSED`; `monkeydo` exits 1 even then, so read the
last line.

`build.sh test` adds `test/` to the source path; the app build leaves it out.
