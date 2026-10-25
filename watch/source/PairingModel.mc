import Toybox.Lang;

// The pure part of pairing persistence: whether a stored pairing can be
// resumed and when a new one expires. No Toybox I/O, so the (:test) suite
// covers it.
(:background, :glance)
module PairingModel {
    // The expiry assumed when coachd's answer has no usable expires_in_s.
    const DEFAULT_TTL_S = 600;

    // Whether a stored pairing can be resumed at now (epoch seconds): an id
    // with the shape of a pairing id and an expiry still ahead.
    function resumable(id as Object or Null, exp as Object or Null, now as Number) as Boolean {
        if (!(id instanceof String) || !Api.isId(id)) {
            return false;
        }
        if (!(exp instanceof Number)) {
            return false;
        }
        return exp > now;
    }

    // The expiry (epoch seconds) of a pairing started at now: now plus
    // coachd's expires_in_s, or DEFAULT_TTL_S when that is missing or not a
    // positive Number.
    function expiry(now as Number, expiresIn as Object or Null) as Number {
        if (expiresIn instanceof Number && expiresIn > 0) {
            return now + expiresIn;
        }
        return now + DEFAULT_TTL_S;
    }
}
