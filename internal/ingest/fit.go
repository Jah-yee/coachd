package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/stefiix92/coachd/internal/fitdata"
	"github.com/stefiix92/coachd/internal/garmin"
	"github.com/stefiix92/coachd/internal/store"
)

const (
	fitBatch = 10
	// maxFITFailureStreak stops a drain after this many consecutive failed
	// downloads, so an outage (e.g. Garmin 5xx) does not use up every pending
	// activity's limited attempts.
	maxFITFailureStreak = 2
	// detailFillBatch bounds how many stored FITs without a detail row one
	// drain decodes, so a large pre-existing archive fills over several drains.
	detailFillBatch = 20
)

// signalFIT wakes the FIT worker without blocking.
func (s *Service) signalFIT() {
	select {
	case s.fitSignal <- struct{}{}:
	default:
	}
}

// fitWorker drains pending FIT downloads each time it is signalled. It never
// holds syncMu, so downloads do not delay syncs.
func (s *Service) fitWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.fitSignal:
			s.drainFIT(ctx)
		}
	}
}

// drainFIT downloads FIT files for activities that need one, attempting each
// activity at most once per drain, in one "ingest.fit.drain" span. It stops
// when nothing new is left, when Garmin pauses (429), rejects the key or is
// unreachable, after maxFITFailureStreak consecutive failures, or when ctx ends.
// Then, however downloads ended, it fills missing detail from local files.
func (s *Service) drainFIT(ctx context.Context) {
	ctx, span := s.tracer.Start(ctx, "ingest.fit.drain")
	defer span.End()
	tried, err := s.drainFITBatches(ctx)
	s.fillMissingDetail(ctx)
	outcome := outcomeOf(err)
	if errors.Is(err, errFITFailureStreak) {
		outcome = "stopped"
	}
	span.SetAttributes(attribute.String("outcome", outcome), attribute.Int("count", tried))
	// A pause (429) or the failure-streak stop is expected back-pressure, not a span error.
	if err != nil && outcome != OutcomeRateLimited && outcome != "stopped" {
		failSpan(span, err)
	}
}

// storeDetail decodes a FIT file's laps, series and workout, downsamples the
// series and stores them. Failures are logged, returned and leave the FIT stored:
// fillMissingDetail retries it once per process.
func (s *Service) storeDetail(ctx context.Context, id int64, r io.Reader) error {
	a, err := fitdata.Decode(r)
	if err == nil {
		err = s.db.PutActivityDetail(ctx, id, store.NewActivityDetail(a))
	}
	if err != nil {
		s.log.WarnContext(ctx, "fit detail failed", "data_type", "fit", "activity_id", id, "error_class", errorClass(err))
	}
	return err
}

// fillMissingDetail computes detail for up to detailFillBatch stored FITs that
// have none (stored before detail existed, or a decode that failed). It reads
// local files only and makes no Garmin call, so it neither paces nor stops on
// a Garmin pause; only ctx stops it. An activity whose fill fails (file
// missing or undecodable) is skipped for the life of the process.
func (s *Service) fillMissingDetail(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	s.fillMu.Lock()
	skip := len(s.fillFailed)
	s.fillMu.Unlock()
	// Ask for enough rows to get past those that already failed.
	batch, err := s.db.StoredFITsWithoutDetail(ctx, detailFillBatch+skip)
	if err != nil {
		if ctx.Err() == nil {
			s.log.ErrorContext(ctx, "fit detail query failed", "data_type", "fit", "error_class", errorClass(err))
		}
		return
	}
	done := 0
	for _, a := range batch {
		if done == detailFillBatch || ctx.Err() != nil {
			return
		}
		if s.fillFailedBefore(a.ID) {
			continue
		}
		done++
		if err := s.fillDetail(ctx, a); err != nil && ctx.Err() == nil {
			s.noteFillFailed(a.ID)
		}
	}
}

// fillDetail stores the detail decoded from a's stored FIT file.
func (s *Service) fillDetail(ctx context.Context, a store.Activity) error {
	f, err := os.Open(filepath.Join(s.opts.DataDir, filepath.FromSlash(a.FITPath)))
	if err != nil {
		s.log.WarnContext(ctx, "fit detail failed", "data_type", "fit", "activity_id", a.ID, "error_class", errorClass(err))
		return err
	}
	defer f.Close()
	return s.storeDetail(ctx, a.ID, f)
}

func (s *Service) fillFailedBefore(id int64) bool {
	s.fillMu.Lock()
	defer s.fillMu.Unlock()
	return s.fillFailed[id]
}

func (s *Service) noteFillFailed(id int64) {
	s.fillMu.Lock()
	defer s.fillMu.Unlock()
	s.fillFailed[id] = true
}

// drainFITBatches does drainFIT's work. It returns how many downloads it
// attempted and why it stopped early (nil when nothing was left).
func (s *Service) drainFITBatches(ctx context.Context) (int, error) {
	tried := map[int64]bool{}
	streak := 0
	for {
		if err := s.checkPause(); err != nil {
			return len(tried), err
		}
		// Ask for enough rows to get past those already tried in this drain.
		batch, err := s.db.ActivitiesNeedingFIT(ctx, fitBatch+len(tried))
		if err != nil {
			if ctx.Err() == nil {
				s.log.ErrorContext(ctx, "fit query failed", "data_type", "fit", "error_class", errorClass(err), "error", err)
			}
			return len(tried), err
		}
		progressed := false
		for _, a := range batch {
			if tried[a.ID] {
				continue
			}
			if len(tried) > 0 {
				if err := sleepCtx(ctx, s.opts.BackfillDelay); err != nil {
					return len(tried), err
				}
			}
			tried[a.ID] = true
			progressed = true
			status, stop := s.fetchFIT(ctx, a)
			if stop != nil {
				return len(tried), stop
			}
			switch status {
			case store.FITStored:
				streak = 0
			case store.FITFailed:
				if streak++; streak >= maxFITFailureStreak {
					s.log.WarnContext(ctx, "fit drain stopped after consecutive failures", "data_type", "fit", "count", streak)
					return len(tried), errFITFailureStreak
				}
			}
		}
		if !progressed {
			return len(tried), nil
		}
	}
}

// errFITFailureStreak ends a drain after maxFITFailureStreak failed downloads.
var errFITFailureStreak = errors.New("ingest: fit: consecutive download failures")

// fetchFIT downloads and stores one activity's FIT file and records its
// status, which it returns. A non-nil stop means draining should stop; status
// is then empty if the activity's state was left untouched. If the activity
// row vanished during the download, the file just written is removed and the
// status is empty (not a failure) unless the download itself failed.
func (s *Service) fetchFIT(ctx context.Context, a store.Activity) (status store.FITStatus, stop error) {
	if err := s.checkPause(); err != nil {
		return "", err
	}
	body, err := s.g.DownloadActivityFIT(ctx, a.ID)
	rel := ""
	var se *garmin.StatusError
	switch {
	case err == nil && len(body) > 0:
		if rel, err = s.writeFIT(a, body); err != nil {
			status = store.FITFailed
		} else {
			status = store.FITStored
		}
	case err == nil:
		status, err = store.FITFailed, errors.New("ingest: fit: empty body")
	case errors.Is(err, garmin.ErrNotFound):
		status = store.FITUnavailable // e.g. manual or deleted: never retried
	default:
		transport := errors.As(err, &se) && se.Status == 0
		if s.notePause(ctx, err) || errors.Is(err, garmin.ErrUnauthorized) || transport || ctx.Err() != nil {
			// 429, revoked key, transport failure or cancellation: not the
			// activity's fault, so leave its state untouched and stop draining.
			s.logFIT(ctx, a.ID, outcomeOf(err), err)
			return "", err
		}
		status = store.FITFailed // 5xx and other errors
	}
	serr := s.db.SetFIT(ctx, a.ID, rel, status)
	if errors.Is(serr, store.ErrNotFound) {
		s.removeOrphanFIT(ctx, a.ID, rel)
		s.log.WarnContext(ctx, "fit download finished", "data_type", "fit", "activity_id", a.ID,
			"outcome", "skipped", "reason", "activity_missing")
		if status != store.FITFailed {
			status = ""
		}
		return status, ctx.Err()
	}
	if serr != nil {
		s.logFIT(ctx, a.ID, OutcomeError, serr)
		return store.FITFailed, ctx.Err()
	}
	if status == store.FITStored {
		s.storeDetail(ctx, a.ID, bytes.NewReader(body))
	}
	s.logFIT(ctx, a.ID, string(status), err)
	if s.opts.Events != nil && (status == store.FITStored || status == store.FITUnavailable) &&
		s.now().Sub(a.StartUTC) <= recentActivity {
		s.opts.Events.ActivityStored(ctx, a)
	}
	return status, nil
}

// removeOrphanFIT removes a FIT file writeFIT just wrote (rel, relative to
// DataDir; empty when none) for an activity whose row no longer exists.
func (s *Service) removeOrphanFIT(ctx context.Context, id int64, rel string) {
	if rel == "" {
		return
	}
	if err := os.Remove(filepath.Join(s.opts.DataDir, filepath.FromSlash(rel))); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.WarnContext(ctx, "fit file not removed", "data_type", "fit", "activity_id", id, "error_class", errorClass(err))
	}
}

func (s *Service) logFIT(ctx context.Context, id int64, outcome string, err error) {
	attrs := []any{"data_type", "fit", "activity_id", id, "outcome", outcome}
	if err != nil {
		s.log.WarnContext(ctx, "fit download finished", append(attrs, "error_class", errorClass(err), "error", err)...)
		return
	}
	s.log.InfoContext(ctx, "fit download finished", attrs...)
}

// writeFIT atomically writes body to DataDir/fit/<yyyy>/<id>.fit (dir 0700,
// file 0600) and returns the path relative to DataDir.
func (s *Service) writeFIT(a store.Activity, body []byte) (string, error) {
	rel := path.Join("fit", fmt.Sprintf("%04d", a.Date.Year), strconv.FormatInt(a.ID, 10)+".fit")
	final := filepath.Join(s.opts.DataDir, filepath.FromSlash(rel))
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("ingest: fit dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".fit-*.tmp") // created 0600
	if err != nil {
		return "", fmt.Errorf("ingest: fit temp: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return "", fmt.Errorf("ingest: fit chmod: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		return "", fmt.Errorf("ingest: fit write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("ingest: fit sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("ingest: fit close: %w", err)
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", fmt.Errorf("ingest: fit rename: %w", err)
	}
	ok = true
	if d, err := os.Open(dir); err == nil { // best effort: persist the rename
		d.Sync()
		d.Close()
	}
	return rel, nil
}

// sleepCtx waits d (no wait when d <= 0) or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
