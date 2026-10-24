package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/time/rate"

	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/telemetry"
)

type Auditor interface {
	AppendAudit(ctx context.Context, e store.AuditEvent) error
}

// Limiter is a per-(lane, subject) token bucket. Single-user service, so the map is never evicted.
type Limiter struct {
	mu     sync.Mutex
	limit  rate.Limit
	burst  int
	bucket map[string]*rate.Limiter
}

func NewLimiter(perMinute, burst int) *Limiter {
	return &Limiter{limit: rate.Limit(float64(perMinute) / 60.0), burst: burst, bucket: map[string]*rate.Limiter{}}
}

func (l *Limiter) Allow(lane Lane, subject string) bool {
	key := string(lane) + "\x00" + subject
	l.mu.Lock()
	b, ok := l.bucket[key]
	if !ok {
		b = rate.NewLimiter(l.limit, l.burst)
		l.bucket[key] = b
	}
	l.mu.Unlock()
	return b.Allow()
}

func reasonFor(err error) string {
	switch {
	case errors.Is(err, ErrMissing):
		return "missing"
	case errors.Is(err, ErrExpired):
		return "expired"
	case errors.Is(err, ErrRevoked):
		return "revoked"
	case errors.Is(err, ErrUnavailable):
		return "unavailable"
	case errors.Is(err, ErrForbiddenEmail):
		return "forbidden_email"
	default:
		return "invalid"
	}
}

// MiddlewareOptions configures MiddlewareWith.
type MiddlewareOptions struct {
	Lane      Lane
	Verify    func(*http.Request) (Identity, error)
	Limiter   *Limiter // nil: no rate limit; keyed on lane and Identity.Subject
	Auditor   Auditor
	Metrics   *telemetry.Metrics
	Challenge func(err error) string // WWW-Authenticate value; nil: "Bearer"
	Sampler   *AuditSampler          // nil: audit every failure
	// SkipAuditMissing counts ErrMissing in the metric but never audits it.
	SkipAuditMissing bool
}

// Middleware authenticates with verify, auditing every failure.
func Middleware(lane Lane, verify func(*http.Request) (Identity, error), lim *Limiter, a Auditor, m *telemetry.Metrics) func(http.Handler) http.Handler {
	return MiddlewareWith(MiddlewareOptions{Lane: lane, Verify: verify, Limiter: lim, Auditor: a, Metrics: m})
}

// MiddlewareWith is Middleware with a custom challenge, audit sampling and
// optional silence for missing credentials.
func MiddlewareWith(o MiddlewareOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			client := telemetry.ClientClass(r.UserAgent())
			id, err := o.Verify(r)
			if err != nil {
				reason := reasonFor(err)
				unavailable := errors.Is(err, ErrUnavailable)
				if !unavailable && !(o.SkipAuditMissing && errors.Is(err, ErrMissing)) {
					// Audit failure must not turn a 401 into a 500; the metric below still records it.
					_, _ = o.Sampler.Append(context.WithoutCancel(r.Context()), o.Auditor, store.AuditEvent{At: time.Now().UTC(), Lane: string(o.Lane), Client: client,
						Event: "auth_failure", Target: r.URL.Path, Outcome: reason})
				}
				if o.Metrics != nil && o.Metrics.AuthFailures != nil {
					o.Metrics.AuthFailures.Add(r.Context(), 1, metric.WithAttributes(
						attribute.String("lane", string(o.Lane)), attribute.String("reason", reason)))
				}
				if unavailable {
					// Not a credential failure: the client must keep its token and retry.
					w.Header().Set("Retry-After", "5")
					writeJSON(w, http.StatusServiceUnavailable, `{"error":"unavailable"}`)
					return
				}
				challenge := "Bearer"
				if o.Challenge != nil {
					challenge = o.Challenge(err)
				}
				w.Header().Set("WWW-Authenticate", challenge)
				writeJSON(w, http.StatusUnauthorized, `{"error":"unauthorized"}`)
				return
			}
			if o.Limiter != nil && !o.Limiter.Allow(o.Lane, id.Subject) {
				w.Header().Set("Retry-After", "60")
				writeJSON(w, http.StatusTooManyRequests, `{"error":"rate_limited"}`)
				return
			}
			id.Client = client
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
