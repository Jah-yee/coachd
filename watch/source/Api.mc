import Toybox.Communications;
import Toybox.Lang;

// coachd's device API (/device/v1/*) over Communications.makeWebRequest,
// which goes through the phone. Every call is asynchronous and answers the
// callback with the HTTP status, or a negative Communications error code
// (-104: no phone connection), and the parsed JSON body. Callers keep one
// request in flight at a time: the watch's request queue is small and coachd
// rate-limits per device.
//
// Every call takes the server's base URL. The device token and the pairing id
// are secrets: they go only into request headers and URLs for the server they
// came from (Store.server()), never to another server, System.println or the
// screen. Only startPairing is given a URL before it is bound.
module Api {
    typedef Callback as Method(code as Number, data as Dictionary or String or Null) as Void;

    function url(base as String, path as String) as String {
        return base + "/device/v1" + path;
    }

    // Headers for the authenticated calls. Without a token coachd answers
    // 401, which the sync handles like a revoked token.
    function authHeaders() as Dictionary {
        var token = Store.token();
        return token == null ? {} : {"Authorization" => "Bearer " + token};
    }

    // GET /device/v1/workouts: {"today", "workouts": [{"id", "date", "name"}]}.
    function fetchDesired(base as String, cb as Callback) as Void {
        Communications.makeWebRequest(url(base, "/workouts"), null, {
            :method => Communications.HTTP_REQUEST_METHOD_GET,
            :headers => authHeaders(),
            :responseType => Communications.HTTP_RESPONSE_CONTENT_TYPE_JSON
        }, cb);
    }

    // GET /device/v1/workouts/{id}/fit. The system parses the FIT workout and
    // stores it as one of the app's workouts; the callback gets no handle on
    // it, so Sync finds it by comparing workout ids before and after.
    function downloadFit(base as String, id as String, cb as Callback) as Void {
        Communications.makeWebRequest(url(base, "/workouts/" + id + "/fit"), null, {
            :method => Communications.HTTP_REQUEST_METHOD_GET,
            :headers => authHeaders(),
            :responseType => Communications.HTTP_RESPONSE_CONTENT_TYPE_FIT
        }, cb);
    }

    // POST /device/v1/workouts/{id}/ack with the JSON body {}.
    function ack(base as String, id as String, cb as Callback) as Void {
        var headers = authHeaders();
        headers["Content-Type"] = Communications.REQUEST_CONTENT_TYPE_JSON;
        Communications.makeWebRequest(url(base, "/workouts/" + id + "/ack"), {}, {
            :method => Communications.HTTP_REQUEST_METHOD_POST,
            :headers => headers,
            :responseType => Communications.HTTP_RESPONSE_CONTENT_TYPE_JSON
        }, cb);
    }

    // POST /device/v1/pairings with {"model": model}:
    // 201 {"pairing_id", "code", "expires_in_s"}.
    function startPairing(base as String, model as String, cb as Callback) as Void {
        Communications.makeWebRequest(url(base, "/pairings"), {"model" => model}, {
            :method => Communications.HTTP_REQUEST_METHOD_POST,
            :headers => {"Content-Type" => Communications.REQUEST_CONTENT_TYPE_JSON},
            :responseType => Communications.HTTP_RESPONSE_CONTENT_TYPE_JSON
        }, cb);
    }

    // GET /device/v1/pairings/{pairingId}: 200 {"status": "pending"},
    // {"status": "paired", "token"} (exactly once) or {"status": "expired"}.
    function pollPairing(base as String, pairingId as String, cb as Callback) as Void {
        Communications.makeWebRequest(url(base, "/pairings/" + pairingId), null, {
            :method => Communications.HTTP_REQUEST_METHOD_GET,
            :responseType => Communications.HTTP_RESPONSE_CONTENT_TYPE_JSON
        }, cb);
    }

    // Whether s has the shape of a proposal or pairing id (32 lower-case hex
    // digits), so it is safe to put into a URL path.
    function isId(s as String) as Boolean {
        if (s.length() != 32) {
            return false;
        }
        var chars = s.toCharArray();
        for (var i = 0; i < chars.size(); i++) {
            var c = chars[i].toNumber();
            if (!((c >= 48 && c <= 57) || (c >= 97 && c <= 102))) {
                return false;
            }
        }
        return true;
    }
}
