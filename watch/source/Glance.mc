import Toybox.Graphics;
import Toybox.Lang;
import Toybox.WatchUi;

// The glance (watches with glances): the app's name, the next workout's day
// and name, and its summary or the number of unread messages. It reads only
// what the app stored.
(:glance)
class CoachdGlance extends WatchUi.GlanceView {
    function initialize() {
        GlanceView.initialize();
    }

    function onUpdate(dc as Dc) as Void {
        var h = dc.getHeight();
        var just = Graphics.TEXT_JUSTIFY_LEFT | Graphics.TEXT_JUSTIFY_VCENTER;
        dc.setColor(Graphics.COLOR_WHITE, Graphics.COLOR_TRANSPARENT);
        dc.drawText(0, h / 6, Graphics.FONT_XTINY, Fmt.text(Rez.Strings.AppName).toUpper(), just);

        var list = Store.workouts();
        var i = SyncModel.nextIndex(list, Fmt.today());
        var line = Fmt.text(Rez.Strings.NoWorkouts);
        var sub = "";
        if (i >= 0) {
            line = Fmt.day(list[i]["date"] as String) + ": " + (list[i]["name"] as String);
            var d = Store.detail(list[i]["id"] as String);
            if (d != null) {
                sub = Fmt.summary(d);
            }
        }
        var unread = SyncModel.unread(Store.messages(), Store.seenMsg());
        if (unread > 0) {
            sub = Fmt.unread(unread);
        }
        dc.drawText(0, h / 2, Graphics.FONT_TINY, line, just);
        dc.setColor(unread > 0 ? Graphics.COLOR_ORANGE : Graphics.COLOR_LT_GRAY, Graphics.COLOR_TRANSPARENT);
        dc.drawText(0, (h * 5) / 6, Graphics.FONT_XTINY, sub, just);
    }
}
