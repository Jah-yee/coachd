// Run No Evil smoke tests for the screens: each one draws into an offscreen
// bitmap of the device's size, on seeded storage, without an error. They
// check the drawing code runs, not how it looks.
import Toybox.Application.Storage;
import Toybox.Graphics;
import Toybox.Lang;
import Toybox.System;
import Toybox.Test;
import Toybox.Time;
import Toybox.Time.Gregorian;
import Toybox.WatchUi;

// The watch's date days from today, as "YYYY-MM-DD".
function tDate(days as Number) as String {
    var t = Gregorian.info(Time.now().add(new Time.Duration(days * 86400)), Time.FORMAT_SHORT);
    return (t.year as Number).format("%04d") + "-" + (t.month as Number).format("%02d") + "-" + (t.day as Number).format("%02d");
}

// Paired, with three workouts from today on (two with details) and two
// messages, one unread.
function tSeedScreens() as Void {
    Storage.clearValues();
    Store.setServer("https://coachd.example.com");
    Store.setToken("t0ken");
    Store.setWorkouts([
        {"id" => T_A, "name" => "Tempo 5x1 km", "date" => tDate(0)},
        {"id" => T_B, "name" => "Long trail run with hills and a very long name that wraps", "date" => tDate(1)},
        {"id" => T_C, "name" => "Zwift sweet spot", "date" => tDate(3)}
    ] as Array<Dictionary>);
    Store.setDetail(T_A, {"sport" => "running", "dur" => 3300, "dist" => 9000, "partial" => false,
        "steps" => ["Warm-up · 15:00 · HR 120–140 bpm", "Repeat 5×", "  Active · 1 km · pace 4:05–4:15 /km", "  Recovery · 2:00",
            "Cool-down · open (lap button)", "    note: jog home"],
        "notes" => "Flat route if you can.", "why" => "Threshold work before the race block.\nLegs looked fresh."});
    Store.setDetail(T_B, {"sport" => "trail_running", "dur" => 7200, "dist" => 18000, "partial" => true,
        "steps" => ["Active · 2:00:00 · HR 130–150 bpm"], "notes" => "", "why" => ""});
    Store.setMessages([
        {"id" => 3, "at" => tDate(0) + "T07:02:00+01:00", "text" => "Easy today if your legs are still heavy from Sunday: swap the tempo for 40 min easy."},
        {"id" => 2, "at" => tDate(-1) + "T19:40:00+01:00", "text" => "Nice long run."}
    ] as Array<Dictionary>);
    Storage.setValue("seenMsg", 2);
    Store.setLastSync(Time.now().value());
}

// An offscreen drawing context of w by h pixels. API 4 and later create
// bitmaps through the graphics pool; older devices cannot compile that call,
// so it is made through a method reference.
(:typecheck(false))
function tBitmapDc(w as Number, h as Number) as Dc {
    var opts = {:width => w, :height => h};
    if (Graphics has :createBufferedBitmap) {
        var ref = Graphics.createBufferedBitmap(opts);
        return ref.method(:get).invoke().getDc();
    }
    return new Graphics.BufferedBitmap(opts).getDc();
}

function tDc() as Dc {
    var s = System.getDeviceSettings();
    return tBitmapDc(s.screenWidth, s.screenHeight);
}

(:test)
function screensDraw(logger as Logger) as Boolean {
    tSeedScreens();
    var dc = tDc();
    new HomeView().onUpdate(dc);
    getApp().status = "Connection problem (-300), try again";
    new HomeView().onUpdate(dc);
    getApp().status = "";

    mainMenu();
    messagesMenu();
    var detail = new TextView([
        ["Wed 29", Graphics.FONT_XTINY, Ui.ACCENT],
        ["Tempo 5x1 km", Graphics.FONT_SMALL, Graphics.COLOR_WHITE],
        ["", Graphics.FONT_XTINY, Graphics.COLOR_LT_GRAY],
        [stepLine("    note: a long note that has to wrap over more than one line of the screen"), Graphics.FONT_XTINY, Graphics.COLOR_WHITE]
    ] as Array<Array>);
    for (var i = 0; i < 4; i++) {
        detail.onUpdate(dc);
        detail.scroll(1);
    }
    detail.scroll(-1);
    detail.onUpdate(dc);

    // Nothing planned and no messages.
    Store.setWorkouts([] as Array<Dictionary>);
    Store.setMessages([] as Array<Dictionary>);
    new HomeView().onUpdate(dc);
    mainMenu();
    messagesMenu();
    Storage.clearValues();
    return true;
}

(:test)
function stepLineIndents(logger as Logger) as Boolean {
    return stepLine("Repeat 5×").equals("Repeat 5×") && stepLine("  Active · 1 km").equals("› Active · 1 km")
        && stepLine("    note: x").equals("› › note: x");
}

(:test)
function glanceDraws(logger as Logger) as Boolean {
    tSeedScreens();
    var dc = tBitmapDc(180, 60);
    new CoachdGlance().onUpdate(dc);
    Storage.clearValues();
    new CoachdGlance().onUpdate(dc);
    return true;
}

// The background check's last step on canned answers, run in the app
// process (where Background.exit does nothing): a new message, then a new
// workout, notify without an error on watches with and without the
// Notifications API.
(:test)
function checkNotifies(logger as Logger) as Boolean {
    tSeedScreens();
    var check = new CheckDelegate();
    var desired = {"workouts" => [{"id" => T_A, "name" => "Tempo 5x1 km", "date" => tDate(0)}, {"id" => T_B, "name" => "Long run", "date" => tDate(1)}]};
    check.onDesired(200, desired);
    check.onMessages(200, {"messages" => [{"id" => 3, "at" => tDate(0) + "T07:02:00+01:00", "text" => "Easy today."}]});
    Storage.setValue("notifiedMsg", 3);
    check.onMessages(404, null);
    Storage.clearValues();
    return true;
}
