import Toybox.Communications;
import Toybox.Graphics;
import Toybox.Lang;
import Toybox.System;
import Toybox.Time;
import Toybox.Timer;
import Toybox.WatchUi;

// Pairing: asks coachd for a pairing code, shows it, and polls every 3 s
// until the runner confirms it on coachd's Devices page (or with
// `coachd device pair`). On "paired" it stores the token and switches to the
// status view, which syncs. An expired pairing waits for START.
//
// The pairing in progress is kept in Storage (Store.setPairing), so after a
// relaunch the view resumes polling it while it is unexpired instead of
// starting another; it is cleared once paired or expired.
//
// A pairing is bound to the coachd URL it was started with (Store.server()):
// polls go only there, and an answer that arrives after the URL changed is
// ignored, so a token is never stored for another server. The screen shows
// that server's host.
//
// The pairing id is the claim secret and the token is the device credential:
// neither is printed or drawn.
class PairingView extends WatchUi.View {
    // The server this view's pairing belongs to.
    private var _server as String or Null = null;
    private var _pairingId as String or Null = null;
    private var _code as String or Null = null;
    // An error to show instead of the instructions (no phone, rate limit...).
    private var _message as String or Null = null;
    private var _expired as Boolean = false;
    private var _inFlight as Boolean = false;
    private var _visible as Boolean = false;
    private var _timer as Timer.Timer;

    function initialize() {
        View.initialize();
        _timer = new Timer.Timer();
    }

    function onShow() as Void {
        _visible = true;
        if (_inFlight || _expired) {
            return;
        }
        if (_pairingId != null) {
            later(3000);
            return;
        }
        var stored = Store.pairing(Time.now().value());
        if (stored != null && Store.server() != null) {
            _server = Store.server();
            _pairingId = stored["id"] as String;
            _code = stored["code"] as String or Null;
            WatchUi.requestUpdate();
            poll();
            return;
        }
        begin();
    }

    function onHide() as Void {
        _visible = false;
        _timer.stop();
    }

    // START: start over after the pairing expired or could not be started.
    function retry() as Boolean {
        if (_inFlight || _pairingId != null) {
            return false;
        }
        _timer.stop();
        begin();
        return true;
    }

    // Asks coachd at the current URL for a new pairing code, binding the
    // pairing to that URL.
    function begin() as Void {
        if (_inFlight || !_visible) {
            return;
        }
        var url = Server.current();
        if (url == null) {
            getApp().showStart();
            return;
        }
        Store.setServer(url);
        _server = url;
        _inFlight = true;
        _expired = false;
        _code = null;
        WatchUi.requestUpdate();
        Api.startPairing(url, System.getDeviceSettings().partNumber, method(:onStarted));
    }

    // Whether an answer belongs to the server this view's pairing is bound
    // to; false after the URL changed (reconcile forgot or rebound it).
    private function current() as Boolean {
        var server = _server;
        return server != null && server.equals(Store.server());
    }

    function onStarted(code as Number, data as Dictionary or String or Null) as Void {
        _inFlight = false;
        if (!current()) {
            return;
        }
        // coachd answers 201 Created; 200 is accepted as well.
        if ((code == 201 || code == 200) && data instanceof Dictionary) {
            var id = data["pairing_id"];
            var c = data["code"];
            if (id instanceof String && c instanceof String && Api.isId(id)) {
                _pairingId = id;
                _code = c;
                _message = null;
                Store.setPairing(id, c, PairingModel.expiry(Time.now().value(), data["expires_in_s"] as Object or Null));
                later(3000);
                WatchUi.requestUpdate();
                return;
            }
            code = Communications.INVALID_HTTP_BODY_IN_NETWORK_RESPONSE;
        }
        // Not started (no phone, too many pending pairings...): show why and
        // try again shortly; START retries at once.
        _message = Ui.pairingErrorText(code);
        if (_visible) {
            _timer.start(method(:begin), code == 429 ? 60000 : 3000, false);
        }
        WatchUi.requestUpdate();
    }

    function poll() as Void {
        var id = _pairingId;
        var server = _server;
        if (id == null || server == null || _inFlight || !_visible) {
            return;
        }
        _inFlight = true;
        Api.pollPairing(server, id, method(:onPolled));
    }

    function onPolled(code as Number, data as Dictionary or String or Null) as Void {
        _inFlight = false;
        if (!current()) {
            return;
        }
        if (code == 200 && data instanceof Dictionary) {
            var status = data["status"] as Object or Null;
            if ("pending".equals(status)) {
                _message = null;
                later(3000);
                WatchUi.requestUpdate();
                return;
            }
            if ("expired".equals(status)) {
                Store.clearPairing();
                _pairingId = null;
                _code = null;
                _message = null;
                _expired = true;
                _timer.stop();
                WatchUi.requestUpdate();
                return;
            }
            var token = data["token"];
            if ("paired".equals(status) && token instanceof String && token.length() > 0) {
                _pairingId = null;
                _timer.stop();
                Store.clearPairing();
                Store.setToken(token);
                getApp().showHome();
                return;
            }
            code = Communications.INVALID_HTTP_BODY_IN_NETWORK_RESPONSE;
        }
        // Keep polling while the pairing lasts (coachd answers "expired"
        // after 10 minutes); a rate limit waits a minute.
        _message = Ui.pairingErrorText(code);
        later(code == 429 ? 60000 : 3000);
        WatchUi.requestUpdate();
    }

    // Polls again after ms milliseconds, while the view is shown.
    private function later(ms as Number) as Void {
        if (_visible) {
            _timer.start(method(:poll), ms, false);
        }
    }

    function onUpdate(dc as Dc) as Void {
        var w = dc.getWidth();
        var h = dc.getHeight();
        dc.setColor(Graphics.COLOR_WHITE, Graphics.COLOR_BLACK);
        dc.clear();
        Ui.drawWrapped(dc, h / 5, Graphics.FONT_SMALL, Ui.text(Rez.Strings.PairTitle), (w * 3) / 4);
        var server = _server;
        if (server != null) {
            dc.setColor(Graphics.COLOR_LT_GRAY, Graphics.COLOR_TRANSPARENT);
            Ui.drawWrapped(dc, (h * 7) / 20, Graphics.FONT_XTINY, ServerModel.host(server), (w * 3) / 4);
            dc.setColor(Graphics.COLOR_WHITE, Graphics.COLOR_TRANSPARENT);
        }
        var code = _code;
        if (code != null) {
            dc.drawText(w / 2, h / 2, Graphics.FONT_NUMBER_MEDIUM, code, Graphics.TEXT_JUSTIFY_CENTER | Graphics.TEXT_JUSTIFY_VCENTER);
        }
        var below = _message;
        if (_expired) {
            below = Ui.text(Rez.Strings.PairExpired);
        } else if (below == null && code != null) {
            below = Ui.text(Rez.Strings.PairConfirm);
        }
        if (below != null) {
            dc.setColor(Graphics.COLOR_LT_GRAY, Graphics.COLOR_TRANSPARENT);
            Ui.drawWrapped(dc, (h * 4) / 5, Graphics.FONT_XTINY, below, (w * 3) / 4);
        }
    }
}

class PairingDelegate extends WatchUi.BehaviorDelegate {
    private var _view as PairingView;

    function initialize(view as PairingView) {
        BehaviorDelegate.initialize();
        _view = view;
    }

    function onSelect() as Boolean {
        return _view.retry();
    }
}
