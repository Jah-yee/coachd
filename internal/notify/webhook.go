package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stefiix92/coachd/internal/outcome"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/telemetry"
)

// EventActivityStored is the event of an activity coachd just stored.
const EventActivityStored = "activity.stored"

// Event is the JSON body a webhook receives. It carries IDs, a date and a
// sport only: no health values, names or other free text.
type Event struct {
	Event      string  `json:"event"`
	ActivityID int64   `json:"activity_id"`
	Date       string  `json:"date"`
	Sport      string  `json:"sport"`
	ProposalID *string `json:"proposal_id"` // the delivered workout it carried out, or null
}

// Webhook posts an Event to the runner's own automation (n8n, Home
// Assistant, an agent's trigger) when a recent activity is stored, so a
// post-run review can start without waiting for the next chat. The URL and
// token are secrets and are never logged.
type Webhook struct {
	url, token string
	db         *store.DB
	client     *http.Client
	m          *telemetry.Metrics
	log        *slog.Logger
	tracer     trace.Tracer
	queue      chan Event
	sleep      func(ctx context.Context, d time.Duration) error
}

// NewWebhook posts to url with token as a bearer token (may be empty). db
// links activities to proposals.
func NewWebhook(url, token string, db *store.DB, client *http.Client,
	m *telemetry.Metrics, log *slog.Logger, tracer trace.Tracer) *Webhook {
	if client == nil {
		client = http.DefaultClient
	}
	return &Webhook{url: url, token: token, db: db, client: client, m: m, log: log, tracer: tracer,
		queue: make(chan Event, queueSize), sleep: sleepCtx}
}

// ActivityStored enqueues the event of a: its link to a delivered proposal
// is looked up from the stored detail, and none when that fails. It never
// blocks; a full queue drops the event.
func (w *Webhook) ActivityStored(ctx context.Context, a store.Activity) {
	ev := Event{Event: EventActivityStored, ActivityID: a.ID, Date: a.Date.String(), Sport: string(a.Sport)}
	if d, err := w.db.GetActivityDetail(ctx, a.ID); err == nil && d.Workout != nil {
		ev.ProposalID, _ = outcome.Linker{DB: w.db}.ProposalFor(ctx,
			store.ActivityWorkout{ID: a.ID, Date: a.Date, Fingerprint: d.Workout.Fingerprint})
	}
	select {
	case w.queue <- ev:
	default:
		w.count(ctx, "dropped")
		w.log.Warn("webhook event dropped", "component", "notify", "activity_id", a.ID, "outcome", "dropped")
	}
}

func (w *Webhook) count(ctx context.Context, outcome string) {
	if w.m != nil {
		w.m.WebhookSends.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}

// Run sends queued events until ctx is done.
func (w *Webhook) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-w.queue:
			w.deliver(ctx, ev)
		}
	}
}

func (w *Webhook) deliver(ctx context.Context, ev Event) {
	ctx, span := w.tracer.Start(ctx, "activity.webhook")
	defer span.End()
	span.SetAttributes(attribute.Int64("activity_id", ev.ActivityID))
	outcome, status, errClass := retry(ctx, w.sleep, func(ctx context.Context) (int, string) { return w.send(ctx, ev) })
	if outcome == "" {
		return
	}
	w.count(ctx, outcome)
	span.SetAttributes(attribute.String("outcome", outcome))
	if outcome == "sent" {
		return
	}
	span.SetStatus(codes.Error, outcome)
	if errClass == "" {
		errClass = "none"
	}
	w.log.Warn("webhook event failed", "component", "notify", "activity_id", ev.ActivityID, "outcome", outcome,
		"status_class", statusClass(status), "error_class", errClass)
}

// send makes one attempt, as Ntfy.send does.
func (w *Webhook) send(ctx context.Context, ev Event) (int, string) {
	actx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	body, err := json.Marshal(ev)
	if err != nil {
		return 0, fmt.Sprintf("%T", err)
	}
	req, err := http.NewRequestWithContext(actx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Sprintf("%T", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "coachd")
	if w.token != "" {
		req.Header.Set("Authorization", "Bearer "+w.token)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return 0, fmt.Sprintf("%T", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, ""
}
