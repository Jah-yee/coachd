package ingest

import (
	"context"
	"math"
	"strconv"
	"syscall"
	"time"

	"github.com/stefiix92/coachd/internal/backup"
	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/telemetry"
)

// Gauges feeds the observable gauges from the store, the key expiry and the
// data volume. A failed read drops that point; it never panics.
type Gauges struct {
	db         *store.DB
	keyExpires func() civil.Date
	tz         *time.Location
	dataDir    string
	now        func() time.Time
}

var _ telemetry.GaugeSource = (*Gauges)(nil)

// NewGauges builds a GaugeSource; keyExpires is the last day the Garmin
// key in use is valid. A nil now means time.Now.
func NewGauges(db *store.DB, keyExpires func() civil.Date, tz *time.Location, dataDir string, now func() time.Time) *Gauges {
	if now == nil {
		now = time.Now
	}
	return &Gauges{db: db, keyExpires: keyExpires, tz: tz, dataDir: dataDir, now: now}
}

// SyncAges is seconds since each data type's last successful sync.
func (g *Gauges) SyncAges(ctx context.Context) map[string]float64 {
	return g.ages(ctx, func(s store.SyncState) *time.Time { return s.LastSuccessAt })
}

// DataFreshness is seconds since the freshest stored data of each type.
func (g *Gauges) DataFreshness(ctx context.Context) map[string]float64 {
	return g.ages(ctx, func(s store.SyncState) *time.Time { return s.FreshestDataAt })
}

func (g *Gauges) ages(ctx context.Context, pick func(store.SyncState) *time.Time) map[string]float64 {
	out := map[string]float64{}
	states, err := g.db.ListSyncStates(ctx)
	if err != nil {
		return out
	}
	now := g.now()
	for _, s := range states {
		if t := pick(s); t != nil {
			out[s.DataType] = now.Sub(*t).Seconds()
		}
	}
	return out
}

// KeyDaysLeft is the number of calendar days until the Garmin key expires.
func (g *Gauges) KeyDaysLeft() float64 {
	return float64(g.keyExpires().Sub(civil.Of(g.now(), g.tz)))
}

// DBSizeBytes is the database file plus WAL size, NaN on error.
func (g *Gauges) DBSizeBytes() float64 {
	n, err := g.db.SizeBytes()
	if err != nil {
		return math.NaN()
	}
	return float64(n)
}

// DiskUsedRatio is the used fraction of the filesystem holding DataDir, NaN on error.
func (g *Gauges) DiskUsedRatio() float64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(g.dataDir, &st); err != nil {
		return math.NaN()
	}
	total, avail := uint64(st.Blocks), uint64(st.Bavail)
	if total == 0 {
		return math.NaN()
	}
	return 1 - float64(avail)/float64(total)
}

// BackupAgeSeconds is seconds since the newest complete backup, NaN when there
// is none, the backups directory cannot be read, or the newest is more than
// 1 h in the future (a wrong clock named it; BackupStale's no-data case fires).
func (g *Gauges) BackupAgeSeconds() float64 {
	t, ok, err := backup.Newest(g.dataDir)
	if err != nil || !ok {
		return math.NaN()
	}
	age := g.now().Sub(t)
	if age < -time.Hour {
		return math.NaN()
	}
	return age.Seconds()
}

// DeviceLastSeenAges is seconds since each active device was last seen, keyed
// by decimal device ID. Revoked and never-seen devices are skipped. A failed
// read drops the points.
func (g *Gauges) DeviceLastSeenAges(ctx context.Context) map[string]float64 {
	out := map[string]float64{}
	devs, err := g.db.ListDevices(ctx)
	if err != nil {
		return out
	}
	now := g.now()
	for _, d := range devs {
		if d.RevokedAt == nil && d.LastSeenAt != nil {
			out[strconv.FormatInt(d.ID, 10)] = now.Sub(*d.LastSeenAt).Seconds()
		}
	}
	return out
}

// ProposalCounts is the number of proposals per status, every status present.
// A failed read drops the points.
func (g *Gauges) ProposalCounts(ctx context.Context) map[string]int {
	out := map[string]int{}
	counts, err := g.db.ProposalCounts(ctx)
	if err != nil {
		return out
	}
	for s, n := range counts {
		out[string(s)] = n
	}
	return out
}
