import Toybox.Communications;
import Toybox.Lang;
import Toybox.PersistedContent;
import Toybox.Time;

// One foreground sync run:
//  1. fetch the desired set;
//  2. read the ids of the app's workouts on the watch;
//  3. apply SyncModel.plan: remove the workouts that left the set (or are
//     orphans), then drop the stale mappings;
//  4. download each missing workout, one at a time, and find the workout it
//     created by comparing ids before and after; with exactly one new id,
//     acknowledge it and store the mapping once coachd confirmed the ack
//     (otherwise skip the ack and count it as unmapped);
//  5. record the time of a complete sync.
// The mapping is persisted after every change, so an interrupted run keeps
// what it did; a workout downloaded but not acknowledged stays unmapped, and
// the next run removes it as an orphan and delivers it again.
// Requests are strictly sequential and go to the bound server
// (Store.server()), taken once at the start. Progress and the outcome go to
// CoachdApp.setStatus, which StatusView draws. A new run is a new object;
// cancel() makes a run ignore the responses still in flight.
class Sync {
    private var _base as String = "";
    private var _busy as Boolean = false;
    private var _cancelled as Boolean = false;
    private var _desired as Array<Dictionary> = [] as Array<Dictionary>;
    private var _map as Dictionary<String, Number> = {} as Dictionary<String, Number>;
    private var _todo as Array<String> = [] as Array<String>;
    private var _next as Number = 0;
    private var _current as String = "";
    private var _before as Array<Number> = [] as Array<Number>;
    private var _newId as Number = 0;
    // The first per-workout failure (any error on one download or ack that
    // does not stop the sync, see SyncModel.stopsSync); the run goes on with
    // the next workout and reports it at the end.
    private var _failure as Number or Null = null;
    // Downloads after which not exactly one new workout appeared: neither
    // mapped nor acknowledged, so the next sync tries them again.
    private var _unmapped as Number = 0;

    function initialize() {
    }

    function isBusy() as Boolean {
        return _busy;
    }

    function start() as Void {
        var base = Store.server();
        if (base == null) {
            getApp().showStart();
            return;
        }
        _base = base;
        _busy = true;
        getApp().setStatus(Ui.text(Rez.Strings.Syncing));
        Api.fetchDesired(_base, method(:onDesired));
    }

    function cancel() as Void {
        _cancelled = true;
        _busy = false;
    }

    function onDesired(code as Number, data as Dictionary or String or Null) as Void {
        if (_cancelled) {
            return;
        }
        if (code != 200) {
            stop(code);
            return;
        }
        var desired = parseDesired(data);
        if (desired == null) {
            stop(Communications.INVALID_HTTP_BODY_IN_NETWORK_RESPONSE);
            return;
        }
        _desired = desired;
        Store.setWorkouts(desired);

        _map = Store.map();
        var plan = SyncModel.plan(desired, _map, Workouts.ids());
        Workouts.remove(plan["remove"] as Array<Number>);
        var drop = plan["drop"] as Array<String>;
        for (var i = 0; i < drop.size(); i++) {
            _map.remove(drop[i]);
        }
        Store.setMap(_map);

        _todo = plan["download"] as Array<String>;
        _next = 0;
        downloadNext();
    }

    private function downloadNext() as Void {
        if (_next >= _todo.size()) {
            finish();
            return;
        }
        _current = _todo[_next];
        _next += 1;
        _before = Workouts.ids();
        Api.downloadFit(_base, _current, method(:onFit));
    }

    function onFit(code as Number, data as Dictionary or String or Null) as Void {
        if (_cancelled) {
            return;
        }
        if (code != 200) {
            failed(code);
            return;
        }
        var fresh = SyncModel.newIds(_before, Workouts.ids());
        if (fresh.size() != 1) {
            // Nothing safe to map: no ack, so coachd retries the delivery.
            // An extra workout is an orphan, removed by the next sync.
            _unmapped += 1;
            downloadNext();
            return;
        }
        _newId = fresh[0];
        Api.ack(_base, _current, method(:onAck));
    }

    function onAck(code as Number, data as Dictionary or String or Null) as Void {
        if (_cancelled) {
            return;
        }
        if (code != 200) {
            // Left unmapped, so it heals on the next sync: the workout is
            // removed as an orphan, then downloaded and acknowledged again.
            failed(code);
            return;
        }
        _map[_current] = _newId;
        Store.setMap(_map);
        downloadNext();
    }

    // A download or ack failed. 401, 429, -104 and the transport and storage
    // errors (timeouts, a dropped connection, full storage...) end the run
    // (see SyncModel.stopsSync); any other code (an HTTP error, a FIT the
    // watch cannot take...) skips this workout: it stays unmapped, and the
    // next sync tries it again.
    private function failed(code as Number) as Void {
        if (SyncModel.stopsSync(code)) {
            stop(code);
            return;
        }
        if (_failure == null) {
            _failure = code;
        }
        downloadNext();
    }

    private function finish() as Void {
        _busy = false;
        var failure = _failure;
        if (failure != null) {
            getApp().setStatus(Ui.errorText(failure));
            return;
        }
        // "Last sync" means everything in the set is on the watch.
        if (_unmapped == 0) {
            Store.setLastSync(Time.now().value());
        }
        getApp().setStatus(Ui.countText(mappedCount()));
    }

    // Ends the run on code, keeping what already succeeded. A 401 means the
    // token was revoked: forget it and pair again.
    private function stop(code as Number) as Void {
        _busy = false;
        if (code == 401) {
            Store.clearToken();
            getApp().setStatus("");
            getApp().onUnpaired();
            return;
        }
        getApp().setStatus(Ui.errorText(code));
    }

    // The desired workouts that are mapped (and so on the watch).
    private function mappedCount() as Number {
        var n = 0;
        for (var i = 0; i < _desired.size(); i++) {
            if (_map.hasKey(_desired[i]["id"] as String)) {
                n += 1;
            }
        }
        return n;
    }

    // The desired set as [{"id", "name", "date"}], or null when the response
    // is malformed: a partial list would remove the missing workouts.
    private function parseDesired(data as Dictionary or String or Null) as Array<Dictionary> or Null {
        if (!(data instanceof Dictionary)) {
            return null;
        }
        var list = data["workouts"];
        if (!(list instanceof Array)) {
            return null;
        }
        var out = [] as Array<Dictionary>;
        for (var i = 0; i < list.size(); i++) {
            var w = list[i];
            if (!(w instanceof Dictionary)) {
                return null;
            }
            var id = w["id"];
            var name = w["name"];
            var date = w["date"];
            if (!(id instanceof String) || !(name instanceof String) || !(date instanceof String)) {
                return null;
            }
            if (!Api.isId(id)) {
                return null;
            }
            out.add({"id" => id, "name" => name, "date" => date});
        }
        return out;
    }
}

// The app's own workouts on the watch (PersistedContent.getAppWorkouts lists
// only workouts this app downloaded, so nothing else is ever touched).
module Workouts {
    function ids() as Array<Number> {
        var out = [] as Array<Number>;
        var it = PersistedContent.getAppWorkouts();
        var w = it.next();
        while (w != null) {
            out.add(w.getId());
            w = it.next();
        }
        return out;
    }

    function find(id as Number) as PersistedContent.Content or Null {
        var it = PersistedContent.getAppWorkouts();
        var w = it.next();
        while (w != null) {
            if (w.getId() == id) {
                return w;
            }
            w = it.next();
        }
        return null;
    }

    // Removes the app's workouts with these ids. One that cannot be removed
    // stays; the next sync tries again.
    function remove(ids as Array<Number>) as Void {
        if (ids.size() == 0) {
            return;
        }
        var doomed = [] as Array<PersistedContent.Content>;
        var it = PersistedContent.getAppWorkouts();
        var w = it.next();
        while (w != null) {
            if (ids.indexOf(w.getId()) >= 0) {
                doomed.add(w);
            }
            w = it.next();
        }
        for (var i = 0; i < doomed.size(); i++) {
            try {
                doomed[i].remove();
            } catch (e instanceof Lang.Exception) {
                // Not owned by this app or already gone: leave it.
            }
        }
    }
}
