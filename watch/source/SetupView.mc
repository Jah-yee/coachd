import Toybox.Graphics;
import Toybox.Lang;
import Toybox.WatchUi;

// Shown while the coachd URL setting is unset or invalid: the app makes no
// request until the runner sets it in the Connect IQ phone app. The app then
// gets onSettingsChanged and switches away; onShow covers a URL that became
// usable while another view was on top.
class SetupView extends WatchUi.View {
    function initialize() {
        View.initialize();
    }

    function onShow() as Void {
        if (Server.current() != null) {
            getApp().showStart();
        }
    }

    function onUpdate(dc as Dc) as Void {
        var w = dc.getWidth();
        var h = dc.getHeight();
        dc.setColor(Graphics.COLOR_WHITE, Graphics.COLOR_BLACK);
        dc.clear();
        dc.drawText(w / 2, h / 6, Graphics.FONT_SMALL, Ui.text(Rez.Strings.AppName), Graphics.TEXT_JUSTIFY_CENTER);
        Ui.drawWrapped(dc, h / 2, Graphics.FONT_SMALL, Ui.text(Rez.Strings.SetupUrl), (w * 4) / 5);
    }
}
