import Toybox.Application;
import Toybox.Application.Properties;
import Toybox.Application.Storage;
import Toybox.Background;
import Toybox.Lang;
import Toybox.System;
import Toybox.Time;
import Toybox.WatchUi;

// coachd's watch app: pairs with the coachd at the URL set in the Connect IQ
// phone app, then syncs the approved workouts (the desired set), their
// details and the agent's messages onto the watch in the foreground, on
// launch and on "Sync now". While paired, and unless the Notifications
// setting is off, a background check runs every 15 minutes and notifies the
// runner of a new message or workout (CheckDelegate); it never downloads. The
// token and the pairing in progress are bound to the URL they came from: when
// the setting changes, the app forgets them before any request (reconcile).
//
// The app class runs in three processes: the app, the background check and
// the glance. Methods only the app calls skip the type checker's background
// and glance checks.
(:background, :glance)
class CoachdApp extends Application.AppBase {
    // The result of the latest sync step, drawn by HomeView: empty after a
    // complete sync (HomeView then shows the last sync time).
    var status as String = "";
    // Whether HomeView is the top view (see onUnpaired).
    var homeVisible as Boolean = false;
    (:typecheck([disableBackgroundCheck, disableGlanceCheck]))
    private var _sync as Sync or Null = null;

    function initialize() {
        AppBase.initialize();
    }

    (:typecheck([disableBackgroundCheck, disableGlanceCheck]))
    function getInitialView() as [Views] or [Views, InputDelegates] {
        reconcile();
        schedule();
        return startView();
    }

    (:typecheck(disableGlanceCheck))
    function getServiceDelegate() as [System.ServiceDelegate] {
        return [new CheckDelegate()];
    }

    (:typecheck([disableBackgroundCheck, disableGlanceCheck]))
    function getGlanceView() as [WatchUi.GlanceView] or [WatchUi.GlanceView, WatchUi.GlanceViewDelegate] or Null {
        return [new CoachdGlance()];
    }

    // The background check's marks (CheckDelegate): stored so the next check
    // does not notify the same message or workout again. In the glance,
    // where storage may be read-only, the next check reads them back with
    // Background.getBackgroundData instead.
    function onBackgroundData(data as Application.PersistableType) as Void {
        if (data instanceof Dictionary) {
            try {
                Store.setMarks(data["msg"], data["wk"]);
            } catch (e instanceof Lang.Exception) {
                // Not writable here.
            }
        }
    }

    // The coachd URL setting or the Notifications setting changed (Connect
    // IQ phone app) while the app runs.
    (:typecheck([disableBackgroundCheck, disableGlanceCheck]))
    function onSettingsChanged() as Void {
        reconcile();
        schedule();
        showStart();
    }

    // Registers the background check every 15 minutes while paired with a
    // usable URL and the Notifications setting on; removes it otherwise.
    (:typecheck([disableBackgroundCheck, disableGlanceCheck]))
    function schedule() as Void {
        var on = Store.token() != null && Server.current() != null && Server.notifications();
        try {
            if (!on) {
                Background.deleteTemporalEvent();
            } else if (Background.getTemporalEventRegisteredTime() == null) {
                Background.registerForTemporalEvent(new Time.Duration(15 * 60));
            }
        } catch (e instanceof Lang.Exception) {
            // Registered too soon after the last event: the next launch retries.
        }
    }

    // Forgets the state stored for another server (ServerModel.mustForget),
    // cancelling a running sync first. Returns whether it forgot.
    (:typecheck([disableBackgroundCheck, disableGlanceCheck]))
    function reconcile() as Boolean {
        if (!ServerModel.mustForget(Server.current(), Storage.getValue("server"), Store.hasState())) {
            return false;
        }
        cancelSync();
        Store.forgetServer();
        return true;
    }

    // The view for the current state: setup without a usable URL, pairing
    // without a token, otherwise home.
    (:typecheck([disableBackgroundCheck, disableGlanceCheck]))
    function startView() as [Views, InputDelegates] {
        if (Server.current() == null) {
            return [new SetupView(), new WatchUi.BehaviorDelegate()];
        }
        if (Store.token() == null) {
            var pairing = new PairingView();
            return [pairing, new PairingDelegate(pairing)];
        }
        return [new HomeView(), new HomeDelegate()];
    }

    (:typecheck([disableBackgroundCheck, disableGlanceCheck]))
    function showStart() as Void {
        var v = startView();
        WatchUi.switchToView(v[0], v[1], WatchUi.SLIDE_IMMEDIATE);
    }

    // Starts a sync unless one is running. Without a usable URL or a bound
    // server there is nothing to sync with: show the start view instead.
    (:typecheck([disableBackgroundCheck, disableGlanceCheck]))
    function startSync() as Void {
        if (Server.current() == null || Store.server() == null) {
            showStart();
            return;
        }
        var s = _sync;
        if (s != null && s.isBusy()) {
            return;
        }
        s = new Sync();
        _sync = s;
        s.start();
    }

    // Abandons the running sync: its late responses are ignored.
    (:typecheck([disableBackgroundCheck, disableGlanceCheck]))
    function cancelSync() as Void {
        var s = _sync;
        if (s != null) {
            s.cancel();
        }
        _sync = null;
    }

    (:typecheck([disableBackgroundCheck, disableGlanceCheck]))
    function setStatus(text as String) as Void {
        status = text;
        WatchUi.requestUpdate();
    }

    // coachd answered 401 (the token was revoked) and the token is cleared.
    // Shows pairing now when HomeView is on top; otherwise HomeView does it
    // in onShow once the menus above it close (as after Unpair).
    (:typecheck([disableBackgroundCheck, disableGlanceCheck]))
    function onUnpaired() as Void {
        schedule();
        if (homeVisible) {
            showPairing();
        }
    }

    (:typecheck([disableBackgroundCheck, disableGlanceCheck]))
    function showPairing() as Void {
        var pairing = new PairingView();
        WatchUi.switchToView(pairing, new PairingDelegate(pairing), WatchUi.SLIDE_IMMEDIATE);
    }

    (:typecheck([disableBackgroundCheck, disableGlanceCheck]))
    function showHome() as Void {
        schedule();
        WatchUi.switchToView(new HomeView(), new HomeDelegate(), WatchUi.SLIDE_IMMEDIATE);
    }
}

(:background, :glance)
function getApp() as CoachdApp {
    return Application.getApp() as CoachdApp;
}

// The app's persistent state in Application.Storage:
//  - "server": the normalised coachd URL the token and the pairing in
//    progress belong to; requests carrying them go only there;
//  - "token": the device token (String), a secret: never print or draw it;
//  - "map": server workout id => id of the app's workout on the watch;
//  - "workouts": the last desired set ([{"id", "name", "date"}], date order),
//    so home, the menu and the glance have names and dates without a phone;
//  - "d:<id>": the detail of desired workout <id> (SyncModel.parseDetail),
//    deleted when the workout leaves the set;
//  - "messages": the agent's messages ([{"id", "at", "text"}], newest
//    first); "seenMsg": the highest message id the runner opened;
//  - "notifiedMsg", "notifiedWk": what background checks already notified
//    (the highest message id, the workout ids);
//  - "lastSync": time of the last complete sync (epoch seconds);
//  - "pairingId", "pairingCode", "pairingExp": the pairing in progress (its
//    id is the claim secret: never print or draw it; the code is the one the
//    screen shows; the expiry is in epoch seconds), so a relaunch resumes
//    polling it instead of starting another. Cleared once paired or expired.
// The background check and the glance only read it (onBackgroundData writes
// the check's marks in the app).
(:background, :glance)
module Store {
    function server() as String or Null {
        var s = Storage.getValue("server");
        return s instanceof String && s.length() > 0 ? s : null;
    }

    function setServer(url as String) as Void {
        Storage.setValue("server", url);
    }

    // Whether anything tied to a server is stored: a token, a pairing in
    // progress or workout mappings.
    function hasState() as Boolean {
        return token() != null || Storage.getValue("pairingId") != null || map().size() > 0;
    }

    // Forgets everything tied to the bound server, before talking to another
    // one. The app's workouts stay on the watch until the first sync after
    // the next pairing removes them as unmapped.
    function forgetServer() as Void {
        setWorkouts([] as Array<Dictionary>); // deletes the details too
        Storage.deleteValue("token");
        Storage.deleteValue("map");
        Storage.deleteValue("workouts");
        Storage.deleteValue("messages");
        Storage.deleteValue("seenMsg");
        Storage.deleteValue("notifiedMsg");
        Storage.deleteValue("notifiedWk");
        Storage.deleteValue("lastSync");
        Storage.deleteValue("server");
        clearPairing();
    }

    function token() as String or Null {
        var t = Storage.getValue("token");
        return t instanceof String && t.length() > 0 ? t : null;
    }

    function setToken(token as String) as Void {
        Storage.setValue("token", token);
    }

    function clearToken() as Void {
        Storage.deleteValue("token");
    }

    // The stored mapping; entries of the wrong type are skipped.
    function map() as Dictionary<String, Number> {
        var out = {} as Dictionary<String, Number>;
        var m = Storage.getValue("map");
        if (m instanceof Dictionary) {
            var keys = m.keys();
            for (var i = 0; i < keys.size(); i++) {
                var k = keys[i];
                var v = m[k];
                if (k instanceof String && v instanceof Number) {
                    out[k] = v;
                }
            }
        }
        return out;
    }

    function setMap(map as Dictionary<String, Number>) as Void {
        Storage.setValue("map", map as Dictionary<Storage.KeyType, Storage.ValueType>);
    }

    function workouts() as Array<Dictionary> {
        var list = Storage.getValue("workouts");
        return list instanceof Array ? list as Array<Dictionary> : [] as Array<Dictionary>;
    }

    // Stores the desired set and deletes the details of workouts that left
    // it.
    function setWorkouts(list as Array<Dictionary>) as Void {
        var keep = [] as Array<String>;
        for (var i = 0; i < list.size(); i++) {
            keep.add(list[i]["id"] as String);
        }
        var old = workouts();
        for (var i = 0; i < old.size(); i++) {
            var id = old[i]["id"];
            if (id instanceof String && keep.indexOf(id) < 0) {
                Storage.deleteValue("d:" + id);
            }
        }
        Storage.setValue("workouts", list as Array<Storage.ValueType>);
    }

    // The stored detail of a desired workout, or null.
    function detail(id as String) as Dictionary or Null {
        var d = Storage.getValue("d:" + id);
        return d instanceof Dictionary ? d : null;
    }

    function setDetail(id as String, d as Dictionary) as Void {
        Storage.setValue("d:" + id, d as Dictionary<Storage.KeyType, Storage.ValueType>);
    }

    function messages() as Array<Dictionary> {
        var list = Storage.getValue("messages");
        return list instanceof Array ? list as Array<Dictionary> : [] as Array<Dictionary>;
    }

    function setMessages(list as Array<Dictionary>) as Void {
        Storage.setValue("messages", list as Array<Storage.ValueType>);
    }

    function seenMsg() as Number {
        var n = Storage.getValue("seenMsg");
        return n instanceof Number ? n : 0;
    }

    // Marks message id and every older one read.
    function markRead(id as Number) as Void {
        if (id > seenMsg()) {
            Storage.setValue("seenMsg", id);
        }
    }

    // The background check's marks: [highest message id notified, workout
    // ids notified]. Data a check passed to Background.exit that the app has
    // not received yet is newer than storage, so it wins (bgData, read in the
    // background process; null elsewhere).
    function marks(bgData as Object or Null) as [Number, Array<String>] {
        var msg = Storage.getValue("notifiedMsg");
        var wk = Storage.getValue("notifiedWk");
        if (bgData instanceof Dictionary) {
            msg = bgData["msg"];
            wk = bgData["wk"];
        }
        var ids = [] as Array<String>;
        if (wk instanceof Array) {
            for (var i = 0; i < wk.size(); i++) {
                if (wk[i] instanceof String) {
                    ids.add(wk[i] as String);
                }
            }
        }
        return [msg instanceof Number ? msg : 0, ids];
    }

    function setMarks(msg as Object or Null, wk as Object or Null) as Void {
        if (msg instanceof Number) {
            Storage.setValue("notifiedMsg", msg);
        }
        if (wk instanceof Array) {
            Storage.setValue("notifiedWk", wk as Array<Storage.ValueType>);
        }
    }

    function lastSync() as Number or Null {
        var t = Storage.getValue("lastSync");
        return t instanceof Number ? t : null;
    }

    function setLastSync(t as Number) as Void {
        Storage.setValue("lastSync", t);
    }

    // The stored pairing as {"id" => String, "code" => String or Null} when
    // it can be resumed at now (epoch seconds); otherwise null, and a stale
    // one is cleared.
    function pairing(now as Number) as Dictionary or Null {
        var id = Storage.getValue("pairingId");
        if (!PairingModel.resumable(id, Storage.getValue("pairingExp"), now)) {
            clearPairing();
            return null;
        }
        var code = Storage.getValue("pairingCode");
        return {"id" => id, "code" => code instanceof String ? code : null};
    }

    function setPairing(id as String, code as String, exp as Number) as Void {
        Storage.setValue("pairingId", id);
        Storage.setValue("pairingCode", code);
        Storage.setValue("pairingExp", exp);
    }

    function clearPairing() as Void {
        Storage.deleteValue("pairingId");
        Storage.deleteValue("pairingCode");
        Storage.deleteValue("pairingExp");
    }

    // Unpair: forget the token and the mapping. The workouts stay on the
    // watch until a sync after the next pairing replaces them.
    function unpair() as Void {
        Storage.deleteValue("token");
        Storage.deleteValue("map");
    }
}

// The settings of the Connect IQ phone app: the coachd URL, normalised, and
// Notifications.
module Server {
    // Whether background checks may notify: the Notifications setting, on
    // unless set to false (sideloaded builds get no phone settings).
    function notifications() as Boolean {
        var on = null;
        try {
            on = Properties.getValue("notify");
        } catch (e instanceof Lang.Exception) {
            on = null;
        }
        return !(on instanceof Boolean) || on;
    }

    // The setting, or Config.DEFAULT_URL when it is empty (sideloaded builds
    // get no phone settings), normalised by ServerModel; null when unset or
    // invalid. An invalid setting never falls back to the default.
    function current() as String or Null {
        var value = null;
        try {
            value = Properties.getValue("serverUrl");
        } catch (e instanceof Lang.Exception) {
            value = null;
        }
        if (value == null || (value instanceof String && value.length() == 0)) {
            return ServerModel.normalize(Config.DEFAULT_URL);
        }
        return ServerModel.normalize(value);
    }
}
