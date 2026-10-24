import Toybox.Lang;

// The pure part of the sync: what to download, remove and forget, given the
// desired set from coachd, the stored mapping and the app's workouts on the
// watch. No Toybox I/O, so the (:test) suite covers it.
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
