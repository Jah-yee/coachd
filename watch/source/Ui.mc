import Toybox.Communications;
import Toybox.Graphics;
import Toybox.Lang;
import Toybox.Time;
import Toybox.Time.Gregorian;
import Toybox.WatchUi;

// Texts and drawing shared by the views.
module Ui {
    // The colour of day labels, headings and the unread count.
    const ACCENT = Graphics.COLOR_ORANGE;

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

    // "Last sync HH:MM" in the watch's local time.
    function lastSyncText(epoch as Number) as String {
        var t = Gregorian.info(new Time.Moment(epoch), Time.FORMAT_SHORT);
        var hhmm = (t.hour as Number).format("%02d") + ":" + (t.min as Number).format("%02d");
        return Lang.format(text(Rez.Strings.LastSync), [hhmm]);
    }

    // Draws text centred on the screen around y, word-wrapped to width.
    function drawWrapped(dc as Dc, y as Number, font as Graphics.FontType, s as String, width as Number) as Void {
        drawLines(dc, y, font, wrap(dc, font, s, width));
    }

    // Draws lines centred on the screen around y.
    function drawLines(dc as Dc, y as Number, font as Graphics.FontType, lines as Array<String>) as Void {
        var lh = dc.getFontHeight(font);
        var top = y - (lines.size() * lh) / 2;
        for (var i = 0; i < lines.size(); i++) {
            dc.drawText(dc.getWidth() / 2, top + i * lh, font, lines[i], Graphics.TEXT_JUSTIFY_CENTER);
        }
    }

    // At most max lines of s wrapped to width; the last ends in "…" when
    // text was left out.
    function wrapMax(dc as Dc, font as Graphics.FontType, s as String, width as Number, max as Number) as Array<String> {
        var lines = wrap(dc, font, s, width);
        if (lines.size() <= max) {
            return lines;
        }
        lines = lines.slice(0, max);
        var last = lines[max - 1];
        while (last.length() > 1 && dc.getTextWidthInPixels(last + "…", font) > width) {
            last = last.substring(0, last.length() - 1) as String;
        }
        lines[max - 1] = last + "…";
        return lines;
    }

    // Splits s at newlines, then at spaces into lines no wider than width (a
    // longer word gets a line of its own).
    function wrap(dc as Dc, font as Graphics.FontType, s as String, width as Number) as Array<String> {
        var lines = [] as Array<String>;
        var nl = s.find("\n");
        if (nl != null) {
            lines.addAll(wrap(dc, font, s.substring(0, nl) as String, width));
            lines.addAll(wrap(dc, font, s.substring(nl + 1, s.length()) as String, width));
            return lines;
        }
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
