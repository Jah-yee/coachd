package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/passkey"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
)

const (
	loginTTL      = 5 * time.Minute
	enrollTTL     = 10 * time.Minute
	maxLabelRunes = 40

	msgMalformed   = "malformed request"
	msgCheckFailed = "passkey check failed"
	msgUsed        = "already used; try again"
	msgNoPasskey   = "no passkey enrolled"
	msgEnrollLink  = "enrolment link invalid or expired"
)

// beginBody answers /login/begin and /enroll/begin: the WebAuthn options
// ({"publicKey": {...}}) and the signed ceremony token to post back.
type beginBody struct {
	Options  json.RawMessage `json:"options"`
	Ceremony string          `json:"ceremony"`
}

// assertBody is posted to /login/finish (and the approve endpoint).
type assertBody struct {
	Ceremony   string          `json:"ceremony"`
	Credential json.RawMessage `json:"credential"`
}

type enrollBeginBody struct {
	Token string `json:"token"`
}

type enrollFinishBody struct {
	Ceremony   string          `json:"ceremony"`
	Token      string          `json:"token"`
	Label      string          `json:"label"`
	Credential json.RawMessage `json:"credential"`
}

var okBody = map[string]bool{"ok": true}

// present reports whether a JSON value was sent.
func present(v json.RawMessage) bool { return len(v) > 0 && string(v) != "null" }

// authPage is the data of login.html. Next is the validated page to land
// on after signing in (safeNext).
type authPage struct {
	NoPasskey bool
	Next      string
}

func (s *Server) hasPasskey(ctx context.Context) (bool, error) {
	pks, err := s.d.DB.ActivePasskeys(ctx)
	return len(pks) > 0, err
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	has, err := s.hasPasskey(r.Context())
	if err != nil {
		s.internalError(w, r, err, false)
		return
	}
	s.render(w, r, http.StatusOK, "login", authPage{NoPasskey: !has, Next: safeNext(r.URL.Query().Get("next"))})
}

// loginBegin needs no session (Ruling R-P2): it only mints a signed challenge.
func (s *Server) loginBegin(w http.ResponseWriter, r *http.Request) {
	opts, token, err := s.d.Passkeys.BeginAssert(r.Context(), passkey.PurposeLogin, "", "", s.d.Now().Add(loginTTL))
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

// loginFinish verifies the assertion, consumes its nonce, records the
// passkey's new sign count and starts a session.
func (s *Server) loginFinish(w http.ResponseWriter, r *http.Request) {
	var req assertBody
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Ceremony == "" || !present(req.Credential) {
		writeError(w, http.StatusBadRequest, msgMalformed)
		return
	}
	ctx := r.Context()
	as, err := s.d.Passkeys.FinishAssert(ctx, req.Ceremony, passkey.PurposeLogin, req.Credential)
	if err != nil {
		s.passkeyFailure(w, r, err, 0)
		return
	}
	now := s.d.Now()
	if err := s.d.DB.ConsumeNonce(ctx, as.Ceremony.Nonce(), now, as.Ceremony.Expires); err != nil {
		if errors.Is(err, store.ErrNonceUsed) {
			s.passkeyFailure(w, r, err, as.PasskeyID)
			return
		}
		s.internalError(w, r, err, true)
		return
	}
	// The sign count is stored before the session starts, so a failure here
	// leaves no session behind.
	if err := s.d.DB.UsePasskey(ctx, as.PasskeyID, as.CredentialJSON, now); err != nil {
		if errors.Is(err, store.ErrNotFound) { // revoked since the assertion was checked
			s.passkeyFailure(w, r, passkey.ErrUnknownCredential, as.PasskeyID)
			return
		}
		s.internalError(w, r, err, true)
		return
	}
	if err := s.startSession(w, r, as.PasskeyID); err != nil {
		s.internalError(w, r, err, true)
		return
	}
	id := strconv.FormatInt(as.PasskeyID, 10)
	s.appendAudit(r, store.AuditEvent{At: now, Lane: "public", Actor: "runner", Client: client(r, as.PasskeyID),
		Event: "passkey_login", Target: id, Outcome: "ok"})
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.endSession(w, r); err != nil {
		s.internalError(w, r, err, false)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) enrollPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "enroll", nil)
}

// enrollment resolves a one-time enrolment token. When it is absent, used
// or expired it counts the miss, answers 404 and returns false.
func (s *Server) enrollment(w http.ResponseWriter, r *http.Request, token string) (int64, bool) {
	if token != "" {
		eid, err := s.d.DB.EnrollmentByHash(r.Context(), hashToken(token), s.d.Now())
		if err == nil {
			return eid, true
		}
		if !errors.Is(err, store.ErrNotFound) {
			s.internalError(w, r, err, true)
			return 0, false
		}
	}
	s.tokenMiss(r)
	writeError(w, http.StatusNotFound, msgEnrollLink)
	return 0, false
}

// tokenMiss records an enrolment token that is absent, used or expired.
// Ruling R-F3: a miss needs no passkey, so a client holding only the Access
// JWT could repeat it at will; it is counted and logged but never audited,
// so it cannot flood the audit page or, past a small burst, the log. Failed passkey checks stay audited.
func (s *Server) tokenMiss(r *http.Request) {
	if m := s.d.Metrics; m != nil && m.AuthFailures != nil {
		m.AuthFailures.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("lane", "public"), attribute.String("reason", "enroll_token")))
	}
	// The log line is bounded (tokenMissLog: burst 10, one per 2 s): a
	// stranger can repeat a miss at will in builtin mode. The metric is not.
	if s.tokenMissLog.AllowN(s.d.Now(), 1) {
		s.d.Log.WarnContext(r.Context(), "enrolment token invalid or expired", "component", "web", "http.route", r.Pattern,
			"reason", "enroll_token")
	}
}

// enrollBegin needs no session (Ruling R-P2), only the enrolment token.
// The ceremony lasts 10 minutes; store.EnrollPasskey still refuses an
// enrolment past its own expiry.
func (s *Server) enrollBegin(w http.ResponseWriter, r *http.Request) {
	var req enrollBeginBody
	if !decodeJSON(w, r, &req) {
		return
	}
	eid, ok := s.enrollment(w, r, req.Token)
	if !ok {
		return
	}
	opts, token, err := s.d.Passkeys.BeginEnroll(r.Context(), eid, s.d.Now().Add(enrollTTL))
	if err != nil {
		s.internalError(w, r, err, true)
		return
	}
	writeJSON(w, http.StatusOK, beginBody{Options: opts, Ceremony: token})
}

// enrollFinish verifies the new credential and stores it; the store uses the
// enrolment and consumes the nonce in the same transaction. The session it
// starts is that passkey's.
func (s *Server) enrollFinish(w http.ResponseWriter, r *http.Request) {
	var req enrollFinishBody
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Ceremony == "" || !present(req.Credential) {
		writeError(w, http.StatusBadRequest, msgMalformed)
		return
	}
	label := strings.TrimSpace(req.Label)
	if err := workout.CheckLine("label", label, maxLabelRunes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := s.d.Now()
	if label == "" {
		label = "passkey " + civil.Of(now, s.d.TZ).String()
	}
	eid, ok := s.enrollment(w, r, req.Token)
	if !ok {
		return
	}
	ctx := r.Context()
	np, err := s.d.Passkeys.FinishEnroll(ctx, req.Ceremony, req.Credential)
	if err != nil {
		s.passkeyFailure(w, r, err, 0)
		return
	}
	// Ruling R-P1: the ceremony must be the one begun for this token's
	// enrolment, so finishing needs the one-time token, not only a signed
	// ceremony.
	if np.Ceremony.EnrollmentID != eid {
		s.fail(w, r, nil, "invalid", http.StatusNotFound, msgEnrollLink, 0)
		return
	}
	id, err := s.d.DB.EnrollPasskey(ctx, store.EnrollParams{
		EnrollmentID: eid, Nonce: np.Ceremony.Nonce(), NonceExpires: np.Ceremony.Expires,
		Passkey: store.Passkey{CredentialID: np.CredentialID, CredentialJSON: np.CredentialJSON, Label: label},
		At:      now, Lane: "public",
	})
	switch {
	case errors.Is(err, store.ErrNotFound): // used or expired since the lookup
		s.fail(w, r, nil, "invalid", http.StatusNotFound, msgEnrollLink, 0)
		return
	case errors.Is(err, store.ErrNonceUsed):
		s.passkeyFailure(w, r, err, 0)
		return
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "this passkey is already enrolled")
		return
	case err != nil:
		s.internalError(w, r, err, true)
		return
	}
	if err := s.startSession(w, r, id); err != nil {
		s.internalError(w, r, err, true)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

// failureClass maps a ceremony error to its audit reason, status and message.
// Only a genuine token past its expiry (a page left open) is "expired"; any
// other ceremony error, a forged token included, is "invalid". An
// infrastructure failure (passkey.ErrInternal) is an outage, not a failed
// check: status 500 and no reason.
func failureClass(err error) (reason string, status int, msg string) {
	switch {
	case errors.Is(err, passkey.ErrInternal):
		return "", http.StatusInternalServerError, ""
	case errors.Is(err, store.ErrNonceUsed):
		return "used", http.StatusConflict, msgUsed
	case errors.Is(err, passkey.ErrCeremonyExpired):
		return "expired", http.StatusUnauthorized, msgCheckFailed
	case errors.Is(err, passkey.ErrUnknownCredential):
		return "unknown_credential", http.StatusUnauthorized, msgCheckFailed
	}
	return "invalid", http.StatusUnauthorized, msgCheckFailed
}

// passkeyFailure records a failed ceremony and answers with its JSON error.
// It reports whether the failure bucket refused it (fail).
func (s *Server) passkeyFailure(w http.ResponseWriter, r *http.Request, err error, passkeyID int64) (limited bool) {
	reason, status, msg := failureClass(err)
	return s.fail(w, r, err, reason, status, msg, passkeyID)
}

// fail answers a classified ceremony error. A 500 is an outage: logged by
// internalError with no passkey_failure row and no auth-failure count. Any
// other failure draws from its route's failure bucket (spec §4.2, C1); with
// the bucket empty it answers 429, counted and logged but never audited, and
// fail reports limited. Only failures reach here: a verified assertion never
// consults a bucket, so the buckets cannot lock the runner out.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error, reason string, status int, msg string, passkeyID int64) (limited bool) {
	if status == http.StatusInternalServerError {
		s.internalError(w, r, err, true)
		return false
	}
	if b := s.failureBucket(r); b != nil && !b.AllowN(s.d.Now(), 1) {
		s.countFailure(r)
		// One line per minute at most (limitedLog): the 429 branch is the
		// flood path and the metric above already counts every request.
		if s.limitedLog.AllowN(s.d.Now(), 1) {
			s.d.Log.WarnContext(r.Context(), "passkey check failed", "component", "web", "http.route", r.Pattern,
				"reason", reason, "outcome", "rate_limited")
		}
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, msgTooMany)
		return true
	}
	s.recordFailure(r, reason, passkeyID)
	writeError(w, status, msg)
	return false
}

// recordFailure appends the passkey_failure audit row, counts the failure
// and logs it, never with the request body. With a sampler (builtin mode)
// the rows of the pre-authentication finishes, the failure-bucket groups
// login, enrolment and consent (spec §6), are sampled by
// auth.AuditSampler.Append, which writes the audit_suppressed row first when
// a window opens after a suppression. Every other failure (approve) is
// audited unconditionally. The metric and the log are never sampled.
func (s *Server) recordFailure(r *http.Request, reason string, passkeyID int64) {
	smp := s.d.Sampler
	if s.failureBucket(r) == nil {
		smp = nil // a nil sampler writes every row
	}
	e := store.AuditEvent{At: s.d.Now(), Lane: "public", Actor: "runner", Client: client(r, passkeyID),
		Event: "passkey_failure", Target: failureTarget(r), Outcome: reason}
	if _, err := smp.Append(context.WithoutCancel(r.Context()), s.d.DB, e); err != nil {
		s.d.Log.ErrorContext(r.Context(), "audit append failed", "component", "web", "outcome", "error",
			"error_class", errorClass(err))
	}
	s.countFailure(r)
	s.d.Log.WarnContext(r.Context(), "passkey check failed", "component", "web", "http.route", r.Pattern, "reason", reason)
}

// countFailure counts a failed passkey check in coachd.auth.failures.
func (s *Server) countFailure(r *http.Request) {
	if m := s.d.Metrics; m != nil && m.AuthFailures != nil {
		m.AuthFailures.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("lane", "public"), attribute.String("reason", "passkey")))
	}
}

// failureTarget is the audit target of a failed ceremony: the route pattern
// without its method ("/p/{id}/approve"), so client-chosen path values never
// reach the audit log.
func failureTarget(r *http.Request) string {
	if _, route, ok := strings.Cut(r.Pattern, " "); ok {
		return route
	}
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unknown"
}

// appendAudit writes an audit row; a failure is logged, never shown.
func (s *Server) appendAudit(r *http.Request, e store.AuditEvent) {
	if err := s.d.DB.AppendAudit(context.WithoutCancel(r.Context()), e); err != nil {
		s.d.Log.ErrorContext(r.Context(), "audit append failed", "component", "web", "outcome", "error",
			"error_class", errorClass(err))
	}
}
