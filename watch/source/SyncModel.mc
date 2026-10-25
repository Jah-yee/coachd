import Toybox.Lang;

// The pure part of the sync and the background check: what to download,
// remove and forget, given the desired set from coachd, the stored mapping
// and the app's workouts on the watch; parsing coachd's answers; what to
// notify. No Toybox I/O, so the (:test) suite covers it.
(:background, :glance)
module SyncModel {
    // desired: [{"id"=>String, "name"=>String, "date"=>String}] in server order.
    // map: server id => workout id. present: ids of the app's workouts on the
    // watch. Returns {"download"=>Array<String>, "remove"=>Array<Number>,
    // "drop"=>Array<String>}:
    //  - download: every desired id whose mapping is missing or whose mapped
    //    workout is not present, in desired order;
    //  - remove: every present workout not mapped to by a desired id
    //    (withdrawn workouts, and orphans of an interrupted sync);
    //  - drop: every map key that is not desired or whose workout is not
    //    present.
    function plan(desired as Array<Dictionary>, map as Dictionary<String, Number>, present as Array<Number>) as Dictionary {
        var wanted = {} as Dictionary<String, Boolean>;
        var download = [] as Array<String>;
        for (var i = 0; i < desired.size(); i++) {
            var id = desired[i]["id"] as String;
            if (wanted.hasKey(id)) {
                continue;
            }
            wanted[id] = true;
            var wid = map[id];
            if (wid == null || present.indexOf(wid) < 0) {
                download.add(id);
            }
        }

        var drop = [] as Array<String>;
        var kept = {} as Dictionary<Number, Boolean>;
        var keys = map.keys();
        for (var i = 0; i < keys.size(); i++) {
            var wid = map[keys[i]] as Number;
            if (wanted.hasKey(keys[i]) && present.indexOf(wid) >= 0) {
                kept[wid] = true;
            } else {
                drop.add(keys[i]);
            }
        }

        var remove = [] as Array<Number>;
        for (var i = 0; i < present.size(); i++) {
            if (!kept.hasKey(present[i])) {
                remove.add(present[i]);
            }
        }
        return {"download" => download, "remove" => remove, "drop" => drop};
    }

    // Whether a failed call ends the sync (ruling R-M3-F2): 401 (token
    // revoked), 429 (coachd's rate limit), -104 (BLE_CONNECTION_UNAVAILABLE,
    // no phone connection) and the transport and storage errors (isTransport)
    // affect every later request too. Any other code concerns one workout,
    // which the sync skips.
    function stopsSync(code as Number) as Boolean {
        return code == 401 || code == 429 || code == -104 || isTransport(code);
    }

    // The Communications errors of the link or the watch rather than of one
    // workout (values from Toybox.Communications in SDK 9.3.0):
    //  -2    BLE_HOST_TIMEOUT: timed out waiting for the phone;
    //  -3    BLE_SERVER_TIMEOUT: timed out waiting for the server;
    //  -300  NETWORK_REQUEST_TIMED_OUT: no response before the timeout;
    //  -1000 STORAGE_FULL: the watch's filesystem is too full for the response;
    //  -1001 SECURE_CONNECTION_REQUIRED: the request needs https;
    //  -1004 REQUEST_CONNECTION_DROPPED: the connection was lost before the
    //        response.
    function isTransport(code as Number) as Boolean {
        return code == -2 || code == -3 || code == -300 || code == -1000 || code == -1001 || code == -1004;
    }

    // The desired set as [{"id", "name", "date"}], or null when the response
    // is malformed: a partial list would remove the missing workouts.
    function parseDesired(data as Dictionary or String or Null) as Array<Dictionary> or Null {
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

    // The messages as [{"id", "at", "text"}], newest first, or null when the
    // response is malformed. Entries of the wrong shape are skipped.
    function parseMessages(data as Dictionary or String or Null) as Array<Dictionary> or Null {
        if (!(data instanceof Dictionary)) {
            return null;
        }
        var list = data["messages"];
        if (!(list instanceof Array)) {
            return null;
        }
        var out = [] as Array<Dictionary>;
        for (var i = 0; i < list.size(); i++) {
            var m = list[i];
            if (!(m instanceof Dictionary)) {
                continue;
            }
            var id = m["id"];
            var at = m["at"];
            var text = m["text"];
            if (id instanceof Number && at instanceof String && text instanceof String) {
                out.add({"id" => id, "at" => at, "text" => text});
            }
        }
        return out;
    }

    // A workout's detail as stored: {"sport", "dur", "dist", "partial",
    // "steps", "notes", "why"}, or null when the response is malformed.
    function parseDetail(data as Dictionary or String or Null) as Dictionary or Null {
        if (!(data instanceof Dictionary)) {
            return null;
        }
        var sport = data["sport"];
        var dur = data["duration_s"];
        var dist = data["distance_m"];
        var partial = data["partial"];
        var steps = data["steps"];
        var notes = data["notes"];
        var why = data["why"];
        if (!(sport instanceof String) || !(dur instanceof Number) || !(dist instanceof Number) || !(partial instanceof Boolean) ||
            !(steps instanceof Array) || !(notes instanceof String) || !(why instanceof String)) {
            return null;
        }
        var lines = [] as Array<String>;
        for (var i = 0; i < steps.size(); i++) {
            if (steps[i] instanceof String) {
                lines.add(steps[i] as String);
            }
        }
        return {"sport" => sport, "dur" => dur, "dist" => dist, "partial" => partial, "steps" => lines, "notes" => notes, "why" => why};
    }

    // What a background check notifies, given the desired set and messages
    // just fetched (messages newest first), the server ids mapped to workouts
    // on the watch, the highest message id read and the marks of earlier
    // checks (the highest message id notified, the workout ids notified).
    // Returns {"message" => the message to notify or null, "fresh" => the
    // desired workouts to notify (empty when a message is notified), "msg" =>
    // the new message mark, "wk" => the new workout mark}. A message newer
    // than both read and notified wins; otherwise every desired workout that
    // is neither on the watch nor notified. The workout mark keeps only ids
    // still desired.
    function notice(desired as Array<Dictionary>, messages as Array<Dictionary>, onWatch as Array<String>, seen as Number,
        notifiedMsg as Number, notifiedWk as Array<String>) as Dictionary {
        var mark = seen > notifiedMsg ? seen : notifiedMsg;
        var message = null;
        if (messages.size() > 0 && (messages[0]["id"] as Number) > mark) {
            message = messages[0];
            mark = messages[0]["id"] as Number;
        }
        var known = [] as Array<String>;
        var fresh = [] as Array<Dictionary>;
        for (var i = 0; i < desired.size(); i++) {
            var id = desired[i]["id"] as String;
            if (onWatch.indexOf(id) >= 0 || notifiedWk.indexOf(id) >= 0) {
                known.add(id);
            } else if (message == null) {
                fresh.add(desired[i]);
                known.add(id);
            }
        }
        return {"message" => message, "fresh" => fresh, "msg" => mark, "wk" => known};
    }

    // The number of days from 1970-01-01 to the civil date y-m-d (proleptic
    // Gregorian), so date differences need no time zone.
    function dayNumber(y as Number, m as Number, d as Number) as Number {
        if (m <= 2) {
            y -= 1;
        }
        var era = (y >= 0 ? y : y - 399) / 400;
        var yoe = y - era * 400;
        var doy = (153 * ((m + 9) % 12) + 2) / 5 + d - 1;
        var doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
        return era * 146097 + doe - 719468;
    }

    // [year, month, day] of a "YYYY-MM-DD" date (or the date part of an RFC
    // 3339 time), or null when it is not one.
    function parseDate(s as String) as Array<Number> or Null {
        if (s.length() < 10 || !(s.substring(4, 5) as String).equals("-") || !(s.substring(7, 8) as String).equals("-")) {
            return null;
        }
        var y = (s.substring(0, 4) as String).toNumber();
        var m = (s.substring(5, 7) as String).toNumber();
        var d = (s.substring(8, 10) as String).toNumber();
        if (y == null || m == null || d == null || m < 1 || m > 12 || d < 1 || d > 31) {
            return null;
        }
        return [y, m, d];
    }

    // Days from today ([year, month, day]) to date ("YYYY-MM-DD..."): 0 today,
    // 1 tomorrow, negative in the past; null when date is not a date.
    function daysFrom(today as Array<Number>, date as String) as Number or Null {
        var ymd = parseDate(date);
        if (ymd == null) {
            return null;
        }
        return dayNumber(ymd[0], ymd[1], ymd[2]) - dayNumber(today[0], today[1], today[2]);
    }

    // The index of the first desired workout dated today or later, or -1.
    function nextIndex(desired as Array<Dictionary>, today as Array<Number>) as Number {
        for (var i = 0; i < desired.size(); i++) {
            var date = desired[i]["date"];
            if (date instanceof String) {
                var days = daysFrom(today, date);
                if (days != null && days >= 0) {
                    return i;
                }
            }
        }
        return -1;
    }

    // How many messages are newer than seen.
    function unread(messages as Array<Dictionary>, seen as Number) as Number {
        var n = 0;
        for (var i = 0; i < messages.size(); i++) {
            var id = messages[i]["id"];
            if (id instanceof Number && id > seen) {
                n += 1;
            }
        }
        return n;
    }

    // s cut to at most max UTF-8 bytes, ending in "…" when cut.
    function fitBytes(s as String, max as Number) as String {
        if (s.toUtf8Array().size() <= max) {
            return s;
        }
        var chars = s.toCharArray();
        var out = "";
        var bytes = 3; // "…"
        for (var i = 0; i < chars.size(); i++) {
            var c = chars[i].toString();
            var n = c.toUtf8Array().size();
            if (bytes + n > max) {
                break;
            }
            out += c;
            bytes += n;
        }
        return out + "…";
    }

    // The ids in after that are not in before, in after's order.
    function newIds(before as Array<Number>, after as Array<Number>) as Array<Number> {
        var out = [] as Array<Number>;
        for (var i = 0; i < after.size(); i++) {
            if (before.indexOf(after[i]) < 0) {
                out.add(after[i]);
            }
        }
        return out;
    }
}
