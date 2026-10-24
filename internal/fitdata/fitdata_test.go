package fitdata

import (
	"bytes"
	"math"
	"runtime"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/fitdata/fittest"
)

var start = time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC)

func build(t *testing.T, laps, records int, hr uint8, speed float64) []byte {
	t.Helper()
	b, err := fittest.Build(fittest.Options{Start: start, Laps: laps, Records: records, HR: hr, SpeedMS: speed})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeLapsAndPoints(t *testing.T) {
	laps, points, err := Decode(bytes.NewReader(build(t, 2, 120, 140, 3.0)))
	if err != nil {
		t.Fatal(err)
	}
	if len(laps) != 2 || len(points) != 120 {
		t.Fatalf("laps=%d points=%d", len(laps), len(points))
	}
	if p := points[0].PaceSPerKm; p == nil || math.Abs(*p-333.3) > 0.1 {
		t.Fatalf("pace = %v", p)
	}
	if h := points[0].HRBpm; h == nil || *h != 140 {
		t.Fatalf("hr = %v", h)
	}
	if points[5].OffsetS != 5 {
		t.Fatalf("offset = %v", points[5].OffsetS)
	}
	if d := points[2].DistanceKm; d == nil || math.Abs(*d-0.006) > 1e-9 {
		t.Fatalf("distance = %v", d)
	}
	if a := points[0].AltitudeM; a == nil || math.Abs(*a-200) > 0.01 {
		t.Fatalf("altitude = %v", a)
	}
	l := laps[1]
	if l.Index != 1 || l.StartOffsetS != 60 || l.DurationS != 60 || math.Abs(l.DistanceKm-0.18) > 1e-9 {
		t.Fatalf("lap = %+v", l)
	}
	if l.AvgHRBpm == nil || *l.AvgHRBpm != 140 || l.MaxHRBpm == nil || *l.MaxHRBpm != 140 {
		t.Fatalf("lap hr = %+v", l)
	}
	if l.AvgPaceSPerKm == nil || math.Abs(*l.AvgPaceSPerKm-333.3) > 0.1 {
		t.Fatalf("lap pace = %v", l.AvgPaceSPerKm)
	}
	if l.ElevGainM == nil || *l.ElevGainM != 5 {
		t.Fatalf("lap elev = %v", l.ElevGainM)
	}
}

func TestDecodeInvalidHRIsNil(t *testing.T) {
	laps, points, err := Decode(bytes.NewReader(build(t, 1, 10, 0xFF, 3.0)))
	if err != nil {
		t.Fatal(err)
	}
	if points[0].HRBpm != nil || laps[0].AvgHRBpm != nil || laps[0].MaxHRBpm != nil {
		t.Fatal("invalid HR must decode to nil")
	}
}

func TestDecodeSlowSpeedNoPace(t *testing.T) {
	laps, points, err := Decode(bytes.NewReader(build(t, 1, 10, 120, 0.2)))
	if err != nil {
		t.Fatal(err)
	}
	if points[0].PaceSPerKm != nil || laps[0].AvgPaceSPerKm != nil {
		t.Fatal("speed below 0.3 m/s must give nil pace")
	}
}

func TestDecodeGarbageErrors(t *testing.T) {
	if _, _, err := Decode(bytes.NewReader([]byte("definitely not a fit file"))); err == nil {
		t.Fatal("want error")
	}
}

func TestDownsampleLongActivity(t *testing.T) {
	ps := make([]Point, 36000)
	for i := range ps {
		ps[i].OffsetS = float64(i)
	}
	out := Downsample(ps, 600)
	if len(out) != 600 {
		t.Fatalf("len = %d", len(out))
	}
	if out[0].OffsetS != 0 || out[599] != ps[35999] {
		t.Fatalf("ends wrong: %v %v", out[0], out[599])
	}
	for i := 1; i < len(out); i++ {
		if out[i].OffsetS <= out[i-1].OffsetS {
			t.Fatal("not ordered")
		}
	}
}

func TestDownsampleShortUnchanged(t *testing.T) {
	ps := []Point{{OffsetS: 1}, {OffsetS: 2}}
	if out := Downsample(ps, 600); len(out) != 2 {
		t.Fatal("short input changed")
	}
	if out := Downsample(ps, 0); len(out) != 2 {
		t.Fatal("max<=0 must return input")
	}
}

// BenchmarkDecode36k decodes a 10-hour, 1 Hz activity (36,000 records).
func BenchmarkDecode36k(b *testing.B) {
	data, err := fittest.Build(fittest.Options{Start: start, Laps: 36, Records: 36000, HR: 150, SpeedMS: 3})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		laps, points, err := Decode(bytes.NewReader(data))
		if err != nil || len(laps) != 36 || len(points) != 36000 {
			b.Fatalf("laps=%d points=%d err=%v", len(laps), len(points), err)
		}
	}
}

// TestDecodeLongActivityStreams pins streaming decode: a 36,000-record file
// must not keep every decoded message (the old decode-all path allocated
// ~32 MiB here; streaming allocates ~8 MiB, mostly the returned points).
func TestDecodeLongActivityStreams(t *testing.T) {
	data := build(t, 36, 36000, 150, 3.0)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	laps, points, err := Decode(bytes.NewReader(data))
	runtime.ReadMemStats(&after)
	if err != nil || len(laps) != 36 || len(points) != 36000 {
		t.Fatalf("laps=%d points=%d err=%v", len(laps), len(points), err)
	}
	const maxBytes = 12 << 20
	if got := after.TotalAlloc - before.TotalAlloc; got > maxBytes {
		t.Fatalf("Decode allocated %.1f MiB, want <= %d MiB", float64(got)/(1<<20), maxBytes>>20)
	}
}
