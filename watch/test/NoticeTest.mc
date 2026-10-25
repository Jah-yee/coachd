// Run No Evil tests for the background check's and the screens' pure parts
// in SyncModel: parsing coachd's answers, what to notify, dates and the wake
// prompt's byte limit.
import Toybox.Lang;
import Toybox.Test;
import Toybox.Time;
import Toybox.Time.Gregorian;

const T_A = "0123456789abcdef0123456789abcdef";
const T_B = "fedcba9876543210fedcba9876543210";
const T_C = "00000000000000000000000000000000";

function tMsg(id as Number) as Dictionary {
    return {"id" => id, "at" => "2026-10-25T07:02:00+02:00", "text" => "m" + id};
}

function tIds(list as Array<Dictionary>) as Array<String> {
    var out = [] as Array<String>;
    for (var i = 0; i < list.size(); i++) {
        out.add(list[i]["id"] as String);
    }
    return out;
}

// Whether notice n has message id msg (0: none), these fresh ids and marks.
function tNoticeIs(logger as Logger, n as Dictionary, msg as Number, fresh as Array<String>, msgMark as Number, wk as Array<String>) as Boolean {
    var ok = true;
    var m = n["message"];
    var gotMsg = m instanceof Dictionary ? m["id"] as Number : 0;
    if (gotMsg != msg) {
        logger.error("message: got " + gotMsg + ", want " + msg);
        ok = false;
    }
    var gotFresh = tIds(n["fresh"] as Array<Dictionary>);
    if (!tSameStrings(gotFresh, fresh)) {
        logger.error("fresh: got " + gotFresh + ", want " + fresh);
        ok = false;
    }
    if ((n["msg"] as Number) != msgMark) {
        logger.error("msg mark: got " + (n["msg"] as Number) + ", want " + msgMark);
        ok = false;
    }
    var gotWk = n["wk"] as Array<String>;
    if (!tSameStrings(gotWk, wk)) {
        logger.error("wk mark: got " + gotWk + ", want " + wk);
        ok = false;
    }
    return ok;
}

(:test)
function noticeNewestMessageWins(logger as Logger) as Boolean {
    var desired = tDesired([T_A, T_B]);
    // Message 7 is newer than read (5) and notified (6): it is notified, the
    // new workout B waits for the next check.
    var n = SyncModel.notice(desired, [tMsg(7), tMsg(6)], [T_A], 5, 6, [] as Array<String>);
    return tNoticeIs(logger, n, 7, [] as Array<String>, 7, [T_A]);
}

(:test)
function noticeNoRepeat(logger as Logger) as Boolean {
    var desired = tDesired([T_A, T_B]);
    var ok = true;
    // Already notified, or already read: nothing again.
    var n = SyncModel.notice(desired, [tMsg(7)], [T_A], 0, 7, [T_B]);
    ok = tNoticeIs(logger, n, 0, [] as Array<String>, 7, [T_A, T_B]) && ok;
    n = SyncModel.notice(desired, [tMsg(7)], [T_A, T_B], 9, 0, [] as Array<String>);
    ok = tNoticeIs(logger, n, 0, [] as Array<String>, 9, [T_A, T_B]) && ok;
    return ok;
}

(:test)
function noticeFreshWorkouts(logger as Logger) as Boolean {
    var desired = tDesired([T_A, T_B, T_C]);
    // A is on the watch, B was notified: C is fresh. Notified ids that left
    // the set (T_X) are dropped from the mark.
    var n = SyncModel.notice(desired, [] as Array<Dictionary>, [T_A], 0, 0, [T_B, "gone"]);
    return tNoticeIs(logger, n, 0, [T_C], 0, [T_A, T_B, T_C]);
}

(:test)
function parseMessagesShapes(logger as Logger) as Boolean {
    var ok = true;
    var got = SyncModel.parseMessages({"messages" => [tMsg(3), {"id" => "4", "at" => "x", "text" => "y"}, 5, tMsg(2)]});
    if (got == null || got.size() != 2 || got[0]["id"] != 3 || got[1]["id"] != 2) {
        logger.error("parseMessages: got " + got);
        ok = false;
    }
    if (SyncModel.parseMessages({"messages" => "no"}) != null || SyncModel.parseMessages(null) != null) {
        logger.error("malformed list accepted");
        ok = false;
    }
    return ok;
}

(:test)
function parseDetailShapes(logger as Logger) as Boolean {
    var body = {"id" => T_A, "sport" => "running", "duration_s" => 3300, "distance_m" => 9000, "partial" => false,
        "steps" => ["Warm-up · 15:00", 7, "  Active · 1 km"], "notes" => "", "why" => "base"};
    var d = SyncModel.parseDetail(body);
    var ok = true;
    if (d == null || (d["dur"] as Number) != 3300 || (d["steps"] as Array).size() != 2 || !"base".equals(d["why"] as String)) {
        logger.error("parseDetail: got " + d);
        ok = false;
    }
    body["partial"] = "no";
    if (SyncModel.parseDetail(body) != null) {
        logger.error("partial of the wrong type accepted");
        ok = false;
    }
    return ok;
}

(:test)
function parseDesiredRejectsPartialList(logger as Logger) as Boolean {
    var good = {"workouts" => [{"id" => T_A, "name" => "Run", "date" => "2026-10-25"}]};
    var bad = {"workouts" => [{"id" => T_A, "name" => "Run", "date" => "2026-10-25"}, {"id" => "../x", "name" => "Run", "date" => "2026-10-25"}]};
    var got = SyncModel.parseDesired(good);
    return got != null && got.size() == 1 && SyncModel.parseDesired(bad) == null && SyncModel.parseDesired("x") == null;
}

(:test)
function daysFromDates(logger as Logger) as Boolean {
    var today = [2026, 10, 25];
    // [date, want]; null: not a date
    var cases = [
        ["2026-10-25", 0], ["2026-10-26", 1], ["2026-10-24", -1], ["2026-11-01", 7],
        ["2027-01-01", 68], ["2026-10-25T07:02:00+02:00", 0], ["2026-13-01", null], ["25.10.2026", null], ["", null]
    ];
    var ok = true;
    for (var i = 0; i < cases.size(); i++) {
        var got = SyncModel.daysFrom(today, cases[i][0] as String);
        if (got != cases[i][1]) {
            logger.error("daysFrom(" + cases[i][0] + "): got " + got + ", want " + cases[i][1]);
            ok = false;
        }
    }
    // Across a leap day, and the epoch itself.
    if (SyncModel.daysFrom([2028, 2, 28], "2028-03-01") != 2 || SyncModel.dayNumber(1970, 1, 1) != 0) {
        logger.error("leap day or epoch");
        ok = false;
    }
    return ok;
}

(:test)
function nextIndexSkipsPast(logger as Logger) as Boolean {
    var list = [
        {"id" => T_A, "name" => "a", "date" => "2026-10-24"},
        {"id" => T_B, "name" => "b", "date" => "2026-10-25"},
        {"id" => T_C, "name" => "c", "date" => "2026-10-27"}
    ] as Array<Dictionary>;
    return SyncModel.nextIndex(list, [2026, 10, 25]) == 1 && SyncModel.nextIndex(list, [2026, 10, 26]) == 2
        && SyncModel.nextIndex(list, [2026, 10, 28]) == -1;
}

(:test)
function unreadCounts(logger as Logger) as Boolean {
    var list = [tMsg(9), tMsg(8), tMsg(5)];
    return SyncModel.unread(list, 0) == 3 && SyncModel.unread(list, 8) == 1 && SyncModel.unread(list, 9) == 0;
}

(:test)
function fitBytesCuts(logger as Logger) as Boolean {
    var ok = true;
    if (!SyncModel.fitBytes("short", 10).equals("short")) {
        logger.error("short text changed");
        ok = false;
    }
    // "é" is 2 bytes and "…" 3: 10 bytes leave room for 3 "é" and the ellipsis.
    var got = SyncModel.fitBytes("éééééé", 10);
    if (!got.equals("ééé…") || got.toUtf8Array().size() > 10) {
        logger.error("fitBytes: got " + got);
        ok = false;
    }
    return ok;
}

(:test)
function fmtDurationDistance(logger as Logger) as Boolean {
    // [got, want]
    var cases = [
        [Fmt.duration(3300, false), "55 min"], [Fmt.duration(3300, true), "55+ min"], [Fmt.duration(5100, false), "1 h 25 min"],
        [Fmt.duration(7200, true), "2 h+"], [Fmt.duration(0, true), ""], [Fmt.distance(800), "800 m"],
        [Fmt.distance(9000), "9 km"], [Fmt.distance(9460), "9.5 km"], [Fmt.distance(0), ""],
        [Fmt.join(["Run", "", "9 km"]), "Run · 9 km"]
    ];
    var ok = true;
    for (var i = 0; i < cases.size(); i++) {
        if (!(cases[i][0] as String).equals(cases[i][1] as String)) {
            logger.error("case " + i + ": got " + cases[i][0] + ", want " + cases[i][1]);
            ok = false;
        }
    }
    return ok;
}

// Day labels against the watch's own date: today and tomorrow by name, later
// days as weekday and day of month.
(:test)
function fmtDayLabels(logger as Logger) as Boolean {
    var t = Fmt.today();
    var base = SyncModel.dayNumber(t[0], t[1], t[2]);
    var ok = true;
    var got = [] as Array<String>;
    for (var i = 0; i < 3; i++) {
        var info = Gregorian.utcInfo(new Time.Moment((base + i) * 86400 + 43200), Time.FORMAT_SHORT);
        var date = (info.year as Number).format("%04d") + "-" + (info.month as Number).format("%02d") + "-" + (info.day as Number).format("%02d");
        got.add(Fmt.day(date));
    }
    if (!got[0].equals("Today") || !got[1].equals("Tomorrow") || got[2].length() < 4 || got[2].find(" ") == null) {
        logger.error("day labels: " + got);
        ok = false;
    }
    var at = Fmt.at("2026-10-25T07:02:00+01:00");
    if (!Fmt.day("nope").equals("nope") || at.find(" 07:02") != at.length() - 6) {
        logger.error("at or invalid day");
        ok = false;
    }
    return ok;
}
