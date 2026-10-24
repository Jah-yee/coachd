// Run No Evil tests for CoachdApp.reconcile against the simulator's real
// Application.Storage and Properties. Each test seeds the state and clears it
// again; the test build's default URL is https://coachd.example.com.
import Toybox.Application.Properties;
import Toybox.Application.Storage;
import Toybox.Lang;
import Toybox.Test;

// Stores a token, a mapping and a pairing in progress, bound to server (or
// unbound, as an older build left them), and sets the URL setting.
function tSeed(server as String or Null, setting as String) as Void {
    Storage.clearValues();
    if (server != null) {
        Store.setServer(server);
    }
    Store.setToken("t0ken");
    Store.setMap({"0123456789abcdef0123456789abcdef" => 1});
    Store.setPairing("fedcba9876543210fedcba9876543210", "123456", 2000000000);
    Properties.setValue("serverUrl", setting);
}

function tCleanup() as Void {
    Properties.setValue("serverUrl", "");
    Storage.clearValues();
}

// Whether everything bound to a server is gone.
function tForgotten(logger as Logger) as Boolean {
    var ok = true;
    if (Store.token() != null) {
        logger.error("token kept");
        ok = false;
    }
    if (Store.server() != null) {
        logger.error("server kept");
        ok = false;
    }
    if (Store.map().size() != 0) {
        logger.error("map kept");
        ok = false;
    }
    if (Storage.getValue("pairingId") != null) {
        logger.error("pairing kept");
        ok = false;
    }
    return ok;
}

(:test)
function reconcileForgetsOnUrlChange(logger as Logger) as Boolean {
    tSeed("https://a.example.com", "https://b.example.com");
    var forgot = getApp().reconcile();
    var ok = forgot && tForgotten(logger);
    tCleanup();
    return ok;
}

(:test)
function reconcileKeepsSameUrl(logger as Logger) as Boolean {
    // Typed differently, the same server once normalised.
    tSeed("https://a.example.com", " Https://A.example.com/ ");
    var forgot = getApp().reconcile();
    var ok = !forgot && "t0ken".equals(Store.token()) && "https://a.example.com".equals(Store.server());
    tCleanup();
    return ok;
}

(:test)
function reconcileForgetsUnboundToken(logger as Logger) as Boolean {
    // A token from a build before the setting existed has no stored server.
    tSeed(null, "https://a.example.com");
    var forgot = getApp().reconcile();
    var ok = forgot && tForgotten(logger);
    tCleanup();
    return ok;
}

(:test)
function reconcileKeepsWhileSettingInvalid(logger as Logger) as Boolean {
    // An invalid setting does not fall back to the default and makes no
    // request, so the pairing is kept for when it is fixed.
    tSeed("https://a.example.com", "http://a.example.com");
    var forgot = getApp().reconcile();
    var ok = !forgot && Server.current() == null && "t0ken".equals(Store.token());
    tCleanup();
    return ok;
}

(:test)
function serverCurrentFallsBackToDefault(logger as Logger) as Boolean {
    Properties.setValue("serverUrl", "");
    var ok = "https://coachd.example.com".equals(Server.current());
    tCleanup();
    return ok;
}

(:test)
function pairingErrorTextNamesWrongUrl(logger as Logger) as Boolean {
    // A healthy POST /device/v1/pairings answers 201, 400, 429 or 500. A
    // wrong URL (such as the /mcp connector URL) or the device API turned
    // off reaches coachd's page or Access instead: 401, 403 or 404.
    var ok = true;
    var codes = [401, 403, 404];
    for (var i = 0; i < codes.size(); i++) {
        var want = "Not coachd's device API (" + codes[i] + "): check the URL";
        var got = Ui.pairingErrorText(codes[i]);
        if (!want.equals(got)) {
            logger.error(codes[i] + ": got " + got);
            ok = false;
        }
    }
    var other = Ui.pairingErrorText(500);
    if (!"Pairing failed (500)".equals(other)) {
        logger.error("500: got " + other);
        ok = false;
    }
    return ok;
}

(:test)
function pairingIgnoresAnswerAfterUrlChange(logger as Logger) as Boolean {
    // A pairing in progress with server A is resumed (onShow binds the view
    // to A and polls), then the URL changes to B before A answers "paired".
    tSeed("https://a.example.com", "https://a.example.com");
    Store.clearToken();
    Store.setMap({} as Dictionary<String, Number>);
    var view = new PairingView();
    view.onShow();
    Store.setServer("https://b.example.com");
    view.onPolled(200, {"status" => "paired", "token" => "stolen"});
    var ok = Store.token() == null;
    if (!ok) {
        logger.error("token from the old server was stored");
    }
    view.onHide();
    tCleanup();
    return ok;
}
