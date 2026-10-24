// Package fitdata decodes stored activity FIT files into laps and a point series.
package fitdata

import (
	"fmt"
	"io"
	"math"
	"time"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/kit/datetime"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/profile/untyped/fieldnum"
	"github.com/muktihari/fit/profile/untyped/mesgnum"
	"github.com/muktihari/fit/proto"
)

// MaxSeriesPoints caps the series served and stored for an activity.
const MaxSeriesPoints = 600

// minPaceSpeedMS is the speed above which a pace is meaningful.
const minPaceSpeedMS = 0.3

type Lap struct {
	Index                               int
	StartOffsetS, DurationS, DistanceKm float64
	AvgHRBpm, MaxHRBpm                  *int
	AvgPaceSPerKm, ElevGainM            *float64
}

type Point struct {
	OffsetS               float64
	DistanceKm            *float64
	HRBpm                 *int
	PaceSPerKm, AltitudeM *float64
}

// Decode reads an activity FIT file. Invalid sentinel values become nil.
// Messages are streamed: only the fields used here are kept as they arrive,
// so no decoded message outlives the decoder's callback.
func Decode(r io.Reader) ([]Lap, []Point, error) {
	// No FileId message means activity, as in filedef.NewActivity.
	c := collector{fileType: typedef.FileActivity, points: []Point{}}
	if _, err := decoder.New(r, decoder.WithMesgListener(&c), decoder.WithBroadcastOnly()).Decode(); err != nil {
		return nil, nil, fmt.Errorf("fitdata: decode: %w", err)
	}
	if c.fileType != typedef.FileActivity {
		return nil, nil, fmt.Errorf("fitdata: not an activity FIT file (type %v)", c.fileType)
	}

	first := c.first
	if first.IsZero() {
		first = c.sessionStart
	}
	laps := make([]Lap, 0, len(c.laps))
	for i, l := range c.laps {
		l.Lap.Index = i
		if !l.start.IsZero() && !first.IsZero() {
			l.Lap.StartOffsetS = l.start.Sub(first).Seconds()
		}
		laps = append(laps, l.Lap)
	}
	return laps, c.points, nil
}

// collector is a decoder.MesgListener keeping only the fields Decode needs.
// The decoder reuses each message's storage, so values are copied out.
type collector struct {
	fileType     typedef.File
	first        time.Time // first record timestamp
	sessionStart time.Time // first session start, used when no record has a timestamp
	points       []Point   // offsets from first: the first timestamped record sets it
	laps         []pendingLap
}

// pendingLap is a lap whose StartOffsetS waits for the activity start.
type pendingLap struct {
	Lap
	start time.Time
}

func (c *collector) OnMesg(m proto.Message) {
	switch m.Num {
	case mesgnum.FileId:
		c.fileType = typedef.File(value(&m, fieldnum.FileIdType).Uint8())
	case mesgnum.Record:
		c.record(&m)
	case mesgnum.Lap:
		c.lap(&m)
	case mesgnum.Session:
		if c.sessionStart.IsZero() {
			c.sessionStart = datetime.ToTime(value(&m, fieldnum.SessionStartTime).Uint32())
		}
	}
}

func (c *collector) record(m *proto.Message) {
	ts := datetime.ToTime(value(m, fieldnum.RecordTimestamp).Uint32())
	if ts.IsZero() {
		return
	}
	if c.first.IsZero() {
		c.first = ts
	}
	p := Point{OffsetS: ts.Sub(c.first).Seconds()}
	if v := scaled(value(m, fieldnum.RecordDistance).Uint32(), 100, 0); valid(v) {
		p.DistanceKm = ptr(v / 1000)
	}
	if hr := value(m, fieldnum.RecordHeartRate).Uint8(); hr != 0xFF {
		p.HRBpm = ptr(int(hr))
	}
	p.PaceSPerKm = pace(scaled(value(m, fieldnum.RecordEnhancedSpeed).Uint32(), 1000, 0))
	if v := scaled(value(m, fieldnum.RecordEnhancedAltitude).Uint32(), 5, 500); valid(v) {
		p.AltitudeM = ptr(v)
	}
	c.points = append(c.points, p)
}

func (c *collector) lap(m *proto.Message) {
	l := pendingLap{start: datetime.ToTime(value(m, fieldnum.LapStartTime).Uint32())}
	if v := scaled(value(m, fieldnum.LapTotalTimerTime).Uint32(), 1000, 0); valid(v) {
		l.DurationS = v
	} else if v := scaled(value(m, fieldnum.LapTotalElapsedTime).Uint32(), 1000, 0); valid(v) {
		l.DurationS = v
	}
	if v := scaled(value(m, fieldnum.LapTotalDistance).Uint32(), 100, 0); valid(v) {
		l.DistanceKm = v / 1000
	}
	if hr := value(m, fieldnum.LapAvgHeartRate).Uint8(); hr != 0xFF {
		l.AvgHRBpm = ptr(int(hr))
	}
	if hr := value(m, fieldnum.LapMaxHeartRate).Uint8(); hr != 0xFF {
		l.MaxHRBpm = ptr(int(hr))
	}
	l.AvgPaceSPerKm = pace(scaled(value(m, fieldnum.LapEnhancedAvgSpeed).Uint32(), 1000, 0))
	if a := value(m, fieldnum.LapTotalAscent).Uint16(); a != 0xFFFF {
		l.ElevGainM = ptr(float64(a))
	}
	c.laps = append(c.laps, l)
}

// value returns field num's value; the last occurrence wins, as in the
// mesgdef types. A missing field is the zero Value, whose typed accessors
// return the invalid sentinel.
func value(m *proto.Message, num byte) proto.Value {
	for i := len(m.Fields) - 1; i >= 0; i-- {
		if m.Fields[i].Num == num {
			return m.Fields[i].Value
		}
	}
	return proto.Value{}
}

// scaled applies a FIT scale and offset; the invalid sentinel becomes NaN.
func scaled(raw uint32, scale, offset float64) float64 {
	if raw == math.MaxUint32 {
		return math.NaN()
	}
	return float64(raw)/scale - offset
}

// Downsample returns at most max points at evenly spaced indices, always
// including the first and last. max <= 0 returns ps unchanged.
func Downsample(ps []Point, max int) []Point {
	n := len(ps)
	if max <= 0 || n <= max {
		return ps
	}
	if max == 1 {
		return ps[:1]
	}
	out := make([]Point, max)
	for i := range out {
		out[i] = ps[i*(n-1)/(max-1)]
	}
	return out
}

func valid(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func ptr[T any](v T) *T { return &v }

func pace(speedMS float64) *float64 {
	if !valid(speedMS) || speedMS <= minPaceSpeedMS {
		return nil
	}
	return ptr(1000 / speedMS)
}
