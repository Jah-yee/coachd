// Package notify tells the runner about new pending proposals, and in
// builtin auth mode about app connections, through ntfy. A proposal notice
// carries only a date and a link: no workout name, metrics or health data. A
// connection notice is a fixed text and a link to /apps: no client name. The
// topic URL and token are secrets and are never logged.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/telemetry"
)

const (
	queueSize      = 32
	attemptTimeout = 10 * time.Second
)

// backoff is the wait before attempts 2 and 3 (attempts at 0 s, 5 s and 30 s).
var backoff = []time.Duration{5 * time.Second, 25 * time.Second}

// noticeKind tells a proposal notice from the two connection notices.
type noticeKind int

const (
	kindProposal noticeKind = iota
	kindAppConnected
	kindConnectionRevoked
)

type notice struct {
	kind noticeKind
	id   string // proposal notices only
	date civil.Date
	link trace.SpanContext
}

// Connection notice texts (spec §9, M14). They never name the client.
const (
	textAppConnected      = "A new app was connected to coachd"
	textConnectionRevoked = "coachd revoked an app connection after a token was reused"
)

// Ntfy sends one notification per pending proposal or connection event from
// a single worker.
type Ntfy struct {
	topicURL, token, publicURL string
	client                     *http.Client
	m                          *telemetry.Metrics
	log                        *slog.Logger
	tracer                     trace.Tracer
	queue                      chan notice
	sleep                      func(ctx context.Context, d time.Duration) error
}

// NewNtfy sends to topicURL (the full topic URL). token may be empty.
func NewNtfy(topicURL, token, publicURL string, client *http.Client,
	m *telemetry.Metrics, log *slog.Logger, tracer trace.Tracer) *Ntfy {
	if client == nil {
		client = http.DefaultClient
	}
	return &Ntfy{
		topicURL: topicURL, token: token, publicURL: strings.TrimRight(publicURL, "/"),
		client: client, m: m, log: log, tracer: tracer,
		queue: make(chan notice, queueSize),
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
}

func (n *Ntfy) count(ctx context.Context, outcome string) {
	if n.m != nil {
		n.m.NotifySends.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}

// ProposalPending enqueues a notice; it never blocks. A full queue drops it.
func (n *Ntfy) ProposalPending(id string, date civil.Date, link trace.SpanContext) {
	n.enqueue(notice{id: id, date: date, link: link})
}

// AppConnected enqueues "a new app was connected"; it never blocks (the
// token endpoint calls it inline). A full queue drops it.
func (n *Ntfy) AppConnected() { n.enqueue(notice{kind: kindAppConnected}) }

// ConnectionRevoked enqueues "an app connection was revoked after a token
// was reused"; it never blocks. A full queue drops it.
func (n *Ntfy) ConnectionRevoked() { n.enqueue(notice{kind: kindConnectionRevoked}) }

// enqueue queues nt without blocking; a full queue drops and counts it.
func (n *Ntfy) enqueue(nt notice) {
	select {
	case n.queue <- nt:
	default:
		n.count(context.Background(), "dropped")
		n.log.Warn("notification dropped", n.logAttrs(nt, "outcome", "dropped")...)
	}
}

// logAttrs is the component, the proposal id (proposal notices only) and kv.
func (n *Ntfy) logAttrs(nt notice, kv ...any) []any {
	attrs := []any{"component", "notify"}
	if nt.kind == kindProposal {
		attrs = append(attrs, "proposal_id", nt.id)
	}
	return append(attrs, kv...)
}

// Run sends queued notices until ctx is done.
func (n *Ntfy) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case nt := <-n.queue:
			n.deliver(ctx, nt)
		}
	}
}

func (n *Ntfy) deliver(ctx context.Context, nt notice) {
	var opts []trace.SpanStartOption
	if nt.link.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: nt.link}))
	}
	name := "proposal.notify"
	if nt.kind != kindProposal {
		name = "connection.notify"
	}
	ctx, span := n.tracer.Start(ctx, name, opts...)
	defer span.End()
	if nt.kind == kindProposal {
		span.SetAttributes(attribute.String("proposal_id", nt.id))
	}

	var status int
	var errClass string
	for attempt := 0; attempt <= len(backoff); attempt++ {
		if attempt > 0 {
			if n.sleep(ctx, backoff[attempt-1]) != nil {
				return
			}
		}
		status, errClass = n.send(ctx, nt)
		if ctx.Err() != nil {
			return
		}
		switch {
		case status >= 200 && status < 300:
			n.finish(ctx, span, "sent")
			return
		case status >= 400 && status < 500 && status != http.StatusTooManyRequests:
			n.finish(ctx, span, "rejected")
			n.fail(nt, "rejected", status, errClass)
			return
		}
	}
	n.finish(ctx, span, "error")
	n.fail(nt, "error", status, errClass)
}

// finish counts the outcome and records it on the span, marking the span as
// an error unless the notice was sent.
func (n *Ntfy) finish(ctx context.Context, span trace.Span, outcome string) {
	n.count(ctx, outcome)
	span.SetAttributes(attribute.String("outcome", outcome))
	if outcome != "sent" {
		span.SetStatus(codes.Error, outcome)
	}
}

func (n *Ntfy) fail(nt notice, outcome string, status int, errClass string) {
	if errClass == "" {
		errClass = "none"
	}
	n.log.Warn("notification failed", n.logAttrs(nt, "outcome", outcome,
		"status_class", statusClass(status), "error_class", errClass)...)
}

// message is the notice's body and ntfy Actions header.
func (n *Ntfy) message(nt notice) (body, actions string) {
	switch nt.kind {
	case kindAppConnected:
		return textAppConnected, fmt.Sprintf("view, Open, %s/apps, clear=true", n.publicURL)
	case kindConnectionRevoked:
		return textConnectionRevoked, fmt.Sprintf("view, Open, %s/apps, clear=true", n.publicURL)
	}
	return fmt.Sprintf("Workout for %s ready to review", time.Date(nt.date.Year, nt.date.Month, nt.date.Day, 0, 0, 0, 0, time.UTC).Format("Mon 2 Jan")),
		fmt.Sprintf("view, Review, %s/p/%s, clear=true", n.publicURL, nt.id)
}

// send makes one attempt. It returns the HTTP status (0 on a network error)
// and the error class (the error's Go type, never its text: that can hold the URL).
func (n *Ntfy) send(ctx context.Context, nt notice) (int, string) {
	actx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	body, actions := n.message(nt)
	req, err := http.NewRequestWithContext(actx, http.MethodPost, n.topicURL, strings.NewReader(body))
	if err != nil {
		return 0, fmt.Sprintf("%T", err)
	}
	req.Header.Set("Title", "coachd")
	req.Header.Set("Actions", actions)
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return 0, fmt.Sprintf("%T", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, ""
}

func statusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	}
	return "error"
}
