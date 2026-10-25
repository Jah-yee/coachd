# Watch app via Connect IQ Store: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The coachd watch app installs from the Connect IQ Store and is pointed at a runner's own coachd by a phone setting, with the pairing bound to that one server.

**Architecture:** A pure `ServerModel` module normalises the URL setting and decides when stored state must be forgotten. The app stores the server URL it paired with (`Storage` key `"server"`), sends authenticated requests only there, and reconciles on launch and on `onSettingsChanged`. `build.sh` gains Store modes and a dev app id. The manifest lists every qualifying fēnix and Forerunner model.

**Tech Stack:** Monkey C, Connect IQ SDK 9.3.0 (strict typecheck `-l 3`, `-w`), Run No Evil `(:test)` suite in the simulator, bash.

**Spec:** `docs/design/watch-store.md` (approved 2026-10-24). Context: `docs/design/coachd-design.md` §8, `watch/README.md`.

## Global Constraints

- `https://` only. A URL is valid when it is `https://`, a host of letters, digits, `.` and `-`, an optional `:port` and an optional path of `A-Za-z0-9._~/-`. Trailing slashes are dropped.
- The Store package embeds **no** URL. `./build.sh store` and `./build.sh store-beta` refuse a set `COACHD_PUBLIC_URL`.
- The device token and the pairing id are never printed, drawn, or put in properties. The token stays in `Application.Storage`.
- A token or pairing id goes only to the server stored with it (`Storage` key `"server"`).
- Public app id: `061709a442614150928257efd10885ff`, name "coachd". Dev app id: `8643d4638c924746b3031c79d74c19bb`, name "coachd dev" (sideload, test and Store beta builds).
- `minApiLevel` stays `3.0.0`. Builds keep `-l 3 -w`; no new warnings.
- Devices: only the fēnix and Forerunner families.
- The server and its device API are unchanged. No Go code changes.
- Commits: Conventional Commits, DCO sign-off (`git commit -s`), ending with the session's `Co-Authored-By` and `Claude-Session` lines.

## Review Focus

1. **The phone keyboard capitalises the first letter and adds a trailing space** (`"Https://Coachd.example.com/ "`): accept it and normalise to `https://coachd.example.com`. Pinned in Task 1, `serverNormalizeAccepts`.
2. **The runner types a bare host** (`coachd.example.com`): treat it as `https://coachd.example.com`. Pinned in Task 1, `serverNormalizeAccepts`.
3. **The runner pastes a URL that does not reach the device API** (a typo, the `/mcp` connector URL, or `COACHD_DEVICE_API` off): pairing gets 404 and the watch must say what to check rather than show a bare code. Pinned in Task 2, Step 7 (`PairNotFound`) and the simulator check in Step 9.
4. **The runner clears the setting by accident, then restores the same URL:** the pairing survives, and no request is made while it is empty. Pinned in Task 1, `serverMustForget`, and in Task 2, Step 9.
5. **The setting changes while a pairing poll is in flight:** a late `paired` answer from the old server stores no token. Pinned in Task 2, Step 6 (the `_server` guard) and Step 9.

## How to run the watch tests

The `(:test)` suite runs only in the simulator (macOS SDK, GUI):

```bash
cd watch
export CIQ_SDK="$HOME/Library/Application Support/Garmin/ConnectIQ/Sdks/connectiq-sdk-mac-9.3.0-2026-10-20-95888f262"
COACHD_PUBLIC_URL=https://coachd.example.com ./build.sh test
"$CIQ_SDK/bin/connectiq" &                                  # once; leave it running
"$CIQ_SDK/bin/monkeydo" bin/coachd-test.prg fenix7spro -t
```

PASSED means every test is listed as passed and the run ends with `PASSED`. Today there are 13 tests.

---

### Task 1: `ServerModel`: normalise, forget-decision, host

**Files:**
- Create: `watch/source/ServerModel.mc`
- Test: `watch/test/SyncModelTest.mc` (append; the file already holds the `PairingModel` tests)

**Interfaces:**
- Produces:
  - `ServerModel.normalize(raw as Object or Null) as String or Null`
  - `ServerModel.mustForget(current as String or Null, bound as Object or Null, hasState as Boolean) as Boolean`
  - `ServerModel.host(url as String) as String`

- [ ] **Step 1: Write the failing tests** (append to `watch/test/SyncModelTest.mc`)

```monkeyc
(:test)
function serverNormalizeAccepts(logger as Logger) as Boolean {
    var cases = [
        ["https://coachd.example.com", "https://coachd.example.com"],
        ["https://coachd.example.com/", "https://coachd.example.com"],
        ["  Https://Coachd.Example.com//  ", "https://coachd.example.com"],
        ["coachd.example.com", "https://coachd.example.com"],
        ["HTTPS://coachd.example.com:8443/base/", "https://coachd.example.com:8443/base"],
        ["https://coachd.example.com/Base_1.x~y-z", "https://coachd.example.com/Base_1.x~y-z"]
    ];
    var ok = true;
    for (var i = 0; i < cases.size(); i++) {
        var got = ServerModel.normalize(cases[i][0]);
        if (got == null || !got.equals(cases[i][1])) {
            logger.error("normalize(" + cases[i][0] + "): got " + got + ", want " + cases[i][1]);
            ok = false;
        }
    }
    return ok;
}

(:test)
function serverNormalizeRejects(logger as Logger) as Boolean {
    var bad = [null, 42, "", "   ", "http://coachd.example.com", "ftp://coachd.example.com",
        "https://", "https:///path", "https://coachd example.com", "https://coachd.example.com:port",
        "https://coachd.example.com:123456", "https://coachd.example.com/a?b=c",
        "https://user@coachd.example.com", "https://coachd.example.com/#x"];
    var ok = true;
    for (var i = 0; i < bad.size(); i++) {
        var got = ServerModel.normalize(bad[i]);
        if (got != null) {
            logger.error("normalize(" + bad[i] + "): got " + got + ", want null");
            ok = false;
        }
    }
    return ok;
}

(:test)
function serverMustForget(logger as Logger) as Boolean {
    var a = "https://a.example.com";
    var b = "https://b.example.com";
    var ok = true;
    // [current, bound, hasState, want]
    var cases = [
        [a, a, true, false],     // same server: keep
        [b, a, true, true],      // changed server: forget
        [a, null, true, true],   // token from an older build, no stored server: forget
        [null, null, true, true],
        [a, 42, true, true],     // stored server of the wrong type: forget
        [null, a, true, false],  // setting cleared: keep, no request is made
        [b, a, false, false],    // nothing stored: nothing to forget
        [a, null, false, false]
    ];
    for (var i = 0; i < cases.size(); i++) {
        var c = cases[i];
        var got = ServerModel.mustForget(c[0] as String or Null, c[1], c[2] as Boolean);
        if (got != c[3]) {
            logger.error("mustForget case " + i + ": got " + got);
            ok = false;
        }
    }
    return ok;
}

(:test)
function serverHost(logger as Logger) as Boolean {
    return ServerModel.host("https://coachd.example.com").equals("coachd.example.com")
        && ServerModel.host("https://coachd.example.com:8443/base").equals("coachd.example.com:8443");
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: the commands in "How to run the watch tests".
Expected: the build fails with an undefined-symbol error for `ServerModel`.

- [ ] **Step 3: Implement `watch/source/ServerModel.mc`**

This is a pure module in the style of `PairingModel`: no Toybox I/O, and a header comment saying the `(:test)` suite covers it.

`normalize` does these steps in order. Anything outside the rules returns `null`.
1. A value that is not a `String` returns `null`.
2. Trim spaces, tabs, `\r` and `\n` from both ends. An empty string returns `null`.
3. With no `://`, prepend `https://`. Otherwise the scheme, compared case-insensitively, must be `https`.
4. Split the rest at the first `/` into the authority and the path.
5. The authority is the host, optionally followed by `:port`.
   - The host is lower-cased and must be non-empty, using only `a-z0-9.-`.
   - A port is 1 to 5 digits.
6. The path may use only `A-Za-z0-9._~/-`, and its case is kept. Drop its trailing slashes.
7. Return `"https://" + authority + path`.

Monkey C has no regular expressions or `trim`: walk `toCharArray()`.

`mustForget` returns:
- `false` when `!hasState`;
- otherwise `true` when `bound` is not a `String`;
- otherwise `false` when `current == null`;
- otherwise `!current.equals(bound)`.

`host` returns the text between `https://` and the first following `/`, or the end of the string.

- [ ] **Step 4: Run the tests to verify they pass**

Run: the commands in "How to run the watch tests".
Expected: 17 tests, all passed, `PASSED`.

- [ ] **Step 5: Commit**

```bash
git add watch/source/ServerModel.mc watch/test/SyncModelTest.mc
git commit -s -m "feat(watch): ServerModel normalises the coachd URL setting"
```

---

### Task 2: URL setting, server binding and setup screen

**Files:**
- Create: `watch/resources/settings/properties.xml`, `watch/resources/settings/settings.xml`, `watch/source/SetupView.mc`
- Modify:
  - `watch/source/Config.mc.example` (`SERVER_URL` becomes `DEFAULT_URL`)
  - `watch/source/CoachdApp.mc` (the `Store` additions, a `Server` module and the app methods)
  - `watch/source/Api.mc` (an explicit `base` parameter)
  - `watch/source/PairingView.mc`, `watch/source/Sync.mc`, `watch/source/StatusView.mc`
  - `watch/resources/strings/strings.xml`

**Interfaces:**
- Consumes: `ServerModel.normalize`, `ServerModel.mustForget`, `ServerModel.host` (Task 1).
- Produces:
  - Property `serverUrl` (string, default `""`).
  - `Config.DEFAULT_URL as String`: what `build.sh` writes. Task 3 writes `""` for Store builds.
  - `Server.current() as String or Null`
  - `Store.server() as String or Null`, `Store.setServer(url as String) as Void`, `Store.hasState() as Boolean`, `Store.forgetServer() as Void`
  - `Api.fetchDesired(base, cb)`, `Api.downloadFit(base, id, cb)`, `Api.ack(base, id, cb)`, `Api.startPairing(base, model, cb)`, `Api.pollPairing(base, pairingId, cb)`, each with `base as String` first; `Api.url(base as String, path as String) as String`
  - `CoachdApp.reconcile() as Boolean`, `CoachdApp.startView() as [Views, InputDelegates]`, `CoachdApp.showStart() as Void`, `CoachdApp.onSettingsChanged() as Void`
  - `SetupView`
  - Strings: `SettingUrlTitle`, `SettingUrlPrompt`, `SetupUrl`, `PairNotFound`, `AppNameDev`

- [ ] **Step 1: Add the setting resources and strings**

- `properties.xml`: `<properties><property id="serverUrl" type="string"></property></properties>`.
- `settings.xml`: one `<setting propertyKey="@Properties.serverUrl" title="@Strings.SettingUrlTitle" prompt="@Strings.SettingUrlPrompt">` with `<settingConfig type="url"/>`.

Add these to `strings.xml` exactly:
- `SettingUrlTitle`: `coachd URL`
- `SettingUrlPrompt`: `https://coachd.example.com`
- `SetupUrl`: `Set the coachd URL in the Connect IQ app`
- `PairNotFound`: `coachd not found (404): check the URL`
- `AppNameDev`: `coachd dev`

- [ ] **Step 2: Rename the compiled default**

In `Config.mc.example`, `const SERVER_URL` becomes `const DEFAULT_URL`, and its comment says: "the setting's default for sideloaded builds; empty in Store builds". `build.sh`'s `sed` replaces the URL literal, so it needs no change in this task.

- [ ] **Step 3: Add `Server.current()` and the `Store` binding** (in `CoachdApp.mc`)

- `Server.current()`:
  - Read `Properties.getValue("serverUrl")` inside `try`/`catch`; any exception counts as unset.
  - When the value is `null` or `""`, return `ServerModel.normalize(Config.DEFAULT_URL)`.
  - Otherwise return `ServerModel.normalize(value)`. An invalid setting never falls back to the default.
- `Store.server()`, `Store.setServer(url)`: the `"server"` key, with a type check as in `Store.token()`.
- `Store.hasState()`: true when there is a token, a stored `"pairingId"`, or a non-empty map.
- `Store.forgetServer()`: deletes `"token"`, `"map"`, `"workouts"`, `"lastSync"`, `"server"` and the three pairing keys.
- Update the Storage key list comment above `module Store`.

- [ ] **Step 4: Bind `Api` to an explicit base**

Every `Api` function takes `base as String` first, and `url(base, path)` returns `base + "/device/v1" + path`. Callers pass `Store.server()` (the bound server) for every request except `startPairing`, which gets the URL it is about to bind. Update the module comment to say a token only goes to the server stored with it.

- [ ] **Step 5: Reconcile in `CoachdApp`**

- `reconcile()`:
  - Call `ServerModel.mustForget(Server.current(), Storage.getValue("server"), Store.hasState())`.
  - If it is true, call `cancelSync()` and then `Store.forgetServer()`.
  - Return whether it forgot.
- `startView()` returns:
  - `[new SetupView(), new WatchUi.BehaviorDelegate()]` when `Server.current() == null`;
  - otherwise the pairing view and its delegate when there is no token;
  - otherwise the status view and its delegate.
- `getInitialView()` calls `reconcile()` and returns `startView()`.
- `onSettingsChanged()` calls `reconcile()` and then `showStart()`, which calls `WatchUi.switchToView` on `startView()` with `SLIDE_IMMEDIATE`.
- `startSync()` returns early via `showStart()` when `Server.current() == null` or `Store.server() == null`.

- [ ] **Step 6: Pairing binds the server and ignores stale answers** (`PairingView.mc`)

- Add `private var _server as String or Null`.
- `begin()`:
  - Get `url = Server.current()`. If it is `null`, call `getApp().showStart()` and return.
  - Call `Store.setServer(url)`, set `_server = url`, then call `Api.startPairing(url, ...)`.
- Resuming a stored pairing in `onShow` sets `_server = Store.server()`.
- `poll()` calls `Api.pollPairing(_server, ...)`.
- First thing in `onStarted` and `onPolled`: if `_server == null` or `Store.server()` does not equal `_server`, clear `_inFlight` and return without touching `Storage` or the screen.
- `onUpdate` draws `ServerModel.host(_server)` (when it is set) in `FONT_XTINY` and light grey, between the title and the code.

- [ ] **Step 7: Name the 404**

In `Ui.pairingErrorText`, code `404` returns `text(Rez.Strings.PairNotFound)`. Leave the other codes as they are.

- [ ] **Step 8: Sync and status use the bound server**

- `Sync` keeps `_base` (`Store.server()`, taken in `start()`) and passes it to every `Api` call. If it is `null`, `start()` calls `getApp().showStart()` and returns without a request.
- `StatusView.onShow` calls `app.showStart()` and returns when `Server.current() == null`; the existing no-token check follows.
- Create `SetupView.mc`:
  - It draws `AppName` at `h / 6` and `SetupUrl` wrapped at `h / 2`, like `StatusView`.
  - Its `onShow` calls `getApp().showStart()` when `Server.current() != null`.

- [ ] **Step 9: Build and check in the simulator**

Run: the commands in "How to run the watch tests". Expected: 17 tests, `PASSED`, and no new compiler warnings.

Then run the app build:
```bash
COACHD_PUBLIC_URL=https://coachd.example.com ./build.sh
"$CIQ_SDK/bin/monkeydo" bin/coachd.prg fenix7spro
```

In the simulator, edit the `serverUrl` property with File → Edit Persistent Storage → Application.Properties, and watch the requests in the simulator's network traffic view. Check:
- The host `coachd.example.com` shows on the pairing screen.
- Setting `serverUrl` to `https://other.example.com` while the code shows: the stored pairing is cleared, a new pairing starts against `other.example.com`, and no later request goes to `coachd.example.com`.
- Changing `serverUrl` within 3 s of a poll: the late answer changes nothing on screen or in storage.
- Clearing `serverUrl`: the default URL applies (sideload build), and the pairing is kept.
- Setting `serverUrl` to `http://x`: the setup screen shows, with no requests.
- A URL that answers 404: the pairing screen shows "coachd not found (404): check the URL".
- Paired, with a sync running: changing `serverUrl` cancels the sync, clears the token, map, workouts and last sync, and shows pairing for the new host. The cancelled sync's late answers change nothing.

- [ ] **Step 10: Commit**

```bash
git add watch/source watch/resources
git commit -s -m "feat(watch): coachd URL as a phone setting, bound to the pairing"
```

---

### Task 3: `build.sh` Store modes and the dev app id

**Files:**
- Modify: `watch/build.sh`
- Generated (git-ignored, under `watch/bin/`): `manifest-dev.xml`, `dev.jungle`

**Interfaces:**
- Consumes: `Config.DEFAULT_URL` (Task 2), `@Strings.AppNameDev` (Task 2).
- Produces:
  - `./build.sh` → `bin/coachd.prg`: sideload, dev id, one device.
  - `./build.sh test` → `bin/coachd-test.prg`: dev id.
  - `./build.sh store` → `bin/coachd.iq`: public id, every manifest product.
  - `./build.sh store-beta` → `bin/coachd-beta.iq`: dev id, every manifest product.

- [ ] **Step 1: Modes and URL handling**

- Usage: `build.sh [test|store|store-beta]`.
- Sideload and test keep today's `COACHD_PUBLIC_URL` validation and write it into `DEFAULT_URL`.
- `store` and `store-beta` stop with exit 2 and `store builds take no URL: unset COACHD_PUBLIC_URL` when it is set. They write `DEFAULT_URL = ""`. After writing, check that `source/Config.mc` holds the expected literal (as today's `grep -qF`).

- [ ] **Step 2: The dev id without touching tracked files**

- For every mode except `store`, write `bin/manifest-dev.xml` from `manifest.xml`:
  - replace the application id with `8643d4638c924746b3031c79d74c19bb`;
  - replace `name="@Strings.AppName"` with `name="@Strings.AppNameDev"`;
  - check that both replacements happened.
- Write `bin/dev.jungle` with `project.manifest = <absolute path>/bin/manifest-dev.xml`, and pass it after `monkey.jungle` (and after `test.jungle` in test mode) in `-f`.
- Confirm in Step 4 that the later jungle wins. If it does not, generate a complete jungle instead.

- [ ] **Step 3: Devices and export**

- `CIQ_DEVICE` (sideload and test) accepts any product id in `manifest.xml`. The default stays `fenix7spro`, and test mode keeps its `fenix6pro` fallback.
- Store modes:
  - Read every `<iq:product id>` from the manifest in use.
  - Stop with exit 2 listing those without a directory in `$devices` (`install them with the Connect IQ SDK Manager`).
  - Run `monkeyc -e -r -y "$key" -l 3 -w -f <jungles> -o bin/coachd.iq` (or `bin/coachd-beta.iq`).
- Update the header comment to document the four modes and the two ids.

- [ ] **Step 4: Verify**

```bash
cd watch
COACHD_PUBLIC_URL=https://coachd.example.com ./build.sh            # bin/coachd.prg
COACHD_PUBLIC_URL=https://coachd.example.com ./build.sh store; echo "exit $?"   # exit 2
./build.sh store && unzip -l bin/coachd.iq                         # one entry per product
unzip -p bin/coachd.iq manifest.xml | grep -c 061709a442614150928257efd10885ff   # >= 1
./build.sh store-beta && unzip -p bin/coachd-beta.iq manifest.xml | grep -c 8643d4638c924746b3031c79d74c19bb  # >= 1
unzip -p bin/coachd.iq | grep -a -c coachd.example.com             # 0: no URL in the Store package
git status --short                                                 # nothing besides build.sh
```

- [ ] **Step 5: Commit**

```bash
git add watch/build.sh
git commit -s -m "build(watch): store and store-beta modes, dev app id for sideloads"
```

---

### Task 4: fēnix and Forerunner device list

**Files:**
- Create: `watch/devices.sh`
- Modify: `watch/manifest.xml` (`<iq:products>`, and remove the "compile proxy" comment)

**Interfaces:**
- Consumes: `./build.sh store` (Task 3).
- Produces: `watch/devices.sh`, which prints one qualifying device id per line.

- [ ] **Step 1: Owner step: download the device files**

In the Connect IQ SDK Manager (GUI, Garmin login), download every fēnix and Forerunner device. Many current Forerunners (such as 165, 255, 265, 570, 955, 965 and 970) are not installed yet. **Stop and ask the owner to do this.**

- [ ] **Step 2: Write `watch/devices.sh`**

It scans `$HOME/Library/Application Support/Garmin/ConnectIQ/Devices` for directories matching `fenix*` or `fr*`, skipping ids containing `preview`. It prints an id when all of these hold:
- `compiler.json` `appTypes` includes `watchApp`;
- the highest `partNumbers[].connectIQVersion` is at least `3.0.0`;
- `<id>.api.debug.xml` contains `getAppWorkouts`.

Parse the JSON with `python3 -I`. Output is sorted.

- [ ] **Step 3: Update the manifest**

Replace `<iq:products>` with one `<iq:product id>` per line of `./devices.sh`.

- [ ] **Step 4: Verify**

Run: `./build.sh store`. Expected: exit 0, with `bin/coachd.iq` holding every product.

For each distinct `resolution` and `displayType` group among the listed devices (from `compiler.json`), pick one device. Run `COACHD_PUBLIC_URL=https://coachd.example.com CIQ_DEVICE=<id> ./build.sh`, then `monkeydo bin/coachd.prg <id>`. Check that the setup, pairing (code and host fit on screen) and status screens draw without clipping. Drop any device that fails to build or run, and note it in the commit message.

- [ ] **Step 5: Commit**

```bash
git add watch/devices.sh watch/manifest.xml
git commit -s -m "feat(watch): support fēnix and Forerunner models with app workouts"
```

---

### Task 5: Docs and Store listing

**Files:**
- Create: `watch/STORE.md`
- Modify:
  - `watch/README.md`, `deploy/README.md` (§10, lines ~405–455)
  - `README.md` (lines 51, 77, 133)
  - `docs/design/coachd-design.md` (§8 "Distribution" line 286, and the "Store installs" line 284)
  - `CLAUDE.md` (Status), `CHANGELOG.md` (`[Unreleased]`)

**Interfaces:**
- Consumes: the behaviour from Tasks 2–4 and the strings from Task 2.

- [ ] **Step 1: `watch/STORE.md`**

The listing draft:
- a short description: delivers the workouts you approved in your self-hosted coachd to your watch;
- "needs your own coachd server";
- the setup steps (spec §1);
- what each permission is for (`Communications`: talks to your coachd through the phone; `PersistedContent`: stores the workouts);
- privacy: talks only to the URL you set and sends nothing to the developer;
- "tested with Android; iPhone not yet verified";
- links to the repo and `deploy/README.md` §10.

Then the owner's checklist:
1. Build with `./build.sh store-beta`.
2. Upload as a beta (tick "Beta App"), entering a version higher than the last upload. The manifest carries none.
3. Install from the beta link with the owner's account.
4. Run the real-watch checks (Task 6).
5. Only after the owner approves the beta: run `./build.sh store` and upload as the public app.
6. After the iPhone test, update the iPhone line.

- [ ] **Step 2: `watch/README.md`**

- Lead with **Install** (the Store, then set "coachd URL" in the Connect IQ app's settings for coachd), then **Pairing** (as today, plus "the pairing screen shows the server's host"), **Changing the server** (the app forgets the old pairing; revoke the old device on the old server), **Using it** and **Status messages**. Add `PairNotFound` and `SetupUrl` to the message tables.
- Move **Prerequisites**, **Build** (all four modes and the two ids), **Sideload** and **Tests** under a **Development** section.
- Under Sideload, add the USB lessons:
  - quit Garmin Express and any app that grabs USB devices (Chrome with a WebUSB tab, the ChatGPT desktop app);
  - plug in directly, not through a dock;
  - check that the Mac sees the watch: `ioreg -p IOUSB -l -w0 | grep -c '"idVendor" = 2334'`, where Garmin is 0x091e;
  - run `killall ptpcamerad` if OpenMTP still sees nothing.
- Also under Sideload: sideload builds now use the dev id and appear as "coachd dev". A `coachd.prg` from an older build, which uses the public id, is a separate app: delete it from `GARMIN/APPS/`.
- Update the test count to 17.
- Replace "for the fēnix 7S Pro" with "for fēnix and Forerunner watches (see `manifest.xml`)".

- [ ] **Step 3: The other docs**

- `deploy/README.md` §10: step 3 starts with "Install coachd from the Connect IQ Store and set its coachd URL"; building and sideloading become a pointer to `watch/README.md`'s Development section.
- `README.md`: "a fēnix or Forerunner watch for the watch app (Connect IQ Store)", and the roadmap line says Store distribution.
- `coachd-design.md` §8: the Distribution line becomes "Connect IQ Store, public listing under the owner's account, with the coachd URL as a phone setting; sideloading for developers. See `watch-store.md`. [owner 2026-10-24]".
- `CLAUDE.md`: add `docs/design/watch-store.md` next to `builtin-auth.md`.
- `CHANGELOG.md` `[Unreleased]` → `### Changed`: "Watch app: the coachd URL is a setting in the Connect IQ phone app; changing it forgets the pairing; fēnix and Forerunner models; Store packages via `build.sh store`. Existing sideloaded installs pair again once."

- [ ] **Step 4: Verify**

Run: `grep -rn "SERVER_URL\|fenix7spro is the target\|compile proxy" watch docs README.md deploy/README.md`. Expected: no matches.

Every link in the changed files points at an existing file or anchor. Check with `grep -o '](\S*)'` and look at each.

- [ ] **Step 5: Commit**

```bash
git add watch/STORE.md watch/README.md deploy/README.md README.md docs/design/coachd-design.md CLAUDE.md CHANGELOG.md
git commit -s -m "docs: watch app from the Connect IQ Store"
```

---

### Task 6: Owner acceptance on the real watch (beta)

No code. **Stop and hand over to the owner** with this checklist (it is also in `watch/STORE.md`):

- [ ] Run `./build.sh store-beta` and upload `bin/coachd-beta.iq` on the Garmin developer dashboard as a beta.
- [ ] Install "coachd dev" from the beta link on the fēnix 7S Pro.
- [ ] In the Connect IQ phone app, set coachd URL to the real `COACHD_PUBLIC_URL`. The watch shows the host and a code; confirm it on the Devices page, and the sync runs.
- [ ] Approve a test workout, then Sync now: it shows as `delivered`.
- [ ] Change the URL to a wrong host: the watch forgets and shows pairing for the new host. Set it back and pair again, then revoke the stale device on the Devices page.
- [ ] The owner approves the beta, and only then: run `./build.sh store` and upload `bin/coachd.iq` as the public app (spec §6).
