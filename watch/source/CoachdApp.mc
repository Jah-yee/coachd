import Toybox.Application;
import Toybox.Application.Properties;
import Toybox.Application.Storage;
import Toybox.Lang;
import Toybox.WatchUi;

// coachd's watch app: pairs with the coachd at the URL set in the Connect IQ
// phone app, then syncs the approved workouts (the desired set) onto the
// watch in the foreground, on launch and on "Sync now". There is no
// background service. The token and the pairing in progress are bound to the
// URL they came from: when the setting changes, the app forgets them before
// any request (reconcile).
class CoachdApp extends Application.AppBase {
    // The result of the latest sync step, drawn by StatusView.
    var status as String = "";
    // Whether StatusView is the top view (see onUnpaired).
    var statusVisible as Boolean = false;
    private var _sync as Sync or Null = null;

    function initialize() {
        AppBase.initialize();
    }

    function getInitialView() as [Views] or [Views, InputDelegates] {
        reconcile();
        return startView();
    }

    // The coachd URL setting changed (Connect IQ phone app) while the app
    // runs.
    function onSettingsChanged() as Void {
        reconcile();
        showStart();
    }

    // Forgets the state stored for another server (ServerModel.mustForget),
    // cancelling a running sync first. Returns whether it forgot.
    function reconcile() as Boolean {
        if (!ServerModel.mustForget(Server.current(), Storage.getValue("server"), Store.hasState())) {
            return false;
        }
        cancelSync();
        Store.forgetServer();
        return true;
    }

    // The view for the current state: setup without a usable URL, pairing
    // without a token, otherwise the status.
    function startView() as [Views, InputDelegates] {
        if (Server.current() == null) {
            return [new SetupView(), new WatchUi.BehaviorDelegate()];
        }
        if (Store.token() == null) {
            var pairing = new PairingView();
            return [pairing, new PairingDelegate(pairing)];
        }
        return [new StatusView(), new StatusDelegate()];
    }

    function showStart() as Void {
        var v = startView();
        WatchUi.switchToView(v[0], v[1], WatchUi.SLIDE_IMMEDIATE);
    }

    // Starts a sync unless one is running. Without a usable URL or a bound
    // server there is nothing to sync with: show the start view instead.
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
    function cancelSync() as Void {
        var s = _sync;
        if (s != null) {
            s.cancel();
        }
        _sync = null;
    }

    function setStatus(text as String) as Void {
        status = text;
        WatchUi.requestUpdate();
    }

    // coachd answered 401 (the token was revoked) and the token is cleared.
    // Shows pairing now when StatusView is on top; otherwise StatusView does
    // it in onShow once the menus above it close (as after Unpair).
    function onUnpaired() as Void {
        if (statusVisible) {
            showPairing();
        }
    }

    function showPairing() as Void {
        var pairing = new PairingView();
        WatchUi.switchToView(pairing, new PairingDelegate(pairing), WatchUi.SLIDE_IMMEDIATE);
    }

    function showStatus() as Void {
        WatchUi.switchToView(new StatusView(), new StatusDelegate(), WatchUi.SLIDE_IMMEDIATE);
    }
}

function getApp() as CoachdApp {
    return Application.getApp() as CoachdApp;
}

// The app's persistent state in Application.Storage:
//  - "server": the normalised coachd URL the token and the pairing in
//    progress belong to; requests carrying them go only there;
//  - "token": the device token (String), a secret: never print or draw it;
//  - "map": server workout id => id of the app's workout on the watch;
//  - "workouts": the last desired set ([{"id", "name", "date"}], date order),
//    so the Workouts menu has names and dates without a phone;
//  - "lastSync": time of the last complete sync (epoch seconds);
//  - "pairingId", "pairingCode", "pairingExp": the pairing in progress (its
//    id is the claim secret: never print or draw it; the code is the one the
//    screen shows; the expiry is in epoch seconds), so a relaunch resumes
//    polling it instead of starting another. Cleared once paired or expired.
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
        Storage.deleteValue("token");
        Storage.deleteValue("map");
        Storage.deleteValue("workouts");
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

    function setWorkouts(list as Array<Dictionary>) as Void {
        Storage.setValue("workouts", list as Array<Storage.ValueType>);
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

// The coachd URL setting (Connect IQ phone app), normalised.
module Server {
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
