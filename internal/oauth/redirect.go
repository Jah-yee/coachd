package oauth

import (
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

// validRedirectURI reports whether s is an allowed redirect URI (spec §5.2):
// https with a host, or http on 127.0.0.1, [::1] or localhost with any port.
// Userinfo, fragments, other schemes (custom ones included) and anything on
// coachd's own origin, self, are refused.
func validRedirectURI(s, self string) bool {
	u, ok := parseRedirect(s)
	if !ok {
		return false
	}
	switch {
	case u.Scheme == "https" && u.Hostname() != "":
	case u.Scheme == "http" && loopbackHost(u.Hostname()):
	default:
		return false
	}
	return !sameOrigin(u, self)
}

// isLoopbackURI reports whether s is a loopback redirect URI: one a native
// app listens on, on a port it picks at run time.
func isLoopbackURI(s string) bool {
	u, ok := parseRedirect(s)
	return ok && u.Scheme == "http" && loopbackHost(u.Hostname())
}

// matchRedirect reports whether got is one of the registered redirect URIs.
// The comparison is exact, except that a loopback URI matches whatever its
// port (RFC 8252 §7.3). Callers also check validRedirectURI(got, self); that
// matters only for a loopback COACHD_PUBLIC_URL in development, the one case
// where a loopback URI can share coachd's origin.
func matchRedirect(registered []string, got string) bool {
	gotBare, gotLoopback := withoutPort(got)
	for _, r := range registered {
		if r == got {
			return true
		}
		if gotLoopback {
			if bare, ok := withoutPort(r); ok && bare == gotBare {
				return true
			}
		}
	}
	return false
}

// parseRedirect parses s, refusing the forms no redirect URI may take: opaque,
// with userinfo, or with a fragment, even an empty one.
func parseRedirect(s string) (*url.URL, bool) {
	if strings.Contains(s, "#") {
		return nil, false
	}
	u, err := url.Parse(s)
	if err != nil || u.Opaque != "" || u.User != nil {
		return nil, false
	}
	return u, true
}

func loopbackHost(h string) bool { return h == "127.0.0.1" || h == "::1" || h == "localhost" }

// withoutPort returns the loopback URI s with its port cut out of the string
// as written, so the rest still compares exactly; false when s is not a
// loopback URI.
func withoutPort(s string) (string, bool) {
	if !isLoopbackURI(s) {
		return "", false
	}
	rest, ok := strings.CutPrefix(s, "http://")
	if !ok { // the scheme in another case: exact matches only
		return "", false
	}
	end := strings.IndexAny(rest, "/?")
	if end < 0 {
		end = len(rest)
	}
	host := rest[:end]
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.HasSuffix(host, "]") {
		host = host[:i]
	}
	return "http://" + host + rest[end:], true
}

// sameOrigin reports whether u is on self's origin, comparing hosts the way a
// browser resolves them: IDNA-mapped, case-insensitive, a trailing dot
// ignored, and the port as a number with the scheme's default filled in.
func sameOrigin(u *url.URL, self string) bool {
	s, err := url.Parse(self)
	if err != nil {
		return true // self comes from validated config; fail closed regardless
	}
	return u.Scheme == s.Scheme && originHost(u.Hostname()) == originHost(s.Hostname()) && originPort(u) == originPort(s)
}

func originHost(h string) string {
	if a, err := idna.Lookup.ToASCII(h); err == nil {
		h = a
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

func originPort(u *url.URL) int {
	if n, err := strconv.Atoi(u.Port()); err == nil {
		return n
	}
	if u.Scheme == "https" {
		return 443
	}
	return 80
}
