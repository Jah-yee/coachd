import Toybox.Lang;

// The pure part of the coachd URL setting: normalising what the runner typed
// in the Connect IQ phone app, deciding when the state stored for another
// server must be forgotten, and the host the pairing screen shows. No Toybox
// I/O, so the (:test) suite covers it.
module ServerModel {
    // The canonical form of raw: "https://", a lower-case host, an optional
    // ":port" (1 to 5 digits) and a path of plain URL characters without
    // trailing slashes. Surrounding whitespace is trimmed, a missing scheme
    // means https, and the scheme is matched case-insensitively (phone
    // keyboards capitalise the first letter). Anything else, including a raw
    // value that is not a String, is null.
    function normalize(raw as Object or Null) as String or Null {
        if (!(raw instanceof String)) {
            return null;
        }
        var s = trim(raw);
        if (s.length() == 0) {
            return null;
        }
        var rest = s;
        var sep = s.find("://");
        if (sep != null) {
            if (!(s.substring(0, sep) as String).toLower().equals("https")) {
                return null;
            }
            rest = s.substring(sep + 3, s.length()) as String;
        }

        var slash = rest.find("/");
        var authority = slash == null ? rest : rest.substring(0, slash) as String;
        var path = slash == null ? "" : rest.substring(slash, rest.length()) as String;
        var host = authority;
        var port = "";
        var colon = authority.find(":");
        if (colon != null) {
            host = authority.substring(0, colon) as String;
            port = authority.substring(colon + 1, authority.length()) as String;
            if (port.length() < 1 || port.length() > 5 || !isDigits(port)) {
                return null;
            }
            port = ":" + port;
        }
        host = host.toLower();
        if (host.length() == 0 || !allowed(host, false)) {
            return null;
        }
        if (!allowed(path, true)) {
            return null;
        }
        while (path.length() > 0 && (path.substring(path.length() - 1, path.length()) as String).equals("/")) {
            path = path.substring(0, path.length() - 1) as String;
        }
        return "https://" + host + port + path;
    }

    // Whether the state stored for the server bound (the URL the token or
    // pairing in progress came from) must be forgotten before any request,
    // given the current setting. Nothing stored: nothing to forget. A stored
    // state without a bound String (an older build) is forgotten. An empty or
    // invalid setting keeps it, because no request is made then.
    function mustForget(current as String or Null, bound as Object or Null, hasState as Boolean) as Boolean {
        if (!hasState) {
            return false;
        }
        if (!(bound instanceof String)) {
            return true;
        }
        if (current == null) {
            return false;
        }
        return !current.equals(bound);
    }

    // The host (and port) of a normalised URL, shown on the pairing screen.
    function host(url as String) as String {
        var rest = url;
        if (url.find("https://") == 0) {
            rest = url.substring(8, url.length()) as String;
        }
        var slash = rest.find("/");
        return slash == null ? rest : rest.substring(0, slash) as String;
    }

    // s without leading and trailing spaces, tabs, CRs and LFs.
    function trim(s as String) as String {
        var chars = s.toCharArray();
        var start = 0;
        var end = chars.size();
        while (start < end && isSpace(chars[start].toNumber())) {
            start += 1;
        }
        while (end > start && isSpace(chars[end - 1].toNumber())) {
            end -= 1;
        }
        return s.substring(start, end) as String;
    }

    function isSpace(c as Number) as Boolean {
        return c == 32 || c == 9 || c == 10 || c == 13;
    }

    function isDigits(s as String) as Boolean {
        var chars = s.toCharArray();
        for (var i = 0; i < chars.size(); i++) {
            var c = chars[i].toNumber();
            if (c < 48 || c > 57) {
                return false;
            }
        }
        return true;
    }

    // Whether every character of s is allowed: lower-case letters, digits,
    // "." and "-" in a host, plus upper case, "_", "~" and "/" in a path.
    function allowed(s as String, isPath as Boolean) as Boolean {
        var chars = s.toCharArray();
        for (var i = 0; i < chars.size(); i++) {
            var c = chars[i].toNumber();
            var ok = (c >= 48 && c <= 57) || (c >= 97 && c <= 122) || c == 46 || c == 45;
            if (isPath) {
                ok = ok || (c >= 65 && c <= 90) || c == 95 || c == 126 || c == 47;
            }
            if (!ok) {
                return false;
            }
        }
        return true;
    }
}
