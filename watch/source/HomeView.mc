import Toybox.Graphics;
import Toybox.Lang;
import Toybox.WatchUi;

// The main screen once paired: the next workout (its day, name and summary),
// the number of unread messages and, as a footer, the sync status or the
// time of the last complete sync. It starts a sync the first time it is
// shown; START opens the menu.
class HomeView extends WatchUi.View {
    private var _synced as Boolean = false;

    function initialize() {
        View.initialize();
    }

    function onShow() as Void {
        var app = getApp();
        app.homeVisible = true;
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
        getApp().homeVisible = false;
    }

    function onUpdate(dc as Dc) as Void {
        var w = dc.getWidth();
        var h = dc.getHeight();
        var width = (w * 4) / 5;
        dc.setColor(Graphics.COLOR_WHITE, Graphics.COLOR_BLACK);
        dc.clear();
        dc.setColor(Graphics.COLOR_LT_GRAY, Graphics.COLOR_TRANSPARENT);
        dc.drawText(w / 2, h / 10, Graphics.FONT_XTINY, Ui.text(Rez.Strings.AppName), Graphics.TEXT_JUSTIFY_CENTER);

        var list = Store.workouts();
        var i = SyncModel.nextIndex(list, Fmt.today());
        if (i < 0) {
            dc.setColor(Graphics.COLOR_WHITE, Graphics.COLOR_TRANSPARENT);
            Ui.drawWrapped(dc, (h * 2) / 5, Graphics.FONT_SMALL, Ui.text(Rez.Strings.NoWorkouts), width);
        } else {
            var next = list[i];
            dc.setColor(Ui.ACCENT, Graphics.COLOR_TRANSPARENT);
            dc.drawText(w / 2, (h * 11) / 50, Graphics.FONT_SMALL, Fmt.day(next["date"] as String), Graphics.TEXT_JUSTIFY_CENTER);
            dc.setColor(Graphics.COLOR_WHITE, Graphics.COLOR_TRANSPARENT);
            Ui.drawLines(dc, (h * 43) / 100, Graphics.FONT_MEDIUM, Ui.wrapMax(dc, Graphics.FONT_MEDIUM, next["name"] as String, width, 2));
            var d = Store.detail(next["id"] as String);
            if (d != null) {
                dc.setColor(Graphics.COLOR_LT_GRAY, Graphics.COLOR_TRANSPARENT);
                dc.drawText(w / 2, (h * 3) / 5, Graphics.FONT_XTINY, Fmt.summary(d), Graphics.TEXT_JUSTIFY_CENTER);
            }
        }

        var unread = SyncModel.unread(Store.messages(), Store.seenMsg());
        if (unread > 0) {
            dc.setColor(Ui.ACCENT, Graphics.COLOR_TRANSPARENT);
            dc.drawText(w / 2, (h * 69) / 100, Graphics.FONT_XTINY, Fmt.unread(unread), Graphics.TEXT_JUSTIFY_CENTER);
        }

        var status = getApp().status;
        var last = Store.lastSync();
        if (status.length() == 0 && last != null) {
            status = Ui.lastSyncText(last);
        }
        dc.setColor(Graphics.COLOR_LT_GRAY, Graphics.COLOR_TRANSPARENT);
        Ui.drawLines(dc, (h * 85) / 100, Graphics.FONT_XTINY, Ui.wrapMax(dc, Graphics.FONT_XTINY, status, (w * 3) / 5, 2));
    }
}

class HomeDelegate extends WatchUi.BehaviorDelegate {
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
