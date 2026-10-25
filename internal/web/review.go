package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/passkey"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
)

// maxApproveAll caps the workouts one approve-all approves.
const maxApproveAll = 20

// Why a pending proposal is left out of the approve-all set.
const (
	whyDatePassed = "its date has passed"
	whyTampered   = "its stored content does not match its hash"
	whyReplaced   = "another waiting workout replaces it"
	whyOverCap    = "more than 20 are waiting; approve these first"
)

const msgSetChanged = "the workouts waiting have changed; reload the page"

// reviewPage is the data of review.html: the approve-all set in full, then
// the pending proposals left out of it.
type reviewPage struct {
	Items     []proposalPage
	Excluded  []excludedRow
	NoPasskey bool
	Approve   *approveButton
	Error     string
}

// excludedRow is a pending proposal outside the set, with the reason.
type excludedRow struct {
	Row    proposalRow
	Reason string
}

// approveAllResponse answers a successful approve-all.
type approveAllResponse struct {
	OK    bool `json:"ok"`
	Count int  `json:"count"`
}

// approvableSet splits pending proposals (by date, then creation) into the
// approve-all set and the rest: the set holds intact proposals dated today
// or later that no other pending proposal replaces, at most maxApproveAll.
func approvableSet(pending []store.Proposal, today civil.Date) (set []store.Proposal, out []excludedRow, err error) {
	replaced := map[string]bool{}
	for _, p := range pending {
		if p.Replaces != nil {
			replaced[*p.Replaces] = true
		}
	}
	for _, p := range pending {
		why := ""
		switch {
		case workout.ContentHash(p.ContentJSON) != p.ContentHash:
			why = whyTampered
		case p.Date.Before(today):
			why = whyDatePassed
		case replaced[p.ID]:
			why = whyReplaced
		case len(set) == maxApproveAll:
			why = whyOverCap
		}
		if why == "" {
			set = append(set, p)
			continue
		}
		rows, err := proposalRows([]store.Proposal{p})
		if err != nil {
			return nil, nil, err
		}
		out = append(out, excludedRow{Row: rows[0], Reason: why})
	}
	return set, out, nil
}

// approveAllExpires is when an approve-all ceremony for set ends: the single
// approval's rule for its earliest workout.
func approveAllExpires(now time.Time, set []store.Proposal, tz *time.Location) time.Time {
	first := set[0].Date
	for _, p := range set[1:] {
		if p.Date.Before(first) {
			first = p.Date
		}
	}
	return approveExpires(now, first, tz)
}

// reviewView builds the review page, with a fresh approve-all ceremony bound
// to every proposal in the set.
func (s *Server) reviewView(ctx context.Context) (reviewPage, error) {
	var v reviewPage
	pending, err := s.d.DB.ListProposals(ctx, firstDate, lastDate, []store.ProposalStatus{store.ProposalPending})
	if err != nil {
		return v, err
	}
	now := s.d.Now()
	set, excluded, err := approvableSet(pending, civil.Of(now, s.d.TZ))
	if err != nil {
		return v, err
	}
	v.Excluded = excluded
	if len(set) == 0 {
		return v, nil
	}
	bound := make([]passkey.Bound, 0, len(set))
	for _, p := range set {
		item, _, err := s.proposalDetail(ctx, p)
		if err != nil {
			return v, err
		}
		v.Items = append(v.Items, item)
		bound = append(bound, passkey.Bound{ID: p.ID, ContentHash: p.ContentHash})
	}
	opts, token, err := s.d.Passkeys.BeginApproveAll(ctx, bound, approveAllExpires(now, set, s.d.TZ))
	switch {
	case errors.Is(err, passkey.ErrNoPasskeys):
		v.NoPasskey = true
	case err != nil:
		return v, err
	default:
		v.Approve = &approveButton{URL: "/review/approve", Ceremony: token, Options: string(opts)}
	}
	return v, nil
}

// review serves GET /review.
func (s *Server) review(w http.ResponseWriter, r *http.Request) {
	v, err := s.reviewView(r.Context())
	if err != nil {
		s.internalError(w, r, err, false)
		return
	}
	s.render(w, r, http.StatusOK, "review", v)
}

// approveAll serves POST /review/approve: a fresh passkey assertion bound to
// every proposal of the review page and its content hash, required even
// inside a session, approving all of them or none.
func (s *Server) approveAll(w http.ResponseWriter, r *http.Request) {
	var req assertBody
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Ceremony == "" || !present(req.Credential) {
		writeError(w, http.StatusBadRequest, msgMalformed)
		return
	}
	ctx, span := s.d.Tracer.Start(r.Context(), "proposal.approve_all")
	defer span.End()
	outcome := s.approveSet(w, r.WithContext(ctx), req)
	span.SetAttributes(attribute.String("outcome", outcome))
}

// approveSet checks the assertion and approves its set, answering the
// request; it returns the outcome for the span.
func (s *Server) approveSet(w http.ResponseWriter, r *http.Request, req assertBody) string {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)
	as, err := s.d.Passkeys.FinishAssert(ctx, req.Ceremony, passkey.PurposeApproveAll, req.Credential)
	if err != nil {
		return s.approveFailure(w, r, err, 0)
	}
	bound := as.Ceremony.Proposals
	span.SetAttributes(attribute.Int("count", len(bound)))
	// The ceremony binds the set; each hash is compared and the nonce
	// consumed in ApproveProposals's transaction. The stored bytes must also
	// still be the ones the runner saw.
	byID := map[string]store.Proposal{}
	items := make([]store.ApprovalItem, 0, len(bound))
	for _, b := range bound {
		p, err := s.d.DB.GetProposal(ctx, b.ID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusConflict, msgSetChanged)
			return "not_found"
		case err != nil:
			s.internalError(w, r, err, true)
			return "error"
		}
		if workout.ContentHash(p.ContentJSON) != b.ContentHash {
			writeError(w, http.StatusConflict, longDay(p.Date)+": "+msgContentChanged)
			return "content_changed"
		}
		if l, ok := traceLink(p); ok {
			span.AddLink(l)
		}
		byID[p.ID] = p
		items = append(items, store.ApprovalItem{ID: b.ID, ContentHash: b.ContentHash})
	}
	now := s.d.Now()
	_, err = s.d.DB.ApproveProposals(ctx, store.ApprovalSet{Items: items, Today: civil.Of(now, s.d.TZ),
		Nonce: as.Ceremony.Nonce(), NonceExpires: as.Ceremony.Expires, At: now, Lane: "public", Client: client(r, as.PasskeyID)})
	var sc *store.SetConflict
	var conflict *store.ApprovalConflict
	switch {
	case errors.Is(err, store.ErrNonceUsed): // a concurrent submit of the same assertion won
		return s.approveFailure(w, r, err, as.PasskeyID)
	case errors.As(err, &sc) && errors.As(err, &conflict):
		msg := msgContentChanged // it already asks for a reload
		if conflict.Reason != "content_changed" {
			msg = conflictMessage(conflict) + "; reload the page"
		}
		writeError(w, http.StatusConflict, longDay(byID[sc.ID].Date)+": "+msg)
		return conflict.Reason
	case errors.As(err, &sc): // deleted in between
		writeError(w, http.StatusConflict, msgSetChanged)
		return "not_found"
	case err != nil:
		s.internalError(w, r, err, true)
		return "error"
	}
	if m := s.d.Metrics; m != nil && m.ApprovalLatency != nil {
		for _, p := range byID {
			m.ApprovalLatency.Record(ctx, now.Sub(p.CreatedAt).Seconds())
		}
	}
	s.afterApproval(w, r, as, now, "count", len(items))
	writeJSON(w, http.StatusOK, approveAllResponse{OK: true, Count: len(items)})
	return "approved"
}
