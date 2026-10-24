package ingest

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/garmin"
	"github.com/stefiix92/coachd/internal/store"
)

// backfillLimit is today − 120 days for the default test env.
var backfillLimit = testToday.AddDays(-120)

func mustBackfill(t *testing.T, e *env) {
	t.Helper()
	if err := e.svc.Backfill(context.Background()); err != nil {
		t.Fatalf("Backfill: %v", err)
	}
}

func syncStateOf(t *testing.T, e *env, dt string) store.SyncState {
	t.Helper()
	st, err := e.db.GetSyncState(context.Background(), dt)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestBackfillCoversRange(t *testing.T) {
	f := newFake()
	e := newEnv(t, f, nil)
	mustBackfill(t, e)

	rs := f.rangesOf(mSleep)
	if len(rs) == 0 {
		t.Fatal("no sleep requests")
	}
	if first := rs[0].To; first != testToday.AddDays(-3) {
		t.Fatalf("first sleep chunk ends %s, want today-3", first)
	}
	for i, r := range rs {
		if n := r.To.Sub(r.From) + 1; n > 28 || n < 1 {
			t.Fatalf("chunk %d %s..%s spans %d days, want 1..28", i, r.From, r.To, n)
		}
		if i > 0 && r.To != rs[i-1].From.AddDays(-1) {
			t.Fatalf("chunk %d ends %s, want contiguous with previous start %s", i, r.To, rs[i-1].From)
		}
	}
	if last := rs[len(rs)-1].From; last != backfillLimit {
		t.Fatalf("oldest sleep chunk starts %s, want %s", last, backfillLimit)
	}
	for m, max := range map[string]int{mSearch: 90, mHRV: 28, mDaily: 28, mWeight: 90} {
		rs := f.rangesOf(m)
		if len(rs) == 0 {
			t.Fatalf("%s: no requests", m)
		}
		for _, r := range rs {
			if n := r.To.Sub(r.From) + 1; n > max {
				t.Fatalf("%s: %s..%s spans %d days, cap %d", m, r.From, r.To, n, max)
			}
		}
		if got := rs[len(rs)-1].From; got != backfillLimit {
			t.Fatalf("%s: oldest chunk starts %s, want %s", m, got, backfillLimit)
		}
	}
}

func TestBackfillResumesFromCursor(t *testing.T) {
	f := newFake()
	e := newEnv(t, f, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Let two sleep chunks through; cancel as the third begins.
	f.setHook(func(m string) {
		if m == mSleep && f.count(mSleep) == 3 {
			cancel()
		}
	})
	if err := e.svc.Backfill(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Backfill err = %v, want context.Canceled", err)
	}
	st := syncStateOf(t, e, TypeSleep)
	rs := f.rangesOf(mSleep)
	if st.Cursor != rs[1].From.String() || st.BackfillDone {
		t.Fatalf("sleep state = %+v, want cursor %s, not done", st, rs[1].From)
	}

	f.setHook(nil)
	f.resetCalls()
	mustBackfill(t, e)
	if n := f.count(mSearch); n != 0 {
		t.Fatalf("finished type refetched: %d activity calls", n)
	}
	resumed := f.rangesOf(mSleep)
	if want := rs[1].From.AddDays(-1); resumed[0].To != want {
		t.Fatalf("resumed sleep chunk ends %s, want %s", resumed[0].To, want)
	}
}

func TestBackfillMarksDoneAndRecomputes(t *testing.T) {
	f := newFake()
	day := testToday.AddDays(-30)
	f.sleep = append(f.sleep, item(t, sleepDTO(day.String(), 27000, 80)))
	f.hrv = append(f.hrv, item(t, hrvDTO(day.String(), 50)))
	f.daily = append(f.daily, item(t, dailyDTO(day.String(), 48)))
	f.weight = append(f.weight, item(t, weightDTO(day.String(), fp(79300))))
	f.activities = append(f.activities, item(t, activityDTO(2001, "running",
		day.String()+" 07:00:00", day.String()+" 05:00:00", 8000, false)))
	e := newEnv(t, f, nil)
	mustBackfill(t, e)

	for _, dt := range dataTypes {
		if !syncStateOf(t, e, dt).BackfillDone {
			t.Fatalf("%s not marked done", dt)
		}
	}
	h, err := e.db.ListDailyHealth(context.Background(), day, day)
	if err != nil || len(h) != 1 || h[0].WeightKg == nil || *h[0].WeightKg != 79.3 {
		t.Fatalf("daily health = %+v, %v", h, err)
	}
	m, err := e.db.ListDayMetrics(context.Background(), day, day)
	if err != nil || len(m) == 0 {
		t.Fatalf("no day metrics after backfill: %v %v", m, err)
	}

	f.resetCalls()
	mustBackfill(t, e)
	if n := f.totalCalls(); n != 0 {
		t.Fatalf("second Backfill made %d Garmin calls, want 0", n)
	}
}

func TestBackfillRespectsRateLimit(t *testing.T) {
	f := newFake()
	e := newEnv(t, f, nil)
	// Real time drives the clock so a short pause really elapses.
	begin := time.Now()
	e.svc.opts.Now = func() time.Time { return testNow.Add(time.Since(begin)) }
	f.setErrOnce(mSleep, &garmin.RateLimitedError{RetryAt: testNow.Add(40 * time.Millisecond)})

	var pausedCalls atomic.Int32
	f.setHook(func(string) {
		if _, paused := e.svc.pausedUntil(); paused {
			pausedCalls.Add(1)
		}
	})
	mustBackfill(t, e)

	if n := pausedCalls.Load(); n != 0 {
		t.Fatalf("%d Garmin calls made while paused", n)
	}
	rs := f.rangesOf(mSleep)
	if len(rs) < 2 || rs[0] != rs[1] {
		t.Fatalf("rate-limited chunk not retried identically: %v", rs)
	}
	if !syncStateOf(t, e, TypeSleep).BackfillDone {
		t.Fatal("sleep not done after retry")
	}
}

func TestBackfillActivitiesPaginateInsideChunk(t *testing.T) {
	f := newFake()
	day := testToday.AddDays(-10)
	for i := 0; i < 150; i++ {
		f.activities = append(f.activities, item(t, activityDTO(int64(5000+i), "running",
			day.String()+" 07:00:00", day.String()+" 05:00:00", 5000, false)))
	}
	e := newEnv(t, f, nil)
	mustBackfill(t, e)
	rs := f.rangesOf(mSearch)
	if rs[0] != rs[1] || rs[1] != rs[2] || rs[0].To.Sub(rs[0].From)+1 != 90 {
		t.Fatalf("first chunk not three pages (99+51+0) of one 90-day range: %v", rs)
	}
	got, _, err := e.db.ListActivities(context.Background(), store.ActivityQuery{Limit: 500})
	if err != nil || len(got) != 150 {
		t.Fatalf("stored %d activities, err %v; want 150", len(got), err)
	}
}

func TestBackfillAlreadyRunningReturnsNil(t *testing.T) {
	f := newFake()
	e := newEnv(t, f, nil)
	e.svc.backfillMu.Lock()
	defer e.svc.backfillMu.Unlock()
	if err := e.svc.Backfill(context.Background()); err != nil {
		t.Fatalf("Backfill = %v, want nil", err)
	}
	if n := f.totalCalls(); n != 0 {
		t.Fatalf("%d Garmin calls while another backfill runs", n)
	}
}

func TestBackfillUnauthorizedStopsAndResumes(t *testing.T) {
	f := newFake()
	e := newEnv(t, f, nil)
	// The hook runs after a call has read its error, so arming at call 2
	// fails call 3.
	f.setHook(func(m string) {
		if m == mSleep && f.count(mSleep) == 2 {
			f.setErrOnce(mSleep, garmin.ErrUnauthorized)
		}
	})
	err := e.svc.Backfill(context.Background())
	if !errors.Is(err, garmin.ErrUnauthorized) {
		t.Fatalf("Backfill err = %v, want ErrUnauthorized", err)
	}
	if n := f.count(mHRV); n != 0 {
		t.Fatalf("run continued to hrv after auth failure (%d calls)", n)
	}
	rs := f.rangesOf(mSleep)
	st := syncStateOf(t, e, TypeSleep)
	if st.Cursor != rs[1].From.String() || st.BackfillDone {
		t.Fatalf("sleep state = %+v, want cursor %s", st, rs[1].From)
	}
	f.setHook(nil)
	f.resetCalls()
	mustBackfill(t, e)
	if got := f.rangesOf(mSleep)[0].To; got != rs[1].From.AddDays(-1) {
		t.Fatalf("resume chunk ends %s, want %s", got, rs[1].From.AddDays(-1))
	}
}

func TestBackfillPreservesSyncStateFields(t *testing.T) {
	f := newFake()
	e := newEnv(t, f, nil)
	at := testNow
	if err := e.db.PutSyncState(context.Background(), store.SyncState{
		DataType: TypeWeight, LastAttemptAt: &at, LastSuccessAt: &at, LastOutcome: OutcomeOK,
	}); err != nil {
		t.Fatal(err)
	}
	mustBackfill(t, e)
	st := syncStateOf(t, e, TypeWeight)
	if st.LastSuccessAt == nil || st.LastOutcome != OutcomeOK || !st.BackfillDone {
		t.Fatalf("weight state = %+v", st)
	}
}

func TestRunLaunchesBackfill(t *testing.T) {
	f := newFake()
	e := newEnv(t, f, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.svc.Run(ctx); close(done) }()

	deadline := time.After(10 * time.Second)
	for {
		sts, err := e.db.ListSyncStates(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, st := range sts {
			if st.BackfillDone {
				n++
			}
		}
		if n == len(dataTypes) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("backfill did not complete under Run")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestBackfillFailingTypeDoesNotBlockOthers(t *testing.T) {
	f := newFake()
	e := newEnv(t, f, nil)
	// The first activities chunk succeeds; every later call returns 500.
	f.setHook(func(m string) {
		if m == mSearch && f.count(mSearch) == 1 {
			f.setErr(mSearch, &garmin.StatusError{Endpoint: "activities", Status: 500})
		}
	})
	err := e.svc.Backfill(context.Background())
	var se *garmin.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("Backfill err = %v, want the activities StatusError", err)
	}
	for _, m := range []string{mSleep, mHRV, mDaily, mWeight} {
		if f.count(m) == 0 {
			t.Errorf("%s never called after activities failed", m)
		}
	}
	for _, dt := range []string{TypeSleep, TypeHRV, TypeDaily, TypeWeight} {
		if !syncStateOf(t, e, dt).BackfillDone {
			t.Errorf("%s not done", dt)
		}
	}
	rs := f.rangesOf(mSearch)
	if st := syncStateOf(t, e, TypeActivities); st.BackfillDone || st.Cursor != rs[0].From.String() {
		t.Fatalf("activities state = %+v, want not done with cursor %s", st, rs[0].From)
	}
	if m, err := e.db.ListDayMetrics(context.Background(), backfillLimit, testToday); err != nil || len(m) != 0 {
		t.Fatalf("summaries recomputed with a type unfinished: %d rows, %v", len(m), err)
	}

	// Next run resumes activities from its cursor; the finished types make no calls.
	f.setHook(nil)
	f.setErr(mSearch, nil)
	f.resetCalls()
	mustBackfill(t, e)
	if n := f.count(mSleep) + f.count(mHRV) + f.count(mDaily) + f.count(mWeight); n != 0 {
		t.Errorf("finished types refetched: %d calls", n)
	}
	if got := f.rangesOf(mSearch)[0].To; got != rs[0].From.AddDays(-1) {
		t.Fatalf("resumed activities chunk ends %s, want %s", got, rs[0].From.AddDays(-1))
	}
	if !syncStateOf(t, e, TypeActivities).BackfillDone {
		t.Fatal("activities not done after resume")
	}
}

func TestBackfillRateLimitEndsRun(t *testing.T) {
	f := newFake()
	e := newEnv(t, f, nil)
	// A Retry-After in the past: every retry is immediately rate limited again.
	f.setErr(mSearch, &garmin.RateLimitedError{RetryAt: testNow.Add(-time.Minute)})
	if err := e.svc.Backfill(context.Background()); outcomeOf(err) != OutcomeRateLimited {
		t.Fatalf("Backfill err = %v, want rate limited", err)
	}
	if n := f.totalCalls() - f.count(mSearch); n != 0 {
		t.Fatalf("run continued past a rate limit: %d other calls", n)
	}
}
