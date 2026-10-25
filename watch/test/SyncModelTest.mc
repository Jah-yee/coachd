// Run No Evil tests for SyncModel, PairingModel and ServerModel. build.sh test
// compiles them (its bin/dev.jungle adds this directory to the source path);
// running them needs the simulator: monkeydo bin/coachd-test.prg <device> -t
import Toybox.Lang;
import Toybox.Test;

// Desired-set entries for the given server ids, as Sync passes them.
function tDesired(ids as Array<String>) as Array<Dictionary> {
    var out = [] as Array<Dictionary>;
    for (var i = 0; i < ids.size(); i++) {
        out.add({"id" => ids[i], "name" => "Run " + ids[i], "date" => "2026-10-2" + i});
    }
    return out;
}

// Whether a and b hold the same ids in the same order.
function tSameNumbers(a as Array<Number>, b as Array<Number>) as Boolean {
    if (a.size() != b.size()) {
        return false;
    }
    for (var i = 0; i < a.size(); i++) {
        if (a[i] != b[i]) {
            return false;
        }
    }
    return true;
}

function tSameStrings(a as Array<String>, b as Array<String>) as Boolean {
    if (a.size() != b.size()) {
        return false;
    }
    for (var i = 0; i < a.size(); i++) {
        if (!a[i].equals(b[i])) {
            return false;
        }
    }
    return true;
}

// Whether plan has exactly these lists; logs each mismatch.
function tPlanIs(logger as Logger, plan as Dictionary, download as Array<String>, remove as Array<Number>, drop as Array<String>) as Boolean {
    var ok = true;
    var gotDownload = plan["download"] as Array<String>;
    if (!tSameStrings(gotDownload, download)) {
        logger.error("download: got " + gotDownload + ", want " + download);
        ok = false;
    }
    var gotRemove = plan["remove"] as Array<Number>;
    if (!tSameNumbers(gotRemove, remove)) {
        logger.error("remove: got " + gotRemove + ", want " + remove);
        ok = false;
    }
    var gotDrop = plan["drop"] as Array<String>;
    if (!tSameStrings(gotDrop, drop)) {
        logger.error("drop: got " + gotDrop + ", want " + drop);
        ok = false;
    }
    return ok;
}

(:test)
function syncPlanEmptyMapDownloadsAll(logger as Logger) as Boolean {
    var plan = SyncModel.plan(tDesired(["a", "b"]), {} as Dictionary<String, Number>, [] as Array<Number>);
    return tPlanIs(logger, plan, ["a", "b"], [], []);
}

(:test)
function syncPlanNothingToDo(logger as Logger) as Boolean {
    var plan = SyncModel.plan(tDesired(["a", "b"]), {"a" => 1, "b" => 2}, [1, 2]);
    return tPlanIs(logger, plan, [], [], []);
}

// Mapped but no longer on the watch (deleted by hand, or lost): download it
// again and drop the stale mapping.
(:test)
function syncPlanRedownloadsMissing(logger as Logger) as Boolean {
    var plan = SyncModel.plan(tDesired(["a", "b"]), {"a" => 1, "b" => 2}, [1]);
    return tPlanIs(logger, plan, ["b"], [], ["b"]);
}

// Withdrawn, replaced or past: left the desired set, so remove it and drop
// its mapping.
(:test)
function syncPlanRemovesUndesired(logger as Logger) as Boolean {
    var plan = SyncModel.plan(tDesired(["a"]), {"a" => 1, "b" => 2}, [1, 2]);
    return tPlanIs(logger, plan, [], [2], ["b"]);
}

// On the watch but mapped to nothing (an interrupted sync downloaded it but
// never stored the mapping): remove it; a desired id with no mapping is
// downloaded again.
(:test)
function syncPlanRemovesOrphans(logger as Logger) as Boolean {
    var ok = tPlanIs(logger, SyncModel.plan(tDesired(["a"]), {"a" => 1}, [1, 7]), [], [7], []);
    var plan = SyncModel.plan(tDesired(["a"]), {} as Dictionary<String, Number>, [7]);
    return tPlanIs(logger, plan, ["a"], [7], []) && ok;
}

(:test)
function newIdsSingle(logger as Logger) as Boolean {
    var got = SyncModel.newIds([1, 2], [2, 3, 1]);
    logger.debug("newIds: " + got);
    return tSameNumbers(got, [3]);
}

(:test)
function newIdsNone(logger as Logger) as Boolean {
    var same = SyncModel.newIds([1, 2], [1, 2]);
    var fewer = SyncModel.newIds([1, 2], [1]);
    logger.debug("newIds: " + same + " " + fewer);
    return tSameNumbers(same, []) && tSameNumbers(fewer, []);
}

(:test)
function newIdsMultiple(logger as Logger) as Boolean {
    var got = SyncModel.newIds([1], [1, 4, 5]);
    logger.debug("newIds: " + got);
    return tSameNumbers(got, [4, 5]);
}

// A revoked token, coachd's rate limit, a missing phone and the transport or
// storage errors end the sync: they affect every later request too.
(:test)
function stopsSyncOnAuthRateNoPhoneAndTransport(logger as Logger) as Boolean {
    var codes = [401, 429, -104, -2, -3, -300, -1000, -1001, -1004];
    var ok = true;
    for (var i = 0; i < codes.size(); i++) {
        if (!SyncModel.stopsSync(codes[i])) {
            logger.error("stopsSync(" + codes[i] + ") = false, want true");
            ok = false;
        }
    }
    return ok;
}

// Any other error concerns one workout: the sync goes on with the next one.
(:test)
function continuesOnOtherErrors(logger as Logger) as Boolean {
    var codes = [-1, -400, -402, -403, -1002, -1003, 404, 409, 500];
    var ok = true;
    for (var i = 0; i < codes.size(); i++) {
        if (SyncModel.stopsSync(codes[i])) {
            logger.error("stopsSync(" + codes[i] + ") = true, want false");
            ok = false;
        }
    }
    return ok;
}

// The transport and storage errors (not -104, 401 or 429) get the
// "Connection problem" status.
(:test)
function transportErrors(logger as Logger) as Boolean {
    var yes = [-2, -3, -300, -1000, -1001, -1004];
    var no = [-104, 401, 429, -400, -1002, 404, 500];
    var ok = true;
    for (var i = 0; i < yes.size(); i++) {
        if (!SyncModel.isTransport(yes[i])) {
            logger.error("isTransport(" + yes[i] + ") = false, want true");
            ok = false;
        }
    }
    for (var i = 0; i < no.size(); i++) {
        if (SyncModel.isTransport(no[i])) {
            logger.error("isTransport(" + no[i] + ") = true, want false");
            ok = false;
        }
    }
    return ok;
}

// A stored pairing is resumed only with a well-formed id and an expiry still
// ahead.
(:test)
function pairingResumable(logger as Logger) as Boolean {
    var id = "0123456789abcdef0123456789abcdef";
    var ok = true;
    if (!PairingModel.resumable(id, 1001, 1000)) {
        logger.error("unexpired pairing not resumable");
        ok = false;
    }
    var badIds = [null, "0123", "0123456789ABCDEF0123456789ABCDEF", 42] as Array<Object or Null>;
    for (var i = 0; i < badIds.size(); i++) {
        if (PairingModel.resumable(badIds[i], 2000, 1000)) {
            logger.error("resumable with bad id #" + i);
            ok = false;
        }
    }
    var badExps = [1000, 999, null, "2000"] as Array<Object or Null>;
    for (var i = 0; i < badExps.size(); i++) {
        if (PairingModel.resumable(id, badExps[i], 1000)) {
            logger.error("resumable with expiry #" + i + " at 1000");
            ok = false;
        }
    }
    return ok;
}

// The stored expiry is now plus coachd's expires_in_s, or 600 s when that is
// missing or not a positive number.
(:test)
function pairingExpiry(logger as Logger) as Boolean {
    var given = [300, 600, null, 0, -5, "300"] as Array<Object or Null>;
    var want = [1300, 1600, 1600, 1600, 1600, 1600] as Array<Number>;
    var ok = true;
    for (var i = 0; i < given.size(); i++) {
        var got = PairingModel.expiry(1000, given[i]);
        if (got != want[i]) {
            logger.error("expiry case #" + i + ": got " + got + ", want " + want[i]);
            ok = false;
        }
    }
    return ok;
}

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
