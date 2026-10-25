package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/muktihari/fit/profile/typedef"

	"github.com/stefiix92/coachd/internal/fitdata/fittest"
	"github.com/stefiix92/coachd/internal/garmin"
	"github.com/stefiix92/coachd/internal/store"
)

func TestDrainFITWritesFileAtomically(t *testing.T) {
	f := seededFake(t)
	body, err := fittest.Build(fittest.Options{Start: time.Date(2026, 10, 22, 5, 0, 0, 0, time.UTC), Laps: 2, Records: 60, HR: 150, SpeedMS: 3})
	if err != nil {
		t.Fatal(err)
	}
	f.fit[idRun] = body
	e := newEnv(t, f, nil)
	ctx := context.Background()
	mustSync(t, e)
	e.svc.drainFIT(ctx)

	a, err := e.db.GetActivity(ctx, idRun)
	if err != nil {
		t.Fatal(err)
	}
	if a.FITStatus != store.FITStored || a.FITPath != "fit/2026/1001.fit" {
		t.Fatalf("fit status/path = %q %q, want stored fit/2026/1001.fit (relative to DataDir)", a.FITStatus, a.FITPath)
	}
	abs := filepath.Join(e.dir, filepath.FromSlash(a.FITPath))
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("stored FIT bytes differ from download")
	}
	info, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %04o, want 0600", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(abs))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode = %04o, want 0700", perm)
	}
	entries, err := os.ReadDir(filepath.Dir(abs))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "1001.fit" {
		t.Errorf("dir entries = %v, want only 1001.fit (no temp files left)", entries)
	}
}

func TestDrainFITFailuresAreBounded(t *testing.T) {
	f := seededFake(t)
	f.fitErr[idRun] = &garmin.StatusError{Endpoint: "activity_file", Status: 500}
	e := newEnv(t, f, nil)
	ctx := context.Background()
	mustSync(t, e)
	for i := 0; i < 5; i++ {
		e.svc.drainFIT(ctx)
	}
	if n := f.fitCount(idRun); n != 3 {
		t.Fatalf("failing FIT requested %d times over 5 drains, want 3 (one per drain until 3 attempts)", n)
	}
	a, err := e.db.GetActivity(ctx, idRun)
	if err != nil {
		t.Fatal(err)
	}
	if a.FITStatus != store.FITFailed || a.FITAttempts != 3 || a.FITPath != "" {
		t.Fatalf("activity = status %q attempts %d path %q", a.FITStatus, a.FITAttempts, a.FITPath)
	}
}

func TestDrainFITEmptyBodyFails(t *testing.T) {
	f := seededFake(t)
	f.fit[idRun] = []byte{}
	e := newEnv(t, f, nil)
	ctx := context.Background()
	mustSync(t, e)
	e.svc.drainFIT(ctx)
	a, err := e.db.GetActivity(ctx, idRun)
	if err != nil {
		t.Fatal(err)
	}
	if a.FITStatus != store.FITFailed || a.FITPath != "" {
		t.Fatalf("empty body: status %q path %q, want failed", a.FITStatus, a.FITPath)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "fit", "2026", "1001.fit")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty body must not write a file: %v", err)
	}
}

func TestDrainFITStopsOnRateLimit(t *testing.T) {
	f := seededFake(t)
	f.fitErr[idRun] = &garmin.RateLimitedError{RetryAt: testNow.Add(time.Hour)}
	e := newEnv(t, f, nil)
	ctx := context.Background()
	mustSync(t, e)
	e.svc.drainFIT(ctx)
	if n := f.count(mFIT); n != 1 {
		t.Fatalf("FIT calls = %d, want 1 (drain stops at the 429)", n)
	}
	a, err := e.db.GetActivity(ctx, idRun)
	if err != nil {
		t.Fatal(err)
	}
	if a.FITStatus != store.FITPending || a.FITAttempts != 0 {
		t.Fatalf("rate-limited FIT = %q attempts %d, want pending 0", a.FITStatus, a.FITAttempts)
	}
	e.svc.drainFIT(ctx)
	if n := f.count(mFIT); n != 1 {
		t.Fatalf("drain while paused made %d more calls", n-1)
	}
	if r, err := e.svc.Sync(ctx); err != nil || r.Outcomes["activities"] != OutcomeRateLimited {
		t.Fatalf("sync after FIT 429 = %v %v, want paused", r.Outcomes, err)
	}
}

func TestDrainFITStopsOnAuthFailure(t *testing.T) {
	f := seededFake(t)
	f.fitErr[idRun] = garmin.ErrUnauthorized
	e := newEnv(t, f, nil)
	ctx := context.Background()
	mustSync(t, e)
	e.svc.drainFIT(ctx)
	if n := f.count(mFIT); n != 1 {
		t.Fatalf("FIT calls = %d, want 1 (no retry storm on auth failure)", n)
	}
	a, err := e.db.GetActivity(ctx, idRun)
	if err != nil {
		t.Fatal(err)
	}
	if a.FITStatus != store.FITPending || a.FITAttempts != 0 {
		t.Fatalf("auth-failed FIT = %q attempts %d, want pending 0 (activity not at fault)", a.FITStatus, a.FITAttempts)
	}
}

func TestDrainFITReachesActivitiesBeyondFirstBatch(t *testing.T) {
	f := newFake()
	// 24 activities on one day, listed by id desc: odd ids fail, even ids succeed,
	// so failures never run consecutively and failed rows stay in the query result.
	for i := 0; i < 24; i++ {
		id := int64(2000 + i)
		f.activities = append(f.activities, item(t, activityDTO(id, "running", "2026-10-22 07:00:00", "2026-10-22 05:00:00", 1000, false)))
		if id%2 == 1 {
			f.fitErr[id] = &garmin.StatusError{Endpoint: "activity_file", Status: 500}
		} else {
			f.fit[id] = []byte("FIT")
		}
	}
	e := newEnv(t, f, nil)
	ctx := context.Background()
	mustSync(t, e)
	e.svc.drainFIT(ctx)
	if n := f.count(mFIT); n != 24 {
		t.Fatalf("FIT calls = %d, want 24 (each activity once per drain)", n)
	}
	for i := 0; i < 24; i++ {
		id := int64(2000 + i)
		a, err := e.db.GetActivity(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		want := store.FITStored
		if id%2 == 1 {
			want = store.FITFailed
		}
		if a.FITStatus != want || f.fitCount(id) != 1 {
			t.Errorf("activity %d: fit %q calls %d, want %q 1", id, a.FITStatus, f.fitCount(id), want)
		}
	}
}

// pendingFake returns n activities on distinct days (newest first by id order),
// all with FIT error err.
func pendingFake(t *testing.T, n int, err error) (*fakeGarmin, []int64) {
	t.Helper()
	f := newFake()
	var ids []int64
	for i := 0; i < n; i++ {
		id := int64(4000 + i)
		day := fmt.Sprintf("2026-10-%02d", 22-i)
		f.activities = append(f.activities, item(t, activityDTO(id, "running", day+" 07:00:00", day+" 05:00:00", 1000, false)))
		f.fitErr[id] = err
		ids = append(ids, id)
	}
	return f, ids
}

func TestDrainFITStopsOnTransportError(t *testing.T) {
	f, ids := pendingFake(t, 5, &garmin.StatusError{Endpoint: "activity_file", Status: 0})
	e := newEnv(t, f, nil)
	ctx := context.Background()
	mustSync(t, e)
	e.svc.drainFIT(ctx)
	if n := f.count(mFIT); n != 1 {
		t.Fatalf("FIT calls = %d, want 1 (transport failure stops the drain)", n)
	}
	for _, id := range ids {
		a, err := e.db.GetActivity(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if a.FITStatus != store.FITPending || a.FITAttempts != 0 {
			t.Errorf("activity %d: fit %q attempts %d, want pending 0", id, a.FITStatus, a.FITAttempts)
		}
	}
}

func TestDrainFITStopsAfterTwoConsecutiveFailures(t *testing.T) {
	f, ids := pendingFake(t, 5, &garmin.StatusError{Endpoint: "activity_file", Status: 503})
	e := newEnv(t, f, nil)
	ctx := context.Background()
	mustSync(t, e)
	e.svc.drainFIT(ctx)
	if n := f.count(mFIT); n != 2 {
		t.Fatalf("FIT calls = %d, want 2", n)
	}
	for i, id := range ids {
		a, err := e.db.GetActivity(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		want, attempts := store.FITPending, 0
		if i < 2 {
			want, attempts = store.FITFailed, 1
		}
		if a.FITStatus != want || a.FITAttempts != attempts {
			t.Errorf("activity %d: fit %q attempts %d, want %q %d", id, a.FITStatus, a.FITAttempts, want, attempts)
		}
	}
}

func TestDrainFITSuccessResetsFailureStreak(t *testing.T) {
	boom := &garmin.StatusError{Endpoint: "activity_file", Status: 500}
	f, ids := pendingFake(t, 6, boom)
	// Newest first: fail, ok, 404, fail, fail, (not reached).
	delete(f.fitErr, ids[1])
	f.fit[ids[1]] = []byte("FIT")
	f.fitErr[ids[2]] = garmin.ErrNotFound
	e := newEnv(t, f, nil)
	ctx := context.Background()
	mustSync(t, e)
	e.svc.drainFIT(ctx)
	if n := f.count(mFIT); n != 5 {
		t.Fatalf("FIT calls = %d, want 5 (success resets the streak; 404 does not count)", n)
	}
	if f.fitCount(ids[5]) != 0 {
		t.Fatal("drain should stop before the sixth activity")
	}
}

func buildFIT(t *testing.T, records int) []byte {
	t.Helper()
	b, err := fittest.Build(fittest.Options{Start: time.Date(2026, 10, 22, 5, 0, 0, 0, time.UTC), Laps: 3, Records: records, HR: 150, SpeedMS: 3})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDrainStoresDetail(t *testing.T) {
	f := seededFake(t)
	f.fit[idRun] = buildFIT(t, 5000)
	e := newEnv(t, f, nil)
	ctx := context.Background()
	mustSync(t, e)
	e.svc.drainFIT(ctx)

	d, err := e.db.GetActivityDetail(ctx, idRun)
	if err != nil {
		t.Fatalf("detail row missing after FIT download: %v", err)
	}
	if len(d.Series) == 0 || len(d.Series) > 600 || len(d.Laps) != 3 {
		t.Fatalf("series=%d laps=%d, want 1..600 and 3", len(d.Series), len(d.Laps))
	}
}

func TestDrainStoresWorkout(t *testing.T) {
	wf, err := fittest.WorkoutFile("Easy", []fittest.Step{{Intensity: typedef.IntensityActive,
		Duration: typedef.WktStepDurationTime, DurationValue: 600000, Target: typedef.WktStepTargetHeartRate, Low: 230, High: 260}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := fittest.Build(fittest.Options{Start: time.Date(2026, 10, 22, 5, 0, 0, 0, time.UTC), Records: 600, HR: 150,
		SpeedMS: 3, LapRecords: []int{600}, LapSteps: []uint16{0}, Workout: wf})
	if err != nil {
		t.Fatal(err)
	}
	f := seededFake(t)
	f.fit[idRun] = body
	e := newEnv(t, f, nil)
	ctx := context.Background()
	mustSync(t, e)
	e.svc.drainFIT(ctx)

	d, err := e.db.GetActivityDetail(ctx, idRun)
	if err != nil {
		t.Fatal(err)
	}
	if d.Workout == nil || len(d.Workout.Executed) != 1 || d.Workout.Executed[0].TargetTime == nil ||
		d.Workout.Executed[0].TargetTime.InS != 600 {
		t.Fatalf("workout = %+v", d.Workout)
	}
	a, err := e.db.GetActivity(ctx, idRun)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := e.db.ActivityWorkouts(ctx, a.Date, a.Date)
	if err != nil || len(ws) != 1 || ws[0].ID != idRun || ws[0].Fingerprint != d.Workout.Fingerprint {
		t.Fatalf("activity workouts = %+v, %v", ws, err)
	}
}

func TestDrainFillsMissingDetail(t *testing.T) {
	f := seededFake(t)
	e := newEnv(t, f, nil)
	ctx := context.Background()
	mustSync(t, e)
	// A stored FIT from before detail existed: file on disk, no detail row.
	rel := "fit/2026/1001.fit"
	abs := filepath.Join(e.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, buildFIT(t, 120), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetFIT(ctx, idRun, rel, store.FITStored); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.GetActivityDetail(ctx, idRun); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("precondition: detail err = %v", err)
	}
	e.svc.drainFIT(ctx)
	d, err := e.db.GetActivityDetail(ctx, idRun)
	if err != nil || len(d.Series) != 120 || len(d.Laps) != 3 {
		t.Fatalf("detail not filled: err=%v series=%d laps=%d", err, len(d.Series), len(d.Laps))
	}
}

func TestDrainFillMissingDetailStopsOnCancel(t *testing.T) {
	f := seededFake(t)
	e := newEnv(t, f, nil)
	ctx := context.Background()
	mustSync(t, e)
	rel := "fit/2026/1001.fit"
	abs := filepath.Join(e.dir, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(abs), 0o700)
	os.WriteFile(abs, buildFIT(t, 50), 0o600)
	e.db.SetFIT(ctx, idRun, rel, store.FITStored)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	e.svc.fillMissingDetail(cctx)
	if _, err := e.db.GetActivityDetail(ctx, idRun); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("fill ran with cancelled ctx: %v", err)
	}
}

// storedWithoutDetail stores activity id dated date with a stored FIT file
// holding body (written under DataDir) and no detail row, as from before
// detail existed. A nil body leaves the file missing.
func storedWithoutDetail(t *testing.T, e *env, id int64, date string, body []byte) {
	t.Helper()
	ctx := context.Background()
	it := item(t, activityDTO(id, "running", date+" 07:00:00", date+" 05:00:00", 5000, false))
	a, err := MapActivity(it.V, e.svc.opts.TZ)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.UpsertActivity(ctx, a, it.Raw); err != nil {
		t.Fatal(err)
	}
	rel := path.Join("fit", date[:4], strconv.FormatInt(id, 10)+".fit")
	if body != nil {
		abs := filepath.Join(e.dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.db.SetFIT(ctx, id, rel, store.FITStored); err != nil {
		t.Fatal(err)
	}
}

func hasDetail(t *testing.T, e *env, id int64) bool {
	t.Helper()
	_, err := e.db.GetActivityDetail(context.Background(), id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

// Local detail fill runs after the downloads, so it never delays them.
func TestDrainFillsDetailAfterDownloads(t *testing.T) {
	f := newFake()
	e := newEnv(t, f, nil)
	storedWithoutDetail(t, e, 10, "2026-10-20", buildFIT(t, 60))
	it := item(t, activityDTO(11, "running", "2026-10-21 07:00:00", "2026-10-21 05:00:00", 5000, false))
	a, err := MapActivity(it.V, e.svc.opts.TZ)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.UpsertActivity(context.Background(), a, it.Raw); err != nil {
		t.Fatal(err)
	}
	f.fit[11] = buildFIT(t, 60)
	filledFirst := false
	f.setHook(func(m string) {
		if m == mFIT {
			filledFirst = hasDetail(t, e, 10)
		}
	})
	e.svc.drainFIT(context.Background())
	if n := f.count(mFIT); n != 1 {
		t.Fatalf("FIT downloads = %d, want 1", n)
	}
	if filledFirst {
		t.Fatal("detail fill ran before the FIT download")
	}
	if !hasDetail(t, e, 10) || !hasDetail(t, e, 11) {
		t.Fatalf("detail: filled %v, downloaded %v; want both", hasDetail(t, e, 10), hasDetail(t, e, 11))
	}
}

// Local decodes are not paced by BackfillDelay.
func TestDrainFillDoesNotSleepBetweenDecodes(t *testing.T) {
	e := newEnv(t, newFake(), nil)
	e.svc.opts.BackfillDelay = time.Hour
	for i := range 3 {
		storedWithoutDetail(t, e, int64(20+i), fmt.Sprintf("2026-10-%02d", 18+i), buildFIT(t, 60))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e.svc.drainFIT(ctx)
	for i := range 3 {
		if !hasDetail(t, e, int64(20+i)) {
			t.Errorf("activity %d: no detail, want every local decode done without pacing", 20+i)
		}
	}
}

// The fill makes no Garmin call, so a Garmin pause does not stop it.
func TestDrainFillIgnoresGarminPause(t *testing.T) {
	f := newFake()
	e := newEnv(t, f, nil)
	storedWithoutDetail(t, e, 30, "2026-10-20", buildFIT(t, 60))
	e.svc.notePause(context.Background(), &garmin.RateLimitedError{RetryAt: testNow.Add(time.Hour)})
	if _, paused := e.svc.pausedUntil(); !paused {
		t.Fatal("precondition: not paused")
	}
	e.svc.drainFIT(context.Background())
	if !hasDetail(t, e, 30) {
		t.Fatal("no detail: the fill stopped on a Garmin pause")
	}
	if n := f.totalCalls(); n != 0 {
		t.Fatalf("Garmin calls = %d, want 0 while paused", n)
	}
}

// A FIT whose fill failed (missing or undecodable) is not retried by later
// drains of the same process.
func TestDrainFillSkipsFailedFITs(t *testing.T) {
	logs := newLogRecorder()
	e := newEnv(t, newFake(), logs)
	storedWithoutDetail(t, e, 40, "2026-10-20", nil)                 // file missing
	storedWithoutDetail(t, e, 41, "2026-10-21", []byte("not a FIT")) // undecodable
	for range 3 {
		e.svc.drainFIT(context.Background())
	}
	fails := logs.lines(t, "fit detail failed")
	if len(fails) != 2 {
		t.Fatalf("%d 'fit detail failed' lines over 3 drains, want 2 (one per bad FIT)", len(fails))
	}
	got := map[string]bool{}
	for _, l := range fails {
		got[l.attrs["activity_id"]] = true
	}
	if !got["40"] || !got["41"] {
		t.Fatalf("failed ids %v, want 40 and 41", got)
	}
}

// Failed FITs do not use up the per-drain cap of later drains, and one drain
// decodes at most detailFillBatch.
func TestDrainFillFailedDoNotBlockOthers(t *testing.T) {
	logs := newLogRecorder()
	e := newEnv(t, newFake(), logs)
	storedWithoutDetail(t, e, 50, "2026-09-01", buildFIT(t, 60)) // oldest: listed last
	for i := range detailFillBatch {
		storedWithoutDetail(t, e, int64(100+i), "2026-10-20", []byte("bad"))
	}
	e.svc.drainFIT(context.Background())
	if n := len(logs.lines(t, "fit detail failed")); n != detailFillBatch || detailFillBatch != 20 {
		t.Fatalf("first drain: %d failures (cap %d), want 20", n, detailFillBatch)
	}
	if hasDetail(t, e, 50) {
		t.Fatal("first drain decoded more than the cap")
	}
	e.svc.drainFIT(context.Background())
	if !hasDetail(t, e, 50) {
		t.Fatal("second drain: the good FIT is still blocked by failed ones")
	}
	if n := len(logs.lines(t, "fit detail failed")); n != detailFillBatch {
		t.Fatalf("second drain retried failed FITs: %d failure lines, want %d", n, detailFillBatch)
	}
}

// An activity row that vanishes during its download leaves no orphan file and
// does not count toward the failure streak.
func TestDrainFITVanishedRowRemovesFile(t *testing.T) {
	f, ids := pendingFake(t, 3, nil) // newest first: ids[0], ids[1], ids[2]
	for _, id := range ids {
		delete(f.fitErr, id)
		f.fit[id] = []byte("FIT")
	}
	logs := newLogRecorder()
	e := newEnv(t, f, logs)
	ctx := context.Background()
	mustSync(t, e)
	sdb := rawDB(t, e)
	calls := 0
	f.setHook(func(m string) {
		if m != mFIT {
			return
		}
		if calls++; calls <= 2 { // the rows of the first two downloads vanish mid-download
			if _, err := sdb.Exec(`DELETE FROM activities WHERE id=?`, ids[calls-1]); err != nil {
				t.Error(err)
			}
		}
	})
	e.svc.drainFIT(ctx)
	if n := f.count(mFIT); n != 3 {
		t.Fatalf("FIT calls = %d, want 3 (vanished rows are not failures)", n)
	}
	for _, id := range ids[:2] {
		p := filepath.Join(e.dir, "fit", "2026", strconv.FormatInt(id, 10)+".fit")
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("orphan FIT %s: stat err = %v, want removed", p, err)
		}
	}
	a, err := e.db.GetActivity(ctx, ids[2])
	if err != nil || a.FITStatus != store.FITStored {
		t.Fatalf("third activity = %+v %v, want stored", a, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, filepath.FromSlash(a.FITPath))); err != nil {
		t.Fatalf("third FIT file: %v", err)
	}
	var skipped int
	for _, l := range logs.lines(t, "fit download finished") {
		if l.attrs["outcome"] == "skipped" {
			wantAttrs(t, l, map[string]string{"data_type": "fit", "reason": "activity_missing"})
			skipped++
		}
	}
	if skipped != 2 {
		t.Fatalf("%d skipped download lines, want 2", skipped)
	}
}

// A row soft-deleted (reconcile) while its FIT download is in flight keeps
// the FIT: soft-deleted activities retain their raw JSON and FIT file, so
// SetFIT records it and nothing is treated as an orphan.
func TestDrainFITSoftDeletedMidDownloadKeepsFile(t *testing.T) {
	f, ids := pendingFake(t, 1, nil)
	id := ids[0]
	delete(f.fitErr, id)
	f.fit[id] = []byte("FIT")
	logs := newLogRecorder()
	e := newEnv(t, f, logs)
	ctx := context.Background()
	mustSync(t, e)
	f.setHook(func(m string) {
		if m == mFIT {
			if err := e.db.MarkActivityDeleted(ctx, id, time.Date(2026, 10, 23, 6, 0, 0, 0, time.UTC)); err != nil {
				t.Error(err)
			}
		}
	})
	e.svc.drainFIT(ctx)
	if n := f.count(mFIT); n != 1 {
		t.Fatalf("FIT calls = %d, want 1", n)
	}
	p := filepath.Join(e.dir, "fit", "2026", strconv.FormatInt(id, 10)+".fit")
	if b, err := os.ReadFile(p); err != nil || string(b) != "FIT" {
		t.Fatalf("FIT file %s = %q, %v; want kept", p, b, err)
	}
	var status, rel string
	var deleted bool
	if err := rawDB(t, e).QueryRow(`SELECT fit_status, fit_path, deleted_at IS NOT NULL FROM activities WHERE id=?`, id).
		Scan(&status, &rel, &deleted); err != nil {
		t.Fatal(err)
	}
	if status != string(store.FITStored) || rel != "fit/2026/"+strconv.FormatInt(id, 10)+".fit" || !deleted {
		t.Fatalf("row fit_status %q fit_path %q deleted %v, want stored, the file, still deleted", status, rel, deleted)
	}
	for _, l := range logs.lines(t, "fit download finished") {
		if l.attrs["outcome"] != string(store.FITStored) {
			t.Fatalf("download logged %v, want stored (no orphan removal)", l.attrs)
		}
	}
	if n := len(logs.lines(t, "fit download finished")); n != 1 {
		t.Fatalf("%d download lines, want 1", n)
	}
	if l := logs.lines(t, "fit file not removed"); len(l) != 0 {
		t.Fatalf("orphan removal attempted: %v", l)
	}
}
