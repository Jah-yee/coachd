import Toybox.Application;
import Toybox.Lang;
import Toybox.Time;
import Toybox.Time.Gregorian;

// Texts built from stored data, for home, the menus, the glance and the
// background check's notifications. Dates are coachd's civil dates
// ("YYYY-MM-DD"), compared with the watch's local date.
(:background, :glance)
module Fmt {
    function text(id as ResourceId) as String {
        return Application.loadResource(id) as String;
    }

    // The watch's local date as [year, month, day].
    function today() as Array<Number> {
        var t = Gregorian.info(Time.now(), Time.FORMAT_SHORT);
        return [t.year as Number, t.month as Number, t.day as Number];
    }

    // "Today", "Tomorrow" or the short weekday and day ("Wed 29"); the date
    // itself when it is not one.
    function day(date as String) as String {
        var days = SyncModel.daysFrom(today(), date);
        if (days == 0) {
            return text(Rez.Strings.Today);
        }
        if (days == 1) {
            return text(Rez.Strings.Tomorrow);
        }
        var ymd = SyncModel.parseDate(date);
        if (ymd == null) {
            return date;
        }
        var m = Gregorian.moment({:year => ymd[0], :month => ymd[1], :day => ymd[2], :hour => 12});
        var info = Gregorian.utcInfo(m, Time.FORMAT_MEDIUM);
        return (info.day_of_week as String) + " " + ymd[2];
    }

    // "55 min", "1 h 25 min", with "+" when partial ("55+ min"); "" when
    // nothing is known.
    function duration(s as Number, partial as Boolean) as String {
        var min = (s + 30) / 60;
        if (min <= 0) {
            return "";
        }
        var plus = partial ? "+" : "";
        if (min < 60) {
            return min + plus + " min";
        }
        var rest = min % 60;
        return (min / 60) + " h" + (rest > 0 ? " " + rest + plus + " min" : plus);
    }

    // "800 m", "9 km", "9.5 km"; "" for 0.
    function distance(m as Number) as String {
        if (m <= 0) {
            return "";
        }
        if (m < 1000) {
            return m + " m";
        }
        var tenths = (m + 50) / 100;
        return (tenths % 10 == 0 ? (tenths / 10).toString() : (tenths / 10) + "." + (tenths % 10)) + " km";
    }

    function sport(s as String) as String {
        if (s.equals("running")) {
            return text(Rez.Strings.SportRun);
        } else if (s.equals("trail_running")) {
            return text(Rez.Strings.SportTrail);
        } else if (s.equals("walking")) {
            return text(Rez.Strings.SportWalk);
        } else if (s.equals("hiking")) {
            return text(Rez.Strings.SportHike);
        } else if (s.equals("cycling")) {
            return text(Rez.Strings.SportRide);
        } else if (s.equals("strength")) {
            return text(Rez.Strings.SportStrength);
        }
        return text(Rez.Strings.SportOther);
    }

    // parts joined with " · ", skipping empty ones.
    function join(parts as Array<String>) as String {
        var out = "";
        for (var i = 0; i < parts.size(); i++) {
            if (parts[i].length() > 0) {
                out += (out.length() > 0 ? " · " : "") + parts[i];
            }
        }
        return out;
    }

    // "Run · 55 min · 9 km" from a stored detail.
    function summary(d as Dictionary) as String {
        return join([sport(d["sport"] as String), duration(d["dur"] as Number, d["partial"] as Boolean), distance(d["dist"] as Number)]);
    }

    // "Today · 55 min": a workout's day and, with a detail, its duration.
    function when(w as Dictionary) as String {
        var parts = [day(w["date"] as String)];
        var d = Store.detail(w["id"] as String);
        if (d != null) {
            parts.add(duration(d["dur"] as Number, d["partial"] as Boolean));
        }
        return join(parts);
    }

    // "1 new message" or "<n> new messages".
    function unread(n as Number) as String {
        return n == 1 ? text(Rez.Strings.NewMessageOne) : Lang.format(text(Rez.Strings.NewMessagesN), [n]);
    }

    // The day and time of a message's RFC 3339 "at" ("Today 07:02").
    function at(s as String) as String {
        if (s.length() < 16) {
            return s;
        }
        return day(s.substring(0, 10) as String) + " " + (s.substring(11, 16) as String);
    }
}
