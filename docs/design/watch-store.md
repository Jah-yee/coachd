# coachd watch app: Connect IQ Store distribution

Status: design, 2026-10-24. Extends `coachd-design.md` §8 (Delivery, Connect IQ watch app), which left
"Whether to submit to the Connect IQ Store" open. Tags as in the main design: **[owner]** = owner
decision, **[call]** = designer's call, **[builder]** = left to the implementer.

## 1. Intent

Installing the watch app today means building it yourself: Connect IQ SDK, Java 17, a device file
behind a Garmin developer login, a signing key, `COACHD_PUBLIC_URL` compiled in, then copying the
`.prg` over USB with OpenMTP. The owner did it and found the USB step the trickiest part (MTP,
other apps grabbing the device, docks that pass no data).

The next runner is a self-hoster who already deployed coachd on their own server and connected an
AI agent; that part is easy. The watch should be as easy:

1. Install **coachd** from the Connect IQ Store (phone or web).
2. In the app's settings in the Connect IQ phone app, enter their coachd URL.
3. Open the app on the watch: it shows a 6-digit code.
4. Confirm the code on their coachd's Devices page (passkey session).
5. Done: approved workouts arrive on the watch.

No SDK, Java, device files, keys, builds or cables.

## 2. Goals, non-goals, anti-goals

**Goals**
- One public Connect IQ Store listing, published under the owner's Garmin developer account. Each
  runner points it at their own coachd. **[owner]**
- coachd's URL is an app setting, edited on the phone, not compiled in. **[owner]**
- Supported watches: the fēnix and Forerunner families, where the device can run the app (§5).
  **[owner]**
- Sideloading keeps working, for the owner and other developers. **[call]**
- Pairing, the device API and the server stay as they are.

**Non-goals**
- Other device families (epix, Enduro, MARQ, Venu, vívoactive, Instinct...) for now.
- Any server change, central directory, discovery, QR-code setup or hosted relay.
- Building or signing the Store package in CI.
- Background sync (the app still syncs in the foreground only).

**Anti-goals** (a failure even if it works)
- The Store build defaulting to the owner's server, or to any server: other runners' watches would
  try to pair with it.
- A device token or pairing id ever sent to a server other than the one it came from.
- Weakening the invariant "no agent path to the watch": the app still only reads `/device/v1/*`.
- Breaking the sideload route or the existing `(:test)` suite.

## 3. The URL setting

- **Setting:** one string app setting, title "coachd URL", shown in the Connect IQ phone app (and
  Garmin Express). Property id and resource layout are **[builder]**.
- **Defaults [call]:**
  - Store build: empty. No URL is ever baked into the Store package.
  - Sideload build: `COACHD_PUBLIC_URL` from the build, because sideloaded apps get no phone
    settings.
- **Normalising and validating [call]:** trim whitespace and drop trailing slashes. Valid means
  `https://`, then a host of letters, digits, `.` and `-`, an optional `:port` and an optional path
  of plain URL characters, the same rule `build.sh` applies today. Type-check the value read from
  properties: the phone app does not always respect the declared setting type. This logic is pure
  and unit-tested in the `(:test)` suite, like `SyncModel` and `PairingModel`.
- **Unset or invalid URL:** the watch shows a message to set the coachd URL in the Connect IQ app,
  and makes no request. A setting change while the app is open takes effect without a restart.

## 4. Binding the pairing to one server

A device token and a pairing in progress belong to the server that issued them. **[call]**

- The app stores, next to the token, the normalised URL it paired with (or started the pairing
  with).
- **On launch and on every settings change**, if the normalised setting differs from the stored
  URL, the app forgets everything tied to the old server before any request:
  - the token, the pairing in progress, the workout map, the cached workout list and the last-sync
    time;
  - a running sync is cancelled, and a response from the old server that arrives later is ignored:
    in particular a late `paired` answer must not store a token;
  - then it shows pairing (or the unset-URL message).
- **Migration:** a token stored by an older build has no stored URL. Treat that as a mismatch and
  forget it; the runner pairs once more. Never adopt the current setting for an old token. **[call]**
- Workouts the app downloaded from the old server stay on the watch until the first sync after the
  next pairing removes them as unmapped, which is what Unpair does today.
- The old device stays registered on the old server until the runner revokes it there; the docs say
  so.
- **The pairing screen shows the server's hostname** under the title, so the runner sees which coachd
  they are pairing with before confirming. **[call]**

## 5. Supported devices

- **Rule [owner: families; call: criteria]:** every fēnix and Forerunner model that:
  - supports the `watchApp` app type at API level 3.0 or later (the manifest's `minApiLevel`);
  - has `Toybox.PersistedContent` app workouts and FIT download via
    `Communications.HTTP_RESPONSE_CONTENT_TYPE_FIT` (the sync depends on both);
  - builds and runs the app (memory) in the simulator.

  Models that fail any of these are left out, not worked around.
- **Picking the list [builder]:** from the SDK device files (`compiler.json`, `*.api.debug.xml`). Many
  current models, such as most newer Forerunners, are not installed yet and come from the SDK
  Manager. The list goes into `manifest.xml`, which today holds only `fenix6pro` and `fenix7spro`.
- **Testing [call]:** every listed model must compile. Run pairing and a sync in the simulator on one
  model per distinct screen shape and resolution, plus the owner's fēnix 7S Pro for real.

## 6. Build and distribution

- **Two app ids [call]:** a Store beta must use a different application id from the public app, and
  a beta cannot later become the public listing (Garmin forum: "Advice for beta versions").
  - **Public id:** the manifest's current id, `061709a442614150928257efd10885ff`, used only by the
    public Store package.
  - **Dev id:** a new id, used by sideload builds and the Store beta, with the app name "coachd dev"
    so the two can sit side by side on one watch. The owner's sideloaded fixes never overwrite the
    Store install, and each app keeps its own storage and pairs as its own device on the server.
  - Swapping the id (and name) must not edit tracked files. **[builder]**
- **`build.sh` modes [call]:**
  - `./build.sh`: a sideload `.prg` for one device, dev id, as today (needs `COACHD_PUBLIC_URL`, now
    used as the setting's default).
  - `./build.sh test`: as today.
  - `./build.sh store`: the public Store package (`bin/coachd.iq`, public id) for every device in the
    manifest. It takes no URL and refuses to embed one. It fails clearly when a listed device file is
    missing.
  - `./build.sh store-beta`: the same package with the dev id (`bin/coachd-beta.iq`).
  - How the sideload default reaches the resources without editing tracked files is **[builder]**
    (today `source/Config.mc` is generated and git-ignored).
- **Signing:** every build, sideload and Store, is signed with `.secrets/ciq-developer.der`. Store
  updates must use the same key, so it must be backed up. The key never leaves the owner's Mac.
- **App version:** set in the manifest and bumped for every Store upload. **[builder]**
- **Upload [call]:** the owner uploads by hand on Garmin's developer dashboard. First
  `coachd-beta.iq` as a **beta** (tick "Beta App"), which only the owner's account can see and
  install, to test the phone-settings chain on the real watch. Only after the owner has approved the
  beta on their watch does `coachd.iq` go up as the public app. **[owner]**
- **The owner's watch today** runs a sideload under the public id. Installing the public Store app
  replaces it and pairs again (§4 migration). After that, sideloads (dev id) are a separate app.
- **Store listing:** draft the listing text in the repo (`watch/STORE.md`): what the app does, that
  it needs your own coachd server, the setup steps from §1, what the permissions are for
  (`Communications`, `PersistedContent`), and that it talks only to the URL you set and sends
  nothing to the developer. The owner pastes it in and adds simulator screenshots. **[call]**

## 7. Docs

- `watch/README.md`: lead with the Store route (install, set URL, pair). Move building, the SDK,
  device files, signing and sideloading (OpenMTP) to a developer section, and add the USB lessons
  learned: quit Garmin Express and any app that grabs USB devices (Chrome with a WebUSB tab, the
  ChatGPT desktop app), plug in directly rather than through a dock, and check that the Mac sees the
  watch at all.
- `deploy/README.md` §10: the server steps stay; the pairing step points at the Store app.
- `coachd-design.md` §8: replace the "Distribution" line with the decision and a link here.
- README "What you need" and the roadmap: the watch app is a Store install.
- `CLAUDE.md` Status: mention this document next to `builtin-auth.md`.

## 8. Security and invariants

- Unchanged server trust: `/device/v1/*` is still authenticated by the device token only and
  rate-limited as today. A public listing means anyone can install the app, but it is useless without
  a coachd server and a passkey-confirmed pairing on it.
- The token stays in `Application.Storage`, never in properties: properties are visible and editable
  from the phone and Garmin Express.
- The token and pairing id are still never printed or drawn. The hostname on the pairing screen is not
  a secret.
- `https://` only (Connect IQ enforces it too, error -1001).

## 9. Testing

- `(:test)` suite: URL normalising and validation (valid, trailing slashes, whitespace, `http://`,
  bad characters, wrong type), and the bind decision (same URL, changed URL, no stored URL with a
  token, empty setting).
- Simulator: change the setting while paired, while pairing and during a sync; check that no request
  reaches the old URL after the change and that a late old response stores nothing.
- Real watch, via the Store beta: set the URL on the phone, pair, sync; change the URL and see the
  app forget and ask to pair again.

## 10. Risks and open verifications

- **Garmin review:** every Store update waits for review (days), so a fix is not instant. Keep
  sideloading as the fast path for the owner.
- **Connect IQ developer agreement:** not yet checked for apps that depend on a user-run server.
  Precedent: GarminHomeAssistant and the openHAB app are Store apps configured the same way.
- **Phone-settings reliability:** forum reports of the phone app sending wrong types or failing to
  save some setting kinds (arrays). One string setting with type checks keeps this small.
- **Support:** the listing is public under the owner's account, so the owner becomes the support
  contact.
- **iPhone [owner]:** every request goes through the phone app, and coachd has only been tested with
  Android. Forum reports show `makeWebRequest` failing differently on iOS (such as -202). The owner
  has no iPhone; a friend tests once the public app is in the Store (a beta is visible only to the
  owner's account). Until then `STORE.md` and `watch/README.md` say "tested with Android; iPhone not
  yet verified", and they are updated after the test.
- **Device coverage:** the PersistedContent and FIT-download capabilities per model are read from SDK
  files and the simulator; a model could still misbehave on real hardware.

## 11. Left to the builder

- Property ids, resource file layout and string wording beyond §3.
- Where the binding check lives and how late responses are ignored.
- How `build.sh` injects the sideload default.
- The exact device list under the §5 rule.
- App versioning scheme.
