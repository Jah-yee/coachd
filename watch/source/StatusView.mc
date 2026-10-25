import Toybox.Communications;
import Toybox.Graphics;
import Toybox.Lang;
import Toybox.Time;
import Toybox.Time.Gregorian;
import Toybox.WatchUi;

// The main screen once paired: the sync status and the time of the last
// complete sync. It starts a sync the first time it is shown; START opens the
// menu.
class StatusView extends WatchUi.View {
    private var _synced as Boolean = false;

    function initialize() {
        View.initialize();
    }

    function onShow() as Void {
        var app = getApp();
        app.statusVisible = true;
        if (Server.current() == null) {
            app.showStart();
            return;
        }
        if (Store.token() == null) {
            app.showPairing();
            return;
        }
        if (!_synced) {
            _synced = true;
            app.startSync();
        }
    }

    function onHide() as Void {
        getApp().statusVisible = false;
    }

    function onUpdate(dc as Dc) as Void {
        var w = dc.getWidth();
        var h = dc.getHeight();
        dc.setColor(Graphics.COLOR_WHITE, Graphics.COLOR_BLACK);
        dc.clear();
        dc.drawText(w / 2, h / 6, Graphics.FONT_SMALL, Ui.text(Rez.Strings.AppName), Graphics.TEXT_JUSTIFY_CENTER);
        Ui.drawWrapped(dc, h / 2, Graphics.FONT_SMALL, getApp().status, (w * 4) / 5);
        var last = Store.lastSync();
        if (last != null) {
            dc.setColor(Graphics.COLOR_LT_GRAY, Graphics.COLOR_TRANSPARENT);
            dc.drawText(w / 2, (h * 3) / 4, Graphics.FONT_XTINY, Ui.lastSyncText(last), Graphics.TEXT_JUSTIFY_CENTER);
        }
    }
}

class StatusDelegate extends WatchUi.BehaviorDelegate {
    function initialize() {
        BehaviorDelegate.initialize();
    }

    // START/ENTER opens the menu.
    function onSelect() as Boolean {
        WatchUi.pushView(mainMenu(), new MainMenuDelegate(), WatchUi.SLIDE_UP);
        return true;
    }

    function onMenu() as Boolean {
        return onSelect();
    }
}

// Texts and drawing shared by the views.
module Ui {
    function text(id as ResourceId) as String {
        return WatchUi.loadResource(id) as String;
    }

    // The status for a failed sync call: -104 (no phone), 429
    // (rate-limited), a transport or storage error (SyncModel.isTransport) or
    // any other code.
    function errorText(code as Number) as String {
        if (code == Communications.BLE_CONNECTION_UNAVAILABLE) {
            return text(Rez.Strings.NoPhone);
        }
        if (code == 429) {
            return text(Rez.Strings.Paused);
        }
        if (SyncModel.isTransport(code)) {
            return Lang.format(text(Rez.Strings.ConnProblem), [code]);
        }
        return Lang.format(text(Rez.Strings.Failed), [code]);
    }

    // The pairing view's text for a failed pairing call: -104 (no phone),
    // 401, 403 or 404 (not coachd's device API: a wrong URL such as the /mcp
    // connector URL, or the device API off, so coachd's page or Cloudflare
    // Access answered; a healthy pairing endpoint never answers these), 429
    // (coachd's pairing limits) or any other code.
    function pairingErrorText(code as Number) as String {
        if (code == Communications.BLE_CONNECTION_UNAVAILABLE) {
            return text(Rez.Strings.NoPhone);
        }
        if (code == 401 || code == 403 || code == 404) {
            return Lang.format(text(Rez.Strings.PairWrongUrl), [code]);
        }
        if (code == 429) {
            return text(Rez.Strings.PairTooMany);
        }
        return Lang.format(text(Rez.Strings.PairFailed), [code]);
    }

    // "1 workout" or "<n> workouts".
    function countText(n as Number) as String {
        if (n == 1) {
            return text(Rez.Strings.WorkoutsOne);
        }
        return Lang.format(text(Rez.Strings.WorkoutsN), [n]);
    }

    // "Last sync HH:MM" in the watch's local time.
    function lastSyncText(epoch as Number) as String {
        var t = Gregorian.info(new Time.Moment(epoch), Time.FORMAT_SHORT);
        var hhmm = (t.hour as Number).format("%02d") + ":" + (t.min as Number).format("%02d");
        return Lang.format(text(Rez.Strings.LastSync), [hhmm]);
    }

    // Draws text centred on the screen around y, word-wrapped to width.
    function drawWrapped(dc as Dc, y as Number, font as Graphics.FontType, s as String, width as Number) as Void {
        var lines = wrap(dc, font, s, width);
        var lh = dc.getFontHeight(font);
        var top = y - (lines.size() * lh) / 2;
        for (var i = 0; i < lines.size(); i++) {
            dc.drawText(dc.getWidth() / 2, top + i * lh, font, lines[i], Graphics.TEXT_JUSTIFY_CENTER);
        }
    }

    // Splits s at spaces into lines no wider than width (a longer word gets a
    // line of its own).
    function wrap(dc as Dc, font as Graphics.FontType, s as String, width as Number) as Array<String> {
        var lines = [] as Array<String>;
        var line = "";
        var rest = s;
        while (rest.length() > 0) {
            var word = rest;
            var sp = rest.find(" ");
            if (sp == null) {
                rest = "";
            } else {
                word = rest.substring(0, sp) as String;
                rest = rest.substring(sp + 1, rest.length()) as String;
            }
            if (word.length() == 0) {
                continue;
            }
            var joined = line.length() == 0 ? word : line + " " + word;
            if (line.length() > 0 && dc.getTextWidthInPixels(joined, font) > width) {
                lines.add(line);
                line = word;
            } else {
                line = joined;
            }
        }
        if (line.length() > 0) {
            lines.add(line);
        }
        return lines;
    }
}
