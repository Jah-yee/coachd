package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/fitworkout"
	"github.com/stefiix92/coachd/internal/passkey"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
	"github.com/stefiix92/coachd/internal/zwiftworkout"
)

const (
	msgApproveExpired = "this approval expired; reload the page"
	msgApproveUsed    = "already used; reload the page"
	msgOtherProposal  = "this approval is for another proposal"
	msgDatePassed     = "this workout's date has passed"
	msgContentChanged = "content changed; reload the page"
	msgNoProposal     = "proposal not found"
	msgEncodeFailed   = "could not encode this workout as FIT"
	msgZwiftFailed    = "could not encode this workout for Zwift"
	msgHashApprove    = "stored content does not match its hash; approval disabled"
	msgHashDownload   = "stored content does not match its hash; download disabled"

	fitContentType = "application/vnd.ant.fit"
)

// fileFormat is one file a runner can download for an approved workout.
// name is the telemetry "format" attribute; unsupported, when set, returns
// the conflict message for a workout this format cannot hold, or "".
type fileFormat struct {
	name, contentType, failMsg string
	fileName                   func(civil.Date, string) string
	encode                     func(workout.Workout, store.Proposal) ([]byte, error)
	unsupported                func(workout.Workout) string
}

var (
	fitFile = fileFormat{name: "fit", contentType: fitContentType, failMsg: msgEncodeFailed, fileName: fitworkout.FileName,
		encode: func(w workout.Workout, p store.Proposal) ([]byte, error) { return fitworkout.Encode(w, p.CreatedAt) }}
	zwoFile = fileFormat{name: "zwo", contentType: zwiftworkout.ContentType, failMsg: msgZwiftFailed, fileName: zwiftworkout.FileName,
		encode: func(w workout.Workout, _ store.Proposal) ([]byte, error) { return zwiftworkout.Encode(w) },
		unsupported: func(w workout.Workout) string {
			if why := zwiftworkout.Unsupported(w); why != "" {
				return "not exportable to Zwift: " + why
			}
			return ""
		}}
)

// proposalPage is the data of proposal.html. Name, notes, rationale and the
// step lines are agent-written: the template shows them escaped and labelled.
type proposalPage struct {
	ID, Name, Day, Sport, Status string
	Lines                        []workout.Line
	Notes, Rationale             string
	Warnings                     []string // coachd's own warning messages
	Replaces                     *replacedView
	ReplacedBy, SameDay          []proposalRow
	Integrity                    string // why approval or download is disabled; empty while intact
	DatePassed                   bool   // pending and dated before today
	NoPasskey                    bool   // pending, but no passkey to approve with
	Approve                      *approveButton
	Decline                      bool   // pending, with a session
	Deliver, Withdraw            bool   // approved, delivering, delivery_failed or delivered, with a session
	Zwift                        bool   // Deliver, and the workout is one Zwift can take
	ZwiftNote                    string // Deliver, cycling, and why Zwift cannot take it
	DeliveryNote                 string // coachd's own status line for the delivery states
	SignIn                       string // what signing in would offer
	WatchHint                    bool
	Error                        string // a conflict to report
}

// replacedView is the proposal a replacement replaces, shown beside it.
type replacedView struct {
	ID, Name, Day, Status string
	Lines                 []workout.Line
	WatchHint             bool
}

// approveButton is what app.js needs to run the approval ceremony.
type approveButton struct{ URL, Ceremony, Options string }

// approveResponse answers a successful approval.
type approveResponse struct {
	OK                bool `json:"ok"`
	ReplacedWithdrawn bool `json:"replaced_withdrawn"`
}

// longDay formats a date as "Wed 28 Oct 2026".
func longDay(d civil.Date) string {
	return time.Date(d.Year, d.Month, d.Day, 12, 0, 0, 0, time.UTC).Format("Mon 2 Jan 2006")
}

// approveExpires is when an approval ceremony begun at now for a workout on
// date ends: min(now+24h, start of the day after date in tz). That start is
// built from the civil date, so a 23- or 25-hour DST day ends at its real
// midnight.
func approveExpires(now time.Time, date civil.Date, tz *time.Location) time.Time {
	next := date.AddDays(1)
	end := time.Date(next.Year, next.Month, next.Day, 0, 0, 0, 0, tz)
	if limit := now.Add(24 * time.Hour); limit.Before(end) {
		return limit
	}
	return end
}

// storedWorkout decodes p's stored content and returns the hash recomputed
// from the stored bytes; it differs from p.ContentHash when the content was
// changed after the proposal was stored.
func storedWorkout(p store.Proposal) (workout.Workout, string, error) {
	var w workout.Workout
	err := json.Unmarshal(p.ContentJSON, &w)
	return w, workout.ContentHash(p.ContentJSON), err
}

// linkTo links a span to the trace that proposed p, when one was stored.
func linkTo(p store.Proposal) []trace.SpanStartOption {
	tid, terr := trace.TraceIDFromHex(p.TraceID)
	sid, serr := trace.SpanIDFromHex(p.SpanID)
	if terr != nil || serr != nil {
		return nil
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, Remote: true})
	return []trace.SpanStartOption{trace.WithLinks(trace.Link{SpanContext: sc})}
}

// proposalView builds p's page. A pending, intact proposal dated today or
// later gets a fresh approval ceremony bound to its ID and the hash of the
// stored bytes.
func (s *Server) proposalView(ctx context.Context, p store.Proposal, signedIn bool) (proposalPage, error) {
	v := proposalPage{ID: p.ID, Name: p.Name, Day: longDay(p.Date), Sport: strings.ReplaceAll(string(p.Sport), "_", " "),
		Status: string(p.Status), Rationale: p.Rationale}
	w, hash, err := storedWorkout(p)
	intact, decoded := hash == p.ContentHash, err == nil
	switch {
	case err == nil:
		v.Lines, v.Notes = workout.Describe(w), w.Notes
	case intact: // tampered content may not decode; intact content must
		return v, fmt.Errorf("web: proposal %s content: %w", p.ID, err)
	}
	var warnings []struct {
		Message string `json:"message"`
	}
	if len(p.WarningsJSON) > 0 {
		if err := json.Unmarshal(p.WarningsJSON, &warnings); err != nil {
			return v, fmt.Errorf("web: proposal %s warnings: %w", p.ID, err)
		}
	}
	for _, x := range warnings {
		v.Warnings = append(v.Warnings, x.Message)
	}
	if p.Replaces != nil {
		old, err := s.d.DB.GetProposal(ctx, *p.Replaces)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return v, err
		default:
			r := &replacedView{ID: old.ID, Name: old.Name, Day: longDay(old.Date), Status: string(old.Status),
				WatchHint: old.Status == store.ProposalWithdrawn}
			if ow, _, err := storedWorkout(old); err == nil {
				r.Lines = workout.Describe(ow)
			}
			v.Replaces = r
		}
	}
	repl, err := s.d.DB.ReplacementsOf(ctx, p.ID)
	if err != nil {
		return v, err
	}
	if v.ReplacedBy, err = proposalRows(repl); err != nil {
		return v, err
	}
	same, err := s.d.DB.ListProposals(ctx, p.Date, p.Date, []store.ProposalStatus{store.ProposalApproved, store.ProposalDelivering,
		store.ProposalDeliveryFailed, store.ProposalDelivered})
	if err != nil {
		return v, err
	}
	same = slices.DeleteFunc(same, func(o store.Proposal) bool { return o.ID == p.ID })
	if v.SameDay, err = proposalRows(same); err != nil {
		return v, err
	}

	now := s.d.Now()
	switch p.Status {
	case store.ProposalPending:
		if signedIn {
			v.Decline = true
		} else {
			v.SignIn = "Sign in to decline"
		}
		v.DatePassed = p.Date.Before(civil.Of(now, s.d.TZ))
		switch {
		case !intact:
			v.Integrity = msgHashApprove
		case v.DatePassed:
		default:
			opts, token, err := s.d.Passkeys.BeginAssert(ctx, passkey.PurposeApprove, p.ID, hash, approveExpires(now, p.Date, s.d.TZ))
			switch {
			case errors.Is(err, passkey.ErrNoPasskeys):
				v.NoPasskey = true
			case err != nil:
				return v, err
			default:
				v.Approve = &approveButton{URL: "/p/" + p.ID + "/approve", Ceremony: token, Options: string(opts)}
			}
		}
	case store.ProposalApproved, store.ProposalDelivering, store.ProposalDeliveryFailed, store.ProposalDelivered:
		switch p.Status {
		case store.ProposalDelivering:
			v.DeliveryNote = "The watch is fetching this workout."
		case store.ProposalDeliveryFailed:
			v.DeliveryNote = "The last delivery to the watch failed; it retries on the next sync."
		}
		if !intact {
			v.Integrity = msgHashDownload
		}
		if signedIn {
			v.Withdraw = true
			v.Deliver = intact
			if intact && decoded && w.Sport == "cycling" {
				if why := zwiftworkout.Unsupported(w); why == "" {
					v.Zwift = true
				} else {
					v.ZwiftNote = "Not exportable to Zwift: " + why
				}
			}
		} else {
			v.SignIn = "Sign in to download or withdraw"
		}
	case store.ProposalWithdrawn:
		// Shown on every withdrawn proposal: only one withdrawn after
		// delivery can be on the watch, and the hint is conditional.
		v.WatchHint = true
	}
	return v, nil
}

// proposal serves GET /p/{id}.
func (s *Server) proposal(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProposal(w, r, false)
	if !ok {
		return
	}
	s.renderProposal(w, r, http.StatusOK, p, "")
}

// renderProposal renders p's page with an optional conflict message.
func (s *Server) renderProposal(w http.ResponseWriter, r *http.Request, status int, p store.Proposal, msg string) {
	_, signedIn := s.session(r)
	v, err := s.proposalView(r.Context(), p, signedIn)
	if err != nil {
		s.internalError(w, r, err, false)
		return
	}
	v.Error = msg
	s.render(w, r, status, "proposal", v)
}

// statusConflict re-reads the proposal and shows its page with
// "this proposal is <status>".
func (s *Server) statusConflict(w http.ResponseWriter, r *http.Request, id string) {
	p, err := s.d.DB.GetProposal(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err, false)
		return
	}
	s.renderProposal(w, r, http.StatusConflict, p, "this proposal is "+string(p.Status))
}

func (s *Server) proposalNotFound(w http.ResponseWriter, r *http.Request, asJSON bool) {
	if asJSON {
		writeError(w, http.StatusNotFound, msgNoProposal)
		return
	}
	s.render(w, r, http.StatusNotFound, "error", errorPage{Title: "Not found", Message: msgNoProposal})
}

// loadProposal reads the proposal the path names. When it is absent it
// answers 404 and returns false.
func (s *Server) loadProposal(w http.ResponseWriter, r *http.Request, asJSON bool) (store.Proposal, bool) {
	p, err := s.d.DB.GetProposal(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.proposalNotFound(w, r, asJSON)
		return p, false
	case err != nil:
		s.internalError(w, r, err, asJSON)
		return p, false
	}
	return p, true
}

// approve serves POST /p/{id}/approve: a fresh passkey assertion bound to
// this proposal and its content hash, required even inside a session (R59).
func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	var req assertBody
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Ceremony == "" || !present(req.Credential) {
		writeError(w, http.StatusBadRequest, msgMalformed)
		return
	}
	p, ok := s.loadProposal(w, r, true)
	if !ok {
		return
	}
	ctx, span := s.d.Tracer.Start(r.Context(), "proposal.approve", linkTo(p)...)
	defer span.End()
	span.SetAttributes(attribute.String("proposal_id", p.ID))
	outcome := s.approveProposal(w, r.WithContext(ctx), p, req)
	span.SetAttributes(attribute.String("outcome", outcome))
}

// approveProposal checks the assertion and approves p, answering the
// request; it returns the outcome for the span.
func (s *Server) approveProposal(w http.ResponseWriter, r *http.Request, p store.Proposal, req assertBody) string {
	ctx := r.Context()
	as, err := s.d.Passkeys.FinishAssert(ctx, req.Ceremony, passkey.PurposeApprove, req.Credential)
	if err != nil {
		return s.approveFailure(w, r, err, 0)
	}
	// The ceremony binds one proposal; its content hash is compared and its
	// nonce consumed in ApproveProposal's transaction.
	if as.Ceremony.ProposalID != p.ID {
		writeError(w, http.StatusBadRequest, msgOtherProposal)
		return "other_proposal"
	}
	// ApproveProposal compares the ceremony's hash with the stored hash
	// column; the stored bytes must also still be the ones the runner saw.
	if workout.ContentHash(p.ContentJSON) != as.Ceremony.ContentHash {
		writeError(w, http.StatusConflict, msgContentChanged)
		return "content_changed"
	}
	now := s.d.Now()
	res, err := s.d.DB.ApproveProposal(ctx, store.Approval{ID: p.ID, ContentHash: as.Ceremony.ContentHash,
		Today: civil.Of(now, s.d.TZ), Nonce: as.Ceremony.Nonce(), NonceExpires: as.Ceremony.Expires, At: now,
		Lane: "public", Client: client(r, as.PasskeyID)})
	var conflict *store.ApprovalConflict
	switch {
	case errors.Is(err, store.ErrNonceUsed): // a concurrent submit of the same assertion won
		return s.approveFailure(w, r, err, as.PasskeyID)
	case errors.As(err, &conflict):
		writeError(w, http.StatusConflict, conflictMessage(conflict))
		return conflict.Reason
	case errors.Is(err, store.ErrNotFound):
		s.proposalNotFound(w, r, true)
		return "not_found"
	case err != nil:
		s.internalError(w, r, err, true)
		return "error"
	}
	if m := s.d.Metrics; m != nil && m.ApprovalLatency != nil {
		m.ApprovalLatency.Record(ctx, now.Sub(p.CreatedAt).Seconds())
	}
	// The approval is committed, so the answer is ok whatever follows. The
	// passkey's sign count and a session follow it; a failure there is
	// logged and leaves no session. A passkey revoked in between gets no
	// session either.
	switch err := s.d.DB.UsePasskey(ctx, as.PasskeyID, as.CredentialJSON, now); {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		s.noSessionAfterApproval(r, p.ID, err)
	default:
		if _, ok := s.session(r); !ok {
			if err := s.startSession(w, r, as.PasskeyID); err != nil {
				s.noSessionAfterApproval(r, p.ID, err)
			}
		}
	}
	writeJSON(w, http.StatusOK, approveResponse{OK: true,
		ReplacedWithdrawn: res.Replaced != nil && res.Replaced.Status == store.ProposalWithdrawn})
	return "approved"
}

// noSessionAfterApproval logs a failure to record the passkey's use or to
// start a session after an approval was committed. The approval stands.
func (s *Server) noSessionAfterApproval(r *http.Request, id string, err error) {
	s.d.Log.ErrorContext(r.Context(), "approval committed; no session started", "component", "web", "http.route", r.Pattern,
		"proposal_id", id, "outcome", "no_session", "error_class", errorClass(err))
}

// approveFailure answers a failed approval ceremony and returns its reason.
// The audit reason is failureClass's; a stale or replayed page gets a 409
// that asks for a reload.
func (s *Server) approveFailure(w http.ResponseWriter, r *http.Request, err error, passkeyID int64) string {
	reason, status, msg := failureClass(err)
	switch {
	case status == http.StatusInternalServerError:
		reason = "error"
	case errors.Is(err, store.ErrNonceUsed):
		status, msg = http.StatusConflict, msgApproveUsed
	case errors.Is(err, passkey.ErrCeremony):
		status, msg = http.StatusConflict, msgApproveExpired
	}
	s.fail(w, r, err, reason, status, msg, passkeyID)
	return reason
}

func conflictMessage(c *store.ApprovalConflict) string {
	switch c.Reason {
	case "date_passed":
		return msgDatePassed
	case "content_changed":
		return msgContentChanged
	}
	return "this proposal is " + string(c.Status)
}

// transition returns the handler of a runner's form POST that moves a
// proposal to `to` from one of `from` (decline, withdraw). It runs behind
// requireSession; the audit client is the session's passkey.
func (s *Server) transition(to store.ProposalStatus, from ...store.ProposalStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws, _ := s.session(r)
		id := r.PathValue("id")
		_, err := s.d.DB.TransitionProposal(r.Context(), id, store.Transition{From: from, To: to, At: s.d.Now(),
			Lane: "public", Actor: "runner", Client: client(r, ws.PasskeyID)})
		switch {
		case errors.Is(err, store.ErrNotFound):
			s.proposalNotFound(w, r, false)
		case errors.Is(err, store.ErrConflict):
			s.statusConflict(w, r, id)
		case err != nil:
			s.internalError(w, r, err, false)
		default:
			http.Redirect(w, r, "/p/"+id, http.StatusSeeOther)
		}
	}
}

// download serves POST /p/{id}/fit and /p/{id}/zwo behind requireSession
// (R62). It encodes the workout on demand from the stored content; nothing is
// stored. The first download of either file moves approved, delivering or
// delivery_failed to delivered (the runner copies it by hand; a later watch
// ack is a no-op); later ones serve the same bytes.
func (s *Server) download(f fileFormat) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.serveDownload(w, r, f)
	}
}

func (s *Server) serveDownload(w http.ResponseWriter, r *http.Request, f fileFormat) {
	p, ok := s.loadProposal(w, r, false)
	if !ok {
		return
	}
	if !slices.Contains(store.DesiredStatuses, p.Status) {
		s.renderProposal(w, r, http.StatusConflict, p, "this proposal is "+string(p.Status))
		return
	}
	wk, hash, err := storedWorkout(p)
	if hash != p.ContentHash { // the runner approved other bytes
		s.downloadFailed(w, r, f, p.ID, "integrity", msgHashDownload)
		return
	}
	if err == nil && f.unsupported != nil {
		if msg := f.unsupported(wk); msg != "" {
			s.renderProposal(w, r, http.StatusConflict, p, msg)
			return
		}
	}
	var b []byte
	if err == nil {
		b, err = f.encode(wk, p)
	}
	if err != nil {
		s.downloadFailed(w, r, f, p.ID, errorClass(err), f.failMsg)
		return
	}

	ctx, span := s.d.Tracer.Start(r.Context(), "proposal.deliver", linkTo(p)...)
	defer span.End()
	span.SetAttributes(attribute.String("proposal_id", p.ID), attribute.String("format", f.name))
	outcome := "redownload"
	if p.Status != store.ProposalDelivered {
		ws, _ := s.session(r)
		_, err := s.d.DB.TransitionProposal(ctx, p.ID, store.Transition{From: undelivered,
			To: store.ProposalDelivered, At: s.d.Now(), Lane: "public", Actor: "runner", Client: client(r, ws.PasskeyID)})
		switch {
		case err == nil:
			outcome = "delivered"
		case errors.Is(err, store.ErrConflict):
			// Changed since it was read. A concurrent download or watch ack
			// delivered it: serve it again. Otherwise (withdrawn, expired)
			// refuse.
			cur, err := s.d.DB.GetProposal(ctx, p.ID)
			if err != nil {
				span.SetAttributes(attribute.String("outcome", "error"))
				s.internalError(w, r, err, false)
				return
			}
			if cur.Status != store.ProposalDelivered {
				span.SetAttributes(attribute.String("outcome", "conflict"))
				s.renderProposal(w, r, http.StatusConflict, cur, "this proposal is "+string(cur.Status))
				return
			}
		default:
			span.SetAttributes(attribute.String("outcome", "error"))
			s.internalError(w, r, err, false)
			return
		}
	}
	span.SetAttributes(attribute.String("outcome", outcome))
	if m := s.d.Metrics; m != nil && m.Deliveries != nil {
		m.Deliveries.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome), attribute.String("format", f.name)))
	}
	h := w.Header()
	h.Set("Content-Type", f.contentType)
	h.Set("Content-Disposition", `attachment; filename="`+f.fileName(p.Date, p.ID)+`"`)
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// undelivered are the statuses a download moves to delivered.
var undelivered = []store.ProposalStatus{store.ProposalApproved, store.ProposalDelivering, store.ProposalDeliveryFailed}

// downloadFailed answers a download that cannot be served: 500 with msg on
// the error page, and no transition.
func (s *Server) downloadFailed(w http.ResponseWriter, r *http.Request, f fileFormat, id, class, msg string) {
	s.d.Log.ErrorContext(r.Context(), "workout download failed", "component", "web", "http.route", r.Pattern,
		"proposal_id", id, "format", f.name, "outcome", "error", "error_class", class)
	s.render(w, r, http.StatusInternalServerError, "error", errorPage{Title: "Download failed", Message: msg})
}
