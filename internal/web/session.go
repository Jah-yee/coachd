package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/stefiix92/coachd/internal/store"
)

const (
	// cookieName's __Host- prefix makes browsers insist on Secure, Path=/
	// and no Domain.
	cookieName = "__Host-coachd_session"
	// sessionTTL is the absolute session lifetime; there is no sliding renewal.
	sessionTTL = 12 * time.Hour
	// tokenLen is the length of the base64url (raw) form of 32 random bytes.
	tokenLen = 43
)

// hashToken is how session and enrolment tokens are stored: SHA-256 of the
// token string as sent (base64url text), like the private-lane tokens.
func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

type sessionKey struct{}

// session returns the request's passkey session. requireSession caches it
// in the request context; otherwise it is read from the cookie.
func (s *Server) session(r *http.Request) (store.WebSession, bool) {
	if ws, ok := r.Context().Value(sessionKey{}).(store.WebSession); ok {
		return ws, true
	}
	c, err := r.Cookie(cookieName)
	if err != nil || len(c.Value) != tokenLen {
		return store.WebSession{}, false
	}
	ws, err := s.d.DB.SessionByHash(r.Context(), hashToken(c.Value), s.d.Now())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.d.Log.ErrorContext(r.Context(), "session lookup failed", "component", "web", "outcome", "error",
				"error_class", errorClass(err))
		}
		return store.WebSession{}, false
	}
	return ws, true
}

// requireSession runs h only with a passkey session (Ruling R59: runner
// writes need Access plus a passkey). Without one it answers 401: JSON for a
// JSON request, otherwise the error page with a link to /login.
func (s *Server) requireSession(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws, ok := s.session(r)
		if !ok {
			if isJSON(r) {
				writeError(w, http.StatusUnauthorized, msgSignIn)
				return
			}
			s.render(w, r, http.StatusUnauthorized, "error", errorPage{Title: "Sign in", Message: msgSignIn, Login: true})
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, ws)))
	}
}

// startSession creates a 12 h session for the passkey and sets its cookie.
// A session the request already carried is ended first.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, passkeyID int64) error {
	raw := make([]byte, 32)
	rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.d.Now()
	expires := now.Add(sessionTTL)
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		if err := s.d.DB.DeleteSession(r.Context(), hashToken(c.Value)); err != nil {
			return err
		}
	}
	if err := s.d.DB.CreateSession(r.Context(), hashToken(token), passkeyID, now, expires); err != nil {
		return err
	}
	http.SetCookie(w, sessionCookieFor(token, expires, int(sessionTTL/time.Second)))
	return nil
}

// endSession deletes the request's session and clears its cookie.
func (s *Server) endSession(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		if err := s.d.DB.DeleteSession(r.Context(), hashToken(c.Value)); err != nil {
			return err
		}
	}
	http.SetCookie(w, sessionCookieFor("", time.Unix(0, 0), -1))
	return nil
}

func sessionCookieFor(value string, expires time.Time, maxAge int) *http.Cookie {
	return &http.Cookie{Name: cookieName, Value: value, Path: "/", Expires: expires, MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}
