package deviceapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/fitworkout"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
)

const (
	windowDays     = 14 // the desired set runs from today to today+14 in TZ
	maxDesired     = 30
	fitContentType = "application/vnd.ant.fit" // the watch checks it
)

// desiredSet answers GET /device/v1/workouts.
type desiredSet struct {
	Today    string           `json:"today"`
	Workouts []desiredWorkout `json:"workouts"`
}

// desiredWorkout is one entry; Name is exactly the FIT wkt_name.
type desiredWorkout struct {
	ID   string `json:"id"`
	Date string `json:"date"`
	Name string `json:"name"`
}

// ackResponse answers a successful POST /device/v1/workouts/{id}/ack.
type ackResponse struct {
	Status string `json:"status"`
}

// desiredWorkouts returns today in TZ and the proposals the watch keeps.
func (s *Server) desiredWorkouts(ctx context.Context) (civil.Date, []store.Proposal, error) {
	today := civil.Of(s.d.Now(), s.d.TZ)
	ps, err := s.d.DB.DesiredWorkouts(ctx, today, today.AddDays(windowDays), maxDesired)
	return today, ps, err
}

// desired serves GET /device/v1/workouts.
func (s *Server) desired(w http.ResponseWriter, r *http.Request) {
	today, ps, err := s.desiredWorkouts(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := desiredSet{Today: today.String(), Workouts: make([]desiredWorkout, 0, len(ps))}
	for _, p := range ps {
		out.Workouts = append(out.Workouts, desiredWorkout{ID: p.ID, Date: p.Date.String(), Name: fitworkout.WorkoutName(p.Name)})
	}
	writeJSON(w, http.StatusOK, out)
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

// startSpan starts a span for a known proposal and device. The ids are
// attributes only once the proposal is known: a path id may be anything.
func (s *Server) startSpan(r *http.Request, name string, p store.Proposal, dev store.Device) (*http.Request, trace.Span) {
	ctx, span := s.d.Tracer.Start(r.Context(), name, linkTo(p)...)
	span.SetAttributes(attribute.String("proposal_id", p.ID), attribute.String("device_id", strconv.FormatInt(dev.ID, 10)))
	return r.WithContext(ctx), span
}

func (s *Server) countDelivery(ctx context.Context, outcome string) {
	if m := s.d.Metrics; m != nil && m.Deliveries != nil {
		m.Deliveries.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}

// fit serves GET /device/v1/workouts/{id}/fit: 404 unless the id is in the
// current desired set. It encodes first, then records the fetch, which moves
// approved or delivery_failed to delivering.
func (s *Server) fit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validHexID(id) {
		notFound(w, r)
		return
	}
	_, ps, err := s.desiredWorkouts(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	i := slices.IndexFunc(ps, func(p store.Proposal) bool { return p.ID == id })
	if i < 0 {
		notFound(w, r)
		return
	}
	r, span := s.startSpan(r, "device.fit", ps[i], deviceFrom(r.Context()))
	defer span.End()
	span.SetAttributes(attribute.String("outcome", s.serveFIT(w, r, ps[i])))
}

// serveFIT answers the download of p and returns the span outcome.
func (s *Server) serveFIT(w http.ResponseWriter, r *http.Request, p store.Proposal) string {
	ctx := r.Context()
	dev := deviceFrom(ctx)
	// The runner approved the bytes behind ContentHash; never send others.
	if workout.ContentHash(p.ContentJSON) != p.ContentHash {
		s.serverError(w, r, "integrity")
		return "integrity"
	}
	var wk workout.Workout
	if err := json.Unmarshal(p.ContentJSON, &wk); err != nil {
		s.internalError(w, r, err)
		return "error"
	}
	b, err := fitworkout.Encode(wk, p.CreatedAt)
	if err != nil {
		s.internalError(w, r, err)
		return "error"
	}
	switch _, err := s.d.DB.RecordFetch(ctx, p.ID, dev.ID, s.d.Now()); {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrConflict):
		// Left the desired set since it was read (withdrawn, expired).
		notFound(w, r)
		return "not_found"
	case err != nil:
		s.internalError(w, r, err)
		return "error"
	}
	s.countDelivery(ctx, "watch_fetch")
	s.d.Log.InfoContext(ctx, "workout fetched", "component", "deviceapi", "device_id", strconv.FormatInt(dev.ID, 10),
		"proposal_id", p.ID, "outcome", "fetched")
	h := w.Header()
	h.Set("Content-Type", fitContentType)
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
	return "fetched"
}

// ack serves POST /device/v1/workouts/{id}/ack: 404 unless this device has
// fetched the id. The body (the watch sends {}) is read for the cap and
// otherwise ignored.
func (s *Server) ack(w http.ResponseWriter, r *http.Request) {
	if _, err := readBody(r); err != nil {
		writeError(w, http.StatusBadRequest, msgMalformed)
		return
	}
	id := r.PathValue("id")
	if !validHexID(id) {
		notFound(w, r)
		return
	}
	p, err := s.d.DB.GetProposal(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, r)
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	r, span := s.startSpan(r, "device.ack", p, deviceFrom(r.Context()))
	defer span.End()
	span.SetAttributes(attribute.String("outcome", s.acknowledge(w, r, p)))
}

// acknowledge records the ack of p, read before the call, and returns the
// span outcome. Only a real transition to delivered is counted.
func (s *Server) acknowledge(w http.ResponseWriter, r *http.Request, p store.Proposal) string {
	ctx := r.Context()
	dev := deviceFrom(ctx)
	now := s.d.Now()
	got, err := s.d.DB.AckDelivery(ctx, p.ID, dev.ID, now)
	var conflict *store.StatusConflict
	switch {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, r)
		return "not_found"
	case errors.As(err, &conflict):
		writeError(w, http.StatusConflict, "workout is "+string(conflict.Status))
		return "conflict"
	case err != nil:
		s.internalError(w, r, err)
		return "error"
	}
	outcome := "already_delivered"
	// AckDelivery sets StatusAt to now only when it moved the status; an
	// earlier ack (by any device) left the stored time.
	if p.Status != store.ProposalDelivered && got.StatusAt.Equal(now) {
		outcome = "delivered"
		s.countDelivery(ctx, "watch_ack")
		s.d.Log.InfoContext(ctx, "workout delivered", "component", "deviceapi", "device_id", strconv.FormatInt(dev.ID, 10),
			"proposal_id", p.ID, "outcome", outcome)
	}
	writeJSON(w, http.StatusOK, ackResponse{Status: string(store.ProposalDelivered)})
	return outcome
}
