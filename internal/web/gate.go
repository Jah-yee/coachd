package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/stefiix92/coachd/internal/auth"
	"github.com/stefiix92/coachd/internal/store"
)

// maxNext caps the post-login target (spec §4.2): room for an authorize URL
// with a DCR client_id (about 2.5 KiB) and a 1 KiB state.
const maxNext = 8 << 10

// msgSignIn is the answer to a request that needs a passkey session.
const msgSignIn = "sign in with your passkey first"

// Failure buckets (spec §4.2, C1): 30 failures a minute, burst 10, per route
// group. Only failures draw from them; a verified assertion never looks.
const (
	failureEvery = 2 * time.Second
	failureBurst = 10
	msgTooMany   = "too many failed attempts; try again in a minute"
)

// route is one page route. An open route passes the builtin session gate
// without a session (spec §4.1); every other route needs one.
type route struct {
	pattern string
	handler http.HandlerFunc
	open    bool
}

// routeTable lists every page route. routes registers it, the session gate
// resolves requests against it and TestGateRouteWalk walks it, so a route
// cannot be added without the gate and the walk seeing it.
func (s *Server) routeTable() []route {
	rs := []route{
		{"GET /{$}", s.home, false},
		{"GET /login", s.loginPage, true},
		{"POST /login/begin", s.loginBegin, true},
		{"POST /login/finish", s.loginFinish, true},
		{"POST /logout", s.requireSession(s.logout), false},
		{"GET /enroll", s.enrollPage, true},
		{"POST /enroll/begin", s.enrollBegin, true},
		{"POST /enroll/finish", s.enrollFinish, true},
		{"GET /p/{id}", s.proposal, false},
		{"POST /p/{id}/approve", s.approve, false},
		{"GET /review", s.review, false},
		{"POST /review/approve", s.approveAll, false},
		{"POST /p/{id}/decline", s.requireSession(s.transition(store.ProposalDeclined, store.ProposalPending)), false},
		{"POST /p/{id}/withdraw", s.requireSession(s.transition(store.ProposalWithdrawn, store.ProposalApproved,
			store.ProposalDelivering, store.ProposalDeliveryFailed, store.ProposalDelivered)), false},
		{"POST /p/{id}/fit", s.requireSession(s.download(fitFile)), false},
		{"POST /p/{id}/zwo", s.requireSession(s.download(zwoFile)), false},
		{"GET /notes", s.notesPage, false},
		{"POST /notes", s.requireSession(s.noteCreate), false},
		{"POST /notes/{id}", s.requireSession(s.noteEdit), false},
		{"POST /notes/{id}/delete", s.requireSession(s.noteDelete), false},
		{"GET /passkeys", s.passkeysPage, false},
		{"POST /passkeys/{id}/revoke", s.requireSession(s.passkeyRevoke), false},
		{"GET /devices", s.devicesPage, false},
		{"POST /devices/pair", s.requireSession(s.devicePair), false},
		{"POST /devices/{id}/revoke", s.requireSession(s.deviceRevoke), false},
		{"GET /audit", s.auditPage, false},
	}
	if s.d.OAuth != nil {
		rs = append(rs,
			// GET checks the session itself: without one it sends the
			// browser to the login page with the request kept in next.
			route{"GET /oauth/authorize", s.authorizePage, true},
			route{"POST /oauth/authorize/begin", s.requireSession(s.consentBegin), false},
			route{"POST /oauth/authorize/finish", s.requireSession(s.consentFinish), false},
			route{"POST /oauth/authorize/deny", s.requireSession(s.consentDeny), false},
		)
	}
	if s.d.Builtin {
		rs = append(rs,
			route{"GET /apps", s.appsPage, false},
			route{"POST /apps/{id}/revoke", s.requireSession(s.appRevoke), false},
		)
	}
	return append(rs,
		route{"GET /static/", s.static, true},
		route{"/", s.notFound, false},
	)
}

// routes registers every page.
func (s *Server) routes(mux *http.ServeMux) {
	for _, rt := range s.routeTable() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
}

// sessionGate is the builtin-mode page gate (spec §4.1, I6): default deny.
// It resolves the request against the route table exactly as the page's mux
// will; an open route passes untouched. Every other route needs a passkey
// session: without one a GET or HEAD is sent to the login page with the
// target kept in next, and any other method gets 401 (JSON for a JSON request, otherwise the sign-in page). The gate never goes
// through auth.Middleware, so it writes no audit row and sends no
// WWW-Authenticate. With a session it sets the public-lane identity and
// caches the session in the context for requireSession and the handlers.
func (s *Server) sessionGate(next http.Handler) http.Handler {
	lookup := http.NewServeMux()
	s.routes(lookup)
	open := map[string]bool{}
	for _, rt := range s.routeTable() {
		if rt.open {
			open[rt.pattern] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := lookup.Handler(r); open[pattern] {
			next.ServeHTTP(w, r)
			return
		}
		ws, ok := s.session(r)
		if !ok {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(safeNext(r.URL.RequestURI())), http.StatusSeeOther)
				return
			}
			if isJSON(r) {
				writeError(w, http.StatusUnauthorized, msgSignIn)
				return
			}
			s.render(w, r, http.StatusUnauthorized, "error", errorPage{Title: "Sign in", Message: msgSignIn, Login: true})
			return
		}
		ctx := auth.WithIdentity(r.Context(), auth.Identity{Lane: auth.LanePublic, Subject: "runner"})
		ctx = context.WithValue(ctx, sessionKey{}, ws)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// safeNext validates a post-login target (spec §4.2, I2) and re-serialises
// it from its escaped path and raw query. It refuses, answering "/": more
// than maxNext bytes; any byte <= 0x20 or a backslash, in the target or in
// its decoded path; a scheme, host, opaque part or userinfo; a path that
// does not start with exactly one "/".
func safeNext(raw string) string {
	if raw == "" || len(raw) > maxNext || hasUnsafeByte(raw) {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil ||
		hasUnsafeByte(u.Path) || !oneSlash(u.Path) {
		return "/"
	}
	out := u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	if len(out) > maxNext || hasUnsafeByte(out) || !oneSlash(out) {
		return "/"
	}
	return out
}

// hasUnsafeByte reports a byte <= 0x20 (controls and space) or a backslash,
// which browsers strip or read as "/".
func hasUnsafeByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= 0x20 || s[i] == '\\' {
			return true
		}
	}
	return false
}

// oneSlash reports whether p starts with exactly one "/".
func oneSlash(p string) bool { return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") }

// newFailureBucket returns a full failure bucket.
func newFailureBucket() *rate.Limiter { return rate.NewLimiter(rate.Every(failureEvery), failureBurst) }

// failureBucket is the failure bucket of the request's route group: login,
// enrolment or consent finish; nil for every other route.
func (s *Server) failureBucket(r *http.Request) *rate.Limiter {
	switch failureTarget(r) {
	case "/login/finish":
		return s.loginFails
	case "/enroll/finish":
		return s.enrollFails
	case "/oauth/authorize/finish":
		return s.consentFails
	}
	return nil
}
