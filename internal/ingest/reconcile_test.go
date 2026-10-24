package ingest

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/garmin"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/telemetry"
)

// The incremental activities window of a first sync is [today−14, today].
var windowFrom = testToday.AddDays(-activityMinDays)

// logRec is one recorded log line; attrs include the logger's own (component).
type logRec struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

// logRecorder records every log line with its attributes.
type logRecorder struct {
	mu   *sync.Mutex
	recs *[]logRec
	base []slog.Attr
}

func newLogRecorder() *logRecorder { return &logRecorder{mu: &sync.Mutex{}, recs: &[]logRec{}} }

func (h *logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h *logRecorder) Handle(_ context.Context, r slog.Record) error {
	attrs := map[string]string{}
	for _, a := range h.base {
		attrs[a.Key] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.String(); return true })
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.recs = append(*h.recs, logRec{level: r.Level, msg: r.Message, attrs: attrs})
	return nil
}

func (h *logRecorder) WithAttrs(as []slog.Attr) slog.Handler {
	return &logRecorder{mu: h.mu, recs: h.recs, base: append(slices.Clone(h.base), as...)}
}

func (h *logRecorder) WithGroup(string) slog.Handler { return h }

// lines returns the recorded lines with message msg, and fails if any
// recorded line carries a key outside telemetry.AllowedKeys.
func (h *logRecorder) lines(t *testing.T, msg string) []logRec {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []logRec
	for _, r := range *h.recs {
		for k := range r.attrs {
			if !slices.Contains(telemetry.AllowedKeys, k) {
				t.Errorf("log %q key %q is not in telemetry.AllowedKeys", r.msg, k)
			}
		}
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

// wantAttrs fails unless r has every key/value in want.
func wantAttrs(t *testing.T, r logRec, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if r.attrs[k] != v {
			t.Errorf("log %q %s = %q, want %q (attrs %v)", r.msg, k, r.attrs[k], v, r.attrs)
		}
	}
}

// storeActivity stores activity id dated date directly (as an earlier sync
// would have), with a FIT file under DataDir and a detail row. It returns the
// FIT file's absolute path.
func storeActivity(t *testing.T, e *env, id int64, date civil.Date) string {
	t.Helper()
	ctx := context.Background()
	it := item(t, activityDTO(id, "running", date.String()+" 07:00:00", date.String()+" 05:00:00", 5000, false))
	a, err := MapActivity(it.V, e.svc.opts.TZ)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.UpsertActivity(ctx, a, it.Raw); err != nil {
		t.Fatal(err)
	}
	rel := path.Join("fit", strconv.Itoa(date.Year), strconv.FormatInt(id, 10)+".fit")
	abs := filepath.Join(e.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("FIT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetFIT(ctx, id, rel, store.FITStored); err != nil {
		t.Fatal(err)
	}
	if err := e.db.PutActivityDetail(ctx, id, store.ActivityDetail{}); err != nil {
		t.Fatal(err)
	}
	return abs
}

// rawDB opens a second handle on the env's database, for reading rows the
// store hides (soft-deleted activities) or does not expose (audit_log).
func rawDB(t *testing.T, e *env) *sql.DB {
	t.Helper()
	sdb, err := sql.Open("sqlite", "file:"+filepath.Join(e.dir, "coachd.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })
	return sdb
}

// rawActivity reads activity id's row whether or not it is soft-deleted.
type rawActivity struct {
	deleted          bool // deleted_at is set
	rawJSON, details int  // raw_json length, activity_detail rows
}

func readRawActivity(t *testing.T, e *env, id int64) rawActivity {
	t.Helper()
	sdb := rawDB(t, e)
	var r rawActivity
	var del sql.NullString
	if err := sdb.QueryRow(`SELECT deleted_at, length(raw_json) FROM activities WHERE id=?`, id).Scan(&del, &r.rawJSON); err != nil {
		t.Fatalf("activity %d row: %v (rows must never be removed)", id, err)
	}
	if err := sdb.QueryRow(`SELECT count(*) FROM activity_detail WHERE activity_id=?`, id).Scan(&r.details); err != nil {
		t.Fatal(err)
	}
	r.deleted = del.Valid
	return r
}

// auditEvents returns the audit_log rows with event ev, oldest first.
func auditEvents(t *testing.T, e *env, ev string) []store.AuditEvent {
	t.Helper()
	rows, err := rawDB(t, e).Query(`SELECT at, lane, actor, client, event, target, outcome FROM audit_log WHERE event=? ORDER BY id`, ev)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []store.AuditEvent
	for rows.Next() {
		var a store.AuditEvent
		var at string
		if err := rows.Scan(&at, &a.Lane, &a.Actor, &a.Client, &a.Event, &a.Target, &a.Outcome); err != nil {
			t.Fatal(err)
		}
		if a.At, err = time.Parse("2006-01-02T15:04:05.000000000Z", at); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// assertKept fails unless activity id is live with its detail and FIT file.
func assertKept(t *testing.T, e *env, id int64, fitFile string) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.db.GetActivity(ctx, id); err != nil {
		t.Errorf("activity %d: %v, want kept", id, err)
	}
	if _, err := e.db.GetActivityDetail(ctx, id); err != nil {
		t.Errorf("activity %d detail: %v, want kept", id, err)
	}
	if _, err := os.Stat(fitFile); err != nil {
		t.Errorf("activity %d FIT file: %v, want kept", id, err)
	}
	if r := readRawActivity(t, e, id); r.deleted {
		t.Errorf("activity %d: deleted_at set, want live", id)
	}
}

// assertDeleted fails unless activity id is soft-deleted: hidden from reads,
// with its row, raw JSON, detail row and FIT file all kept.
func assertDeleted(t *testing.T, e *env, id int64, fitFile string) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.db.GetActivity(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("activity %d: err = %v, want hidden (soft-deleted)", id, err)
	}
	acts, _, err := e.db.ListActivities(ctx, store.ActivityQuery{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range acts {
		if a.ID == id {
			t.Errorf("activity %d still listed", id)
		}
	}
	r := readRawActivity(t, e, id)
	if !r.deleted || r.rawJSON == 0 || r.details != 1 {
		t.Errorf("activity %d row = %+v, want deleted_at set, raw JSON and detail row kept", id, r)
	}
	if _, err := os.Stat(fitFile); err != nil {
		t.Errorf("activity %d FIT file: %v, want kept", id, err)
	}
}

func TestPagingStopsWhenPageHasNoNewIDs(t *testing.T) {
	f := newFake()
	var page []garmin.Item[garmin.ActivityDTO]
	for i := range fakePageCap {
		page = append(page, item(t, activityDTO(int64(7000+i), "running", "2026-10-20 07:00:00", "2026-10-20 05:00:00", 1000, false)))
	}
	f.search = func(start, limit int) []garmin.Item[garmin.ActivityDTO] { return page } // ignores start
	e := newEnv(t, f, nil)
	got, err := e.svc.fetchActivities(context.Background(), windowFrom, testToday)
	if err != nil {
		t.Fatal(err)
	}
	if n := f.count(mSearch); n != 2 {
		t.Fatalf("search calls = %d, want 2 (the repeated page adds no new ID)", n)
	}
	if !got.complete || got.stored != fakePageCap {
		t.Fatalf("complete = %v stored = %d, want true and %d", got.complete, got.stored, fakePageCap)
	}
	if got := rowCounts(t, e).activities; got != fakePageCap {
		t.Fatalf("activities = %d, want %d", got, fakePageCap)
	}
}

func TestReconcileDeletesConfirmedMissing(t *testing.T) {
	f := seededFake(t) // lists idRun, idManual, idGone
	logs := newLogRecorder()
	e := newEnv(t, f, logs)
	sr := traced(t, e)
	gone := storeActivity(t, e, 777, testToday.AddDays(-8))    // in the window, not listed
	edge := storeActivity(t, e, 780, windowFrom)               // first day of the window, not listed
	old := storeActivity(t, e, 778, testToday.AddDays(-30))    // outside the window
	before := storeActivity(t, e, 779, windowFrom.AddDays(-1)) // the day before the window
	for _, id := range []int64{777, 778, 779, 780} {
		f.deleted[id] = true
	}

	res := mustSync(t, e)
	if res.Outcomes[TypeActivities] != OutcomeOK {
		t.Fatalf("activities outcome = %q", res.Outcomes[TypeActivities])
	}
	assertDeleted(t, e, 777, gone)
	assertDeleted(t, e, 780, edge)
	assertKept(t, e, 778, old)
	assertKept(t, e, 779, before)
	// The first confirmed 404 (780) is verified once against the newest listed
	// activity (idRun, the canary) before anything is deleted.
	if p := f.probed(); !slices.Equal(p, []int64{780, idRun, 777}) {
		t.Fatalf("probed %v, want missing in-window ids oldest first with one canary: [780 %d 777]", p, idRun)
	}
	if n, c := f.count(mExists), f.count(mCanary); n != 2 || c != 1 {
		t.Fatalf("probes = %d, canary probes = %d; want 2 and 1", n, c)
	}
	dels := logs.lines(t, "reconcile deleted activity")
	if len(dels) != 2 {
		t.Fatalf("%d deletion log lines, want 2", len(dels))
	}
	for i, want := range []struct{ id, date string }{{"780", windowFrom.String()}, {"777", testToday.AddDays(-8).String()}} {
		if dels[i].level != slog.LevelInfo {
			t.Errorf("deletion log level %v, want info", dels[i].level)
		}
		wantAttrs(t, dels[i], map[string]string{"component": "ingest", "data_type": TypeActivities,
			"activity_id": want.id, "calendar_date": want.date, "outcome": "deleted"})
	}
	for _, id := range []int64{idRun, idManual, idGone} {
		if _, err := e.db.GetActivity(context.Background(), id); err != nil {
			t.Errorf("listed activity %d: %v", id, err)
		}
	}
	audits := auditEvents(t, e, "activity_deleted")
	if len(audits) != 2 {
		t.Fatalf("%d activity_deleted audit rows, want 2", len(audits))
	}
	for i, target := range []string{"780", "777"} {
		want := store.AuditEvent{At: testNow, Lane: "ingest", Actor: "ingest", Event: "activity_deleted", Target: target, Outcome: "ok"}
		if got := audits[i]; !got.At.Equal(want.At) || got.Lane != want.Lane || got.Actor != want.Actor ||
			got.Client != "" || got.Event != want.Event || got.Target != want.Target || got.Outcome != want.Outcome {
			t.Errorf("audit row %d = %+v, want %+v", i, got, want)
		}
	}

	rec := spansNamed(sr, "ingest.reconcile")
	if len(rec) != 1 {
		t.Fatalf("%d ingest.reconcile spans, want 1", len(rec))
	}
	var parent string
	for _, s := range spansNamed(sr, "ingest.sync.type") {
		if dt, _ := attr(s, "data_type"); dt == TypeActivities && s.SpanContext().SpanID() == rec[0].Parent().SpanID() {
			parent = dt
		}
	}
	if parent != TypeActivities {
		t.Error("ingest.reconcile is not a child of the activities ingest.sync.type span")
	}
	for k, want := range map[string]string{"data_type": TypeActivities, "count": "2", "outcome": OutcomeOK} {
		if got, _ := attr(rec[0], k); got != want {
			t.Errorf("reconcile span %s = %q, want %q", k, got, want)
		}
	}
	assertSpanKeys(t, sr)
}

func TestReconcileKeepsActivityThatStillExists(t *testing.T) {
	f := seededFake(t)
	e := newEnv(t, f, nil)
	// Stored in the window but no longer listed, e.g. its date moved; Garmin still has it.
	moved := storeActivity(t, e, 777, testToday.AddDays(-5))

	mustSync(t, e)
	assertKept(t, e, 777, moved)
	if p := f.probed(); !slices.Equal(p, []int64{777}) {
		t.Fatalf("probed %v, want [777]", p)
	}
}

func TestReconcileEmptyListDeletesNothingWithoutConfirm(t *testing.T) {
	for name, perr := range map[string]error{
		"5xx":          &garmin.StatusError{Endpoint: "activity", Status: 500},
		"transport":    &garmin.StatusError{Endpoint: "activity"},
		"unauthorized": garmin.ErrUnauthorized,
		"rate limited": &garmin.RateLimitedError{RetryAt: testNow.Add(time.Hour)},
		"cancelled":    context.Canceled,
		"other":        errors.New("boom"),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake() // Garmin lists no activities at all
			rec := newKeyRecorder()
			e := newEnv(t, f, rec)
			a := storeActivity(t, e, 777, testToday.AddDays(-3))
			b := storeActivity(t, e, 778, testToday.AddDays(-2))
			f.deleted[777], f.deleted[778] = true, true // the fake answers (false, err): err must win
			f.setErr(mExists, perr)

			res := mustSync(t, e)
			assertKept(t, e, 777, a)
			assertKept(t, e, 778, b)
			if n := f.count(mExists); n != 1 {
				t.Fatalf("probes = %d, want 1 (an error ends reconcile)", n)
			}
			if res.Outcomes[TypeActivities] != OutcomeOK {
				t.Fatalf("activities outcome = %q, want ok (a probe error does not change it)", res.Outcomes[TypeActivities])
			}
			var rl *garmin.RateLimitedError
			if errors.As(perr, &rl) {
				if _, paused := e.svc.pausedUntil(); !paused || res.Outcomes[TypeSleep] != OutcomeRateLimited {
					t.Fatalf("429 not noted: paused = %v, sleep outcome = %q", paused, res.Outcomes[TypeSleep])
				}
			}
			rec.mu.Lock()
			defer rec.mu.Unlock()
			for k := range rec.keys {
				if !slices.Contains(telemetry.AllowedKeys, k) {
					t.Errorf("log key %q is not in telemetry.AllowedKeys", k)
				}
			}
		})
	}
}

func TestReconcileCapsProbesAt10(t *testing.T) {
	f := seededFake(t)
	e := newEnv(t, f, nil)
	files := map[int64]string{}
	for i := range int64(15) {
		id := 800 + i
		files[id] = storeActivity(t, e, id, windowFrom.AddDays(int(i%10)))
		f.deleted[id] = true
	}

	mustSync(t, e)
	if n := f.count(mExists); n != maxReconcileProbes || maxReconcileProbes != 10 {
		t.Fatalf("probes = %d (cap %d), want 10", n, maxReconcileProbes)
	}
	if c := f.count(mCanary); c != 1 {
		t.Fatalf("canary probes = %d, want 1 (outside the cap, once per run)", c)
	}
	deleted := 0
	for id, file := range files {
		if _, err := e.db.GetActivity(context.Background(), id); errors.Is(err, store.ErrNotFound) {
			deleted++
			assertDeleted(t, e, id, file)
		}
	}
	if deleted != 10 {
		t.Fatalf("deleted %d, want 10 (one per confirmed probe)", deleted)
	}
}

func TestReconcileSkippedWhenIncomplete(t *testing.T) {
	f := seededFake(t)
	// Every page brings a new ID, so paging only ends at the page guard.
	f.search = func(start, limit int) []garmin.Item[garmin.ActivityDTO] {
		id := int64(90000 + start)
		return []garmin.Item[garmin.ActivityDTO]{item(t, activityDTO(id, "running", "2026-10-22 07:00:00", "2026-10-22 05:00:00", 1000, false))}
	}
	e := newEnv(t, f, nil)
	gone := storeActivity(t, e, 777, testToday.AddDays(-3))
	f.deleted[777] = true

	res := mustSync(t, e)
	if n := f.count(mSearch); n != maxActivityPages {
		t.Fatalf("search calls = %d, want the %d-page guard", n, maxActivityPages)
	}
	if res.Outcomes[TypeActivities] != OutcomeError {
		t.Fatalf("activities outcome = %q, want error", res.Outcomes[TypeActivities])
	}
	if n := f.count(mExists); n != 0 {
		t.Fatalf("probes = %d, want 0 for an incomplete fetch", n)
	}
	assertKept(t, e, 777, gone)
}

func TestReconcileUnverifiedProbeDeletesNothing(t *testing.T) {
	for name, setup := range map[string]func(f *fakeGarmin){
		// Every ID answers 404, the canary included: e.g. the endpoint moved.
		"canary 404":          func(f *fakeGarmin) { f.deleted[idRun] = true },
		"canary 5xx":          func(f *fakeGarmin) { f.setErr(mCanary, &garmin.StatusError{Endpoint: "activity", Status: 500}) },
		"canary transport":    func(f *fakeGarmin) { f.setErr(mCanary, &garmin.StatusError{Endpoint: "activity"}) },
		"canary unauthorized": func(f *fakeGarmin) { f.setErr(mCanary, garmin.ErrUnauthorized) },
		"canary 429": func(f *fakeGarmin) {
			f.setErr(mCanary, &garmin.RateLimitedError{RetryAt: testNow.Add(time.Hour)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := seededFake(t)
			logs := newLogRecorder()
			e := newEnv(t, f, logs)
			sr := traced(t, e)
			a := storeActivity(t, e, 777, testToday.AddDays(-3))
			b := storeActivity(t, e, 778, testToday.AddDays(-2))
			f.deleted[777], f.deleted[778] = true, true
			setup(f)

			res := mustSync(t, e)
			assertKept(t, e, 777, a)
			assertKept(t, e, 778, b)
			if p := f.probed(); !slices.Equal(p, []int64{777, idRun}) {
				t.Fatalf("probed %v, want [777 %d]: the canary fails, so the run ends", p, idRun)
			}
			if res.Outcomes[TypeActivities] != OutcomeOK {
				t.Fatalf("activities outcome = %q, want ok", res.Outcomes[TypeActivities])
			}
			fin := logs.lines(t, "reconcile finished")
			if len(fin) != 1 || fin[0].level != slog.LevelWarn {
				t.Fatalf("reconcile finished lines = %+v, want one warning", fin)
			}
			wantAttrs(t, fin[0], map[string]string{"component": "ingest", "data_type": TypeActivities,
				"error_class": "probe_unreliable", "outcome": OutcomeError, "count": "0"})
			if n := len(logs.lines(t, "reconcile deleted activity")); n != 0 {
				t.Fatalf("%d deletion lines, want 0", n)
			}
			rs := spansNamed(sr, "ingest.reconcile")
			if len(rs) != 1 {
				t.Fatalf("%d reconcile spans", len(rs))
			}
			if o, _ := attr(rs[0], "outcome"); o != OutcomeError {
				t.Fatalf("reconcile span outcome = %q, want error", o)
			}
			if name == "canary 429" {
				if _, paused := e.svc.pausedUntil(); !paused {
					t.Fatal("canary 429 did not note the pause")
				}
			}
			assertSpanKeys(t, sr)
		})
	}
}

func TestReconcileNoListedActivitySkipsDeletes(t *testing.T) {
	f := newFake() // Garmin lists nothing, so no listed activity can verify the probe
	logs := newLogRecorder()
	e := newEnv(t, f, logs)
	a := storeActivity(t, e, 777, testToday.AddDays(-3))
	b := storeActivity(t, e, 778, testToday.AddDays(-2))
	f.deleted[777], f.deleted[778] = true, true

	mustSync(t, e)
	assertKept(t, e, 777, a)
	assertKept(t, e, 778, b)
	if p := f.probed(); !slices.Equal(p, []int64{777}) {
		t.Fatalf("probed %v, want [777]: no canary exists, so the first 404 ends the run", p)
	}
	if c := f.count(mCanary); c != 0 {
		t.Fatalf("canary probes = %d, want 0", c)
	}
	fin := logs.lines(t, "reconcile finished")
	if len(fin) != 1 || fin[0].level != slog.LevelInfo {
		t.Fatalf("reconcile finished lines = %+v, want one info", fin)
	}
	wantAttrs(t, fin[0], map[string]string{"data_type": TypeActivities, "outcome": "skipped",
		"reason": "no_listed_activity", "count": "0"})
}

func TestReconcileWindowClampedToSummaryRange(t *testing.T) {
	f := seededFake(t)
	e := newEnv(t, f, nil)
	last := testNow.Add(-200 * 24 * time.Hour) // a long outage: the fetch window reaches back ~200 days
	if err := e.db.PutSyncState(context.Background(), store.SyncState{DataType: TypeActivities, LastSuccessAt: &last}); err != nil {
		t.Fatal(err)
	}
	outside := storeActivity(t, e, 777, testToday.AddDays(-130))
	beyond := storeActivity(t, e, 778, testToday.AddDays(-summaryDays))
	floor := storeActivity(t, e, 779, testToday.AddDays(-(summaryDays - 1)))
	inside := storeActivity(t, e, 780, testToday.AddDays(-100))
	for _, id := range []int64{777, 778, 779, 780} {
		f.deleted[id] = true
	}

	mustSync(t, e)
	if rs := f.rangesOf(mSearch); len(rs) == 0 || !rs[0].From.Before(testToday.AddDays(-130)) {
		t.Fatalf("search ranges %v, want a window reaching before today-130", rs)
	}
	assertKept(t, e, 777, outside)
	assertKept(t, e, 778, beyond)
	assertDeleted(t, e, 779, floor)
	assertDeleted(t, e, 780, inside)
	if p := f.probed(); !slices.Equal(p, []int64{779, idRun, 780}) {
		t.Fatalf("probed %v, want [779 %d 780]: nothing before today-119", p, idRun)
	}
}

func TestReconcileRemembersExistingActivity(t *testing.T) {
	f := seededFake(t)
	e := newEnv(t, f, nil)
	var kept []int64
	for i := range 10 { // unlisted but still in Garmin; they fill the probe cap
		id := int64(900 + i)
		storeActivity(t, e, id, windowFrom.AddDays(1+i))
		kept = append(kept, id)
	}
	gone := storeActivity(t, e, 950, testToday.AddDays(-1)) // newest, deleted in Garmin
	f.deleted[950] = true

	mustSync(t, e)
	if n := f.count(mExists); n != 10 {
		t.Fatalf("first sync probes = %d, want 10 (the cap)", n)
	}
	if _, err := e.db.GetActivity(context.Background(), 950); err != nil {
		t.Fatalf("950 deleted before it was probed: %v", err)
	}

	e.clock.Set(testNow.Add(time.Hour))
	mustSync(t, e)
	if n := f.count(mExists); n != 11 {
		t.Fatalf("probes after second sync = %d, want 11: the 10 known to exist are skipped and free their cap slots", n)
	}
	assertDeleted(t, e, 950, gone)

	e.clock.Set(testNow.Add(25 * time.Hour))
	mustSync(t, e)
	if n := f.count(mExists); n != 21 {
		t.Fatalf("probes after 25 h = %d, want 21: the memo expires after 24 h", n)
	}
	for _, id := range kept {
		if _, err := e.db.GetActivity(context.Background(), id); err != nil {
			t.Errorf("activity %d: %v, want kept", id, err)
		}
	}
}

func TestReconcileDeletionUpdatesSummaries(t *testing.T) {
	f := seededFake(t) // idRun: a 10 km run yesterday
	f.activities = append(f.activities, item(t, activityDTO(777, "running", "2026-10-21 07:00:00", "2026-10-21 05:00:00", 20000, false)))
	e := newEnv(t, f, nil)
	longest7d := func() float64 {
		t.Helper()
		ms, err := e.db.ListDayMetrics(context.Background(), testToday, testToday)
		if err != nil || len(ms) != 1 {
			t.Fatalf("day metrics for today: %v %v", ms, err)
		}
		return ms[0].LongestRun7dM
	}
	mustSync(t, e)
	if got := longest7d(); got != 20000 {
		t.Fatalf("longest run 7d = %v, want 20000", got)
	}

	f.mu.Lock()
	f.activities = f.activities[:len(f.activities)-1] // the runner deleted it in Garmin
	f.deleted[777] = true
	f.mu.Unlock()
	e.clock.Set(testNow.Add(time.Hour))
	mustSync(t, e)
	if _, err := e.db.GetActivity(context.Background(), 777); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("777 after reconcile: %v, want deleted", err)
	}
	if got := longest7d(); got != 10000 {
		t.Fatalf("longest run 7d after the deletion = %v, want 10000", got)
	}
}

// A soft-deleted activity is not probed again, and Garmin listing it again
// (e.g. restored) brings it back with its FIT and detail.
func TestReconcileSoftDeleteRestoredWhenListedAgain(t *testing.T) {
	f := seededFake(t)
	e := newEnv(t, f, nil)
	ctx := context.Background()
	date := testToday.AddDays(-5)
	fitFile := storeActivity(t, e, 777, date)
	f.deleted[777] = true

	mustSync(t, e)
	assertDeleted(t, e, 777, fitFile)
	probes := len(f.probed())

	e.clock.Set(testNow.Add(time.Hour))
	mustSync(t, e)
	if p := f.probed(); len(p) != probes {
		t.Fatalf("probed %v after a second sync, want no new probe of the soft-deleted 777", p[probes:])
	}
	if n := len(auditEvents(t, e, "activity_deleted")); n != 1 {
		t.Fatalf("%d activity_deleted audit rows, want 1", n)
	}

	f.mu.Lock()
	f.activities = append(f.activities, item(t, activityDTO(777, "running",
		date.String()+" 07:00:00", date.String()+" 05:00:00", 5000, false)))
	delete(f.deleted, 777)
	f.mu.Unlock()
	e.clock.Set(testNow.Add(2 * time.Hour))
	mustSync(t, e)
	assertKept(t, e, 777, fitFile)
	a, err := e.db.GetActivity(ctx, 777)
	if err != nil || a.FITStatus != store.FITStored || a.FITPath == "" {
		t.Fatalf("restored 777 = %+v, %v; want its stored FIT", a, err)
	}
}
