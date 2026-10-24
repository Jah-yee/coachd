package web

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/stefiix92/coachd/internal/oauth"
	"github.com/stefiix92/coachd/internal/passkey"
	"github.com/stefiix92/coachd/internal/store"
)

// consentTTL is the longest a consent ceremony lasts: the login ceremony's
// time. It is cut to the expiry of the request token it binds, so a ceremony
// never outlives its request.
const consentTTL = loginTTL

type consentBeginBody struct {
	Request   string `json:"request"`
	Write     bool   `json:"write"`
	Recognise bool   `json:"recognise"`
}

type consentDenyBody struct {
	Request string `json:"request"`
}

type redirectBody struct {
	Redirect string `json:"redirect"`
}

// consentPageData is the data of consent.html.
type consentPageData struct {
	oauth.Consent
	HasRead, HasWrite bool
}

func earlier(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// pageError reports whether err is a *oauth.PageError and returns it.
func pageError(err error) (*oauth.PageError, bool) {
	var pe *oauth.PageError
	return pe, errors.As(err, &pe)
}

// authorizePage is GET /oauth/authorize. Without a session it checks the
// request's syntax only, and sends the browser to the login page with the
// request kept in next; with one it shows the consent page.
func (s *Server) authorizePage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if _, ok := s.session(r); !ok {
		if err := s.d.OAuth.CheckSyntax(q); err != nil {
			s.authorizeFailure(w, r, err)
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(safeNext(r.URL.RequestURI())), http.StatusSeeOther)
		return
	}
	c, err := s.d.OAuth.Authorize(r.Context(), q)
	if err != nil {
		s.authorizeFailure(w, r, err)
		return
	}
	data := consentPageData{Consent: c}
	for _, sc := range c.Scopes {
		switch sc {
		case "coach:read":
			data.HasRead = true
		case "coach:write":
			data.HasWrite = true
		}
	}
	s.render(w, r, http.StatusOK, "consent", data)
}

// authorizeFailure answers a failed GET: a redirect back to the client for
// a request error, coachd's error page for anything the runner must see.
func (s *Server) authorizeFailure(w http.ResponseWriter, r *http.Request, err error) {
	var re *oauth.RedirectError
	if errors.As(err, &re) {
		http.Redirect(w, r, re.URL, http.StatusSeeOther)
		return
	}
	if pe, ok := pageError(err); ok {
		s.render(w, r, http.StatusBadRequest, "error", errorPage{Title: "Cannot connect this app", Message: pe.Message})
		return
	}
	s.internalError(w, r, err, false)
}

// consentFailure answers a failed begin, finish or deny as JSON.
func (s *Server) consentFailure(w http.ResponseWriter, r *http.Request, err error) {
	if pe, ok := pageError(err); ok {
		writeError(w, http.StatusBadRequest, pe.Message)
		return
	}
	s.internalError(w, r, err, true)
}

// consentBegin re-validates the request and starts the passkey ceremony over
// the scopes Begin returned, never over anything the browser posted.
func (s *Server) consentBegin(w http.ResponseWriter, r *http.Request) {
	var req consentBeginBody
	if !s.consentDecode(w, r, &req) {
		return
	}
	ctx := r.Context()
	scopes, reqExp, err := s.d.OAuth.Begin(ctx, req.Request, req.Write, req.Recognise)
	if err != nil {
		s.consentFailure(w, r, err)
		return
	}
	opts, token, err := s.d.Passkeys.BeginConsent(ctx, req.Request, scopes,
		earlier(s.d.Now().Add(consentTTL), reqExp))
	if errors.Is(err, passkey.ErrNoPasskeys) {
		writeError(w, http.StatusConflict, msgNoPasskey)
		return
	}
	if err != nil {
		s.internalError(w, r, err, true)
		return
	}
	writeJSON(w, http.StatusOK, beginBody{Options: opts, Ceremony: token})
}

// consentFinish verifies the assertion and approves from the ceremony it
// bound: the request, the scopes and the nonce come from the verified
// ceremony, never from the body.
func (s *Server) consentFinish(w http.ResponseWriter, r *http.Request) {
	var req assertBody
	if !s.consentDecode(w, r, &req) {
		return
	}
	if req.Ceremony == "" || !present(req.Credential) {
		s.d.OAuth.CountConsent(r.Context(), "invalid_request")
		writeError(w, http.StatusBadRequest, msgMalformed)
		return
	}
	ctx := r.Context()
	as, err := s.d.Passkeys.FinishAssert(ctx, req.Ceremony, passkey.PurposeConsent, req.Credential)
	if errors.Is(err, passkey.ErrCeremonyExpired) {
		// The ceremony never outlives its request, so this is a page left open.
		s.d.OAuth.CountConsent(ctx, "invalid_request")
		writeError(w, http.StatusBadRequest, oauth.ErrRequestExpired.Message)
		return
	}
	if err != nil {
		switch {
		case s.passkeyFailure(w, r, err, 0): // the consent failure bucket is empty: 429
			s.d.OAuth.CountConsent(ctx, "rate_limited")
		case !errors.Is(err, passkey.ErrInternal):
			s.d.OAuth.CountConsent(ctx, "invalid_request")
		}
		return
	}
	redirect, err := s.d.OAuth.Approve(ctx, oauth.Approval{Request: as.Ceremony.Request, Scopes: as.Ceremony.Scopes,
		PasskeyID: as.PasskeyID, CredentialJSON: as.CredentialJSON, Nonce: as.Ceremony.Nonce(), NonceExpires: as.Ceremony.Expires})
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, redirectBody{Redirect: redirect})
	case errors.Is(err, passkey.ErrInternal), errors.Is(err, passkey.ErrUnknownCredential), errors.Is(err, store.ErrNonceUsed):
		s.passkeyFailure(w, r, err, as.PasskeyID)
	default:
		s.consentFailure(w, r, err)
	}
}

// consentDeny sends the browser back to the client with access_denied.
func (s *Server) consentDeny(w http.ResponseWriter, r *http.Request) {
	var req consentDenyBody
	if !s.consentDecode(w, r, &req) {
		return
	}
	redirect, err := s.d.OAuth.Deny(r.Context(), req.Request)
	if err != nil {
		s.consentFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, redirectBody{Redirect: redirect})
}

// consentDecode reads a JSON body, counting a malformed one as a consent
// request that never reached the OAuth server.
func (s *Server) consentDecode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !decodeJSON(w, r, dst) {
		s.d.OAuth.CountConsent(r.Context(), "invalid_request")
		return false
	}
	return true
}
