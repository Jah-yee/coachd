package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/store"
)

type sampleClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *sampleClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *sampleClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestAuditSampler(t *testing.T) {
	clk := &sampleClock{t: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
	s := NewAuditSampler(60, clk.now)
	for i := range 60 {
		if ok, n := s.Allow(); !ok || n != 0 {
			t.Fatalf("allow %d = (%v, %d), want (true, 0)", i+1, ok, n)
		}
	}
	for i := range 5 {
		if ok, n := s.Allow(); ok || n != 0 {
			t.Fatalf("allow %d past the limit = (%v, %d), want (false, 0)", 61+i, ok, n)
		}
	}
	clk.add(59 * time.Minute)
	if ok, _ := s.Allow(); ok {
		t.Fatal("allowed before the hour ended")
	}
	clk.add(time.Minute) // the window started at the first Allow: exactly 1 h later it rolls
	if ok, n := s.Allow(); !ok || n != 6 {
		t.Fatalf("first allow of the new window = (%v, %d), want (true, 6)", ok, n)
	}
	if ok, n := s.Allow(); !ok || n != 0 {
		t.Fatalf("second allow of the new window = (%v, %d), want (true, 0): the count is reported once", ok, n)
	}
}

func TestAuditSamplerWindowStartsAtFirstAllow(t *testing.T) {
	clk := &sampleClock{t: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
	s := NewAuditSampler(1, clk.now)
	clk.add(30 * time.Minute) // nothing happened yet: no window is running
	if ok, n := s.Allow(); !ok || n != 0 {
		t.Fatalf("first allow = (%v, %d)", ok, n)
	}
	if ok, _ := s.Allow(); ok {
		t.Fatal("second allow in the window")
	}
	clk.add(59 * time.Minute) // 89 min after construction, 59 min into the window
	if ok, _ := s.Allow(); ok {
		t.Fatal("window rolled before an hour from its first Allow")
	}
	clk.add(5 * time.Hour) // a quiet gap: the next Allow opens a new window and reports the old count
	if ok, n := s.Allow(); !ok || n != 2 {
		t.Fatalf("after the gap = (%v, %d), want (true, 2)", ok, n)
	}
}

func TestAuditSamplerNoSuppressionReportsZero(t *testing.T) {
	clk := &sampleClock{t: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
	s := NewAuditSampler(3, clk.now)
	s.Allow()
	clk.add(2 * time.Hour)
	if ok, n := s.Allow(); !ok || n != 0 {
		t.Fatalf("(%v, %d), want (true, 0)", ok, n)
	}
}

func TestAuditSamplerConcurrent(t *testing.T) {
	clk := &sampleClock{t: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
	s := NewAuditSampler(60, clk.now)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 200 {
		wg.Go(func() {
			if ok, _ := s.Allow(); ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if allowed != 60 {
		t.Fatalf("%d allowed, want 60", allowed)
	}
	clk.add(time.Hour)
	if ok, n := s.Allow(); !ok || n != 140 {
		t.Fatalf("(%v, %d), want (true, 140)", ok, n)
	}
}

type rowAuditor struct {
	rows []store.AuditEvent
	err  error
}

func (a *rowAuditor) AppendAudit(_ context.Context, e store.AuditEvent) error {
	a.rows = append(a.rows, e)
	return a.err
}

func TestAuditSamplerAppend(t *testing.T) {
	clk := &sampleClock{t: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
	s := NewAuditSampler(2, clk.now)
	a := &rowAuditor{}
	e := store.AuditEvent{At: clk.now(), Lane: "public", Actor: "runner", Event: "passkey_failure", Target: "/login/finish", Outcome: "invalid"}
	for i, want := range []bool{true, true, false, false, false} {
		if written, err := s.Append(context.Background(), a, e); written != want || err != nil {
			t.Fatalf("append %d = (%v, %v), want (%v, nil)", i+1, written, err, want)
		}
	}
	if len(a.rows) != 2 {
		t.Fatalf("%d rows, want 2", len(a.rows))
	}
	clk.add(time.Hour)
	e.At = clk.now()
	if written, err := s.Append(context.Background(), a, e); !written || err != nil {
		t.Fatalf("append in the new window = (%v, %v)", written, err)
	}
	if len(a.rows) != 4 {
		t.Fatalf("%d rows, want 4: %+v", len(a.rows), a.rows)
	}
	want := store.AuditEvent{At: e.At, Lane: "public", Actor: "runner", Event: "audit_suppressed", Target: "pre_auth_failures", Outcome: "3"}
	if a.rows[2] != want || a.rows[3] != e {
		t.Fatalf("rows %+v, want %+v then %+v", a.rows[2:], want, e)
	}
}

func TestAuditSamplerAppendNil(t *testing.T) {
	var s *AuditSampler
	a := &rowAuditor{}
	e := store.AuditEvent{Event: "passkey_failure"}
	for range 3 {
		if written, err := s.Append(context.Background(), a, e); !written || err != nil {
			t.Fatalf("(%v, %v)", written, err)
		}
	}
	if len(a.rows) != 3 {
		t.Fatalf("a nil sampler must write every row: %d", len(a.rows))
	}
}

func TestAuditSamplerAppendError(t *testing.T) {
	boom := errors.New("disk full")
	a := &rowAuditor{err: boom}
	s := NewAuditSampler(1, (&sampleClock{t: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}).now)
	if written, err := s.Append(context.Background(), a, store.AuditEvent{}); !written || !errors.Is(err, boom) {
		t.Fatalf("(%v, %v): a write failure must be returned without changing written", written, err)
	}
}
