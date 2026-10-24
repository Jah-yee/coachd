package auth

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/stefiix92/coachd/internal/store"
)

// auditWindow is the sampler's fixed window.
const auditWindow = time.Hour

// AuditSampler caps failure audit rows at perHour per fixed one-hour window
// (spec §6, I1), so strangers cannot fill the disk with them. A window starts
// at the first Allow after the previous one ended. The count of rows refused
// in a window is reported once, by the first Allow of the next window, for
// the caller to write as one audit_suppressed row. Metrics and logs are never
// sampled: callers count and log every failure whatever Allow says.
type AuditSampler struct {
	mu         sync.Mutex
	perHour    int
	now        func() time.Time
	started    bool      // a window has started (the first Allow happened)
	start      time.Time // start of the running window
	count      int       // rows allowed in the running window
	suppressed int       // rows refused in the running window
}

// NewAuditSampler returns a sampler allowing perHour rows an hour (at least
// one). now is the clock; nil means time.Now.
func NewAuditSampler(perHour int, now func() time.Time) *AuditSampler {
	if now == nil {
		now = time.Now
	}
	return &AuditSampler{perHour: max(perHour, 1), now: now}
}

// Allow reports whether one more audit row may be written now. suppressedPrev
// is the number of rows refused in the previous window: non-zero once, on the
// first Allow of a new window after a suppression, and that Allow is always
// allowed, so the count is never lost.
func (s *AuditSampler) Allow() (ok bool, suppressedPrev int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !s.started || !now.Before(s.start.Add(auditWindow)) {
		suppressedPrev = s.suppressed
		s.started, s.start, s.count, s.suppressed = true, now, 0, 0
	}
	if s.count < s.perHour {
		s.count++
		return true, suppressedPrev
	}
	s.suppressed++
	return false, 0
}

// SuppressedTarget is the target of the audit_suppressed row: neutral, since
// one sampler serves every pre-authentication failure (web passkey checks
// and /mcp bearer checks).
const SuppressedTarget = "pre_auth_failures"

// Append writes the audit row e through a when the sampler allows it. On the
// first Allow of a window after a suppression it first writes one
// audit_suppressed row (e's time, lane and actor; Target SuppressedTarget;
// Outcome the count refused). A nil sampler writes e unconditionally.
// written reports whether e was allowed; err joins any write failures, which
// never change written.
func (s *AuditSampler) Append(ctx context.Context, a Auditor, e store.AuditEvent) (written bool, err error) {
	if s == nil {
		return true, a.AppendAudit(ctx, e)
	}
	ok, suppressed := s.Allow()
	if suppressed > 0 {
		err = a.AppendAudit(ctx, store.AuditEvent{At: e.At, Lane: e.Lane, Actor: e.Actor,
			Event: "audit_suppressed", Target: SuppressedTarget, Outcome: strconv.Itoa(suppressed)})
	}
	if !ok {
		return false, err
	}
	return true, errors.Join(err, a.AppendAudit(ctx, e))
}
