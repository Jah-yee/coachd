// Package fittest builds small synthetic activity FIT files for tests.
package fittest

import (
	"bytes"
	"fmt"
	"time"

	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
)

// Options describes the synthetic activity to encode.
type Options struct {
	Start   time.Time // session/lap/record start (UTC)
	Laps    int       // records split evenly across laps
	Records int       // 1 Hz records
	HR      uint8     // heart rate on every record (0xFF = invalid sentinel)
	SpeedMS float64   // enhanced speed on every record, m/s
}

// Build encodes a valid activity FIT file.
func Build(o Options) ([]byte, error) {
	if o.Laps < 1 || o.Records < o.Laps {
		return nil, fmt.Errorf("fittest: need Laps >= 1 and Records >= Laps")
	}
	f := filedef.NewActivity()
	f.FileId.SetType(typedef.FileActivity).SetTimeCreated(o.Start)

	for i := 0; i < o.Records; i++ {
		f.Records = append(f.Records, mesgdef.NewRecord(nil).
			SetTimestamp(o.Start.Add(time.Duration(i)*time.Second)).
			SetDistanceScaled(float64(i)*o.SpeedMS).
			SetHeartRate(o.HR).
			SetEnhancedSpeedScaled(o.SpeedMS).
			SetEnhancedAltitudeScaled(200))
	}

	per := o.Records / o.Laps
	for l := 0; l < o.Laps; l++ {
		n := per
		if l == o.Laps-1 {
			n = o.Records - per*(o.Laps-1)
		}
		start := o.Start.Add(time.Duration(l*per) * time.Second)
		f.Laps = append(f.Laps, mesgdef.NewLap(nil).
			SetTimestamp(start.Add(time.Duration(n)*time.Second)).
			SetStartTime(start).
			SetTotalTimerTimeScaled(float64(n)).
			SetTotalDistanceScaled(float64(n)*o.SpeedMS).
			SetAvgHeartRate(o.HR).
			SetMaxHeartRate(o.HR).
			SetEnhancedAvgSpeedScaled(o.SpeedMS).
			SetTotalAscent(5))
	}

	end := o.Start.Add(time.Duration(o.Records) * time.Second)
	f.Sessions = append(f.Sessions, mesgdef.NewSession(nil).
		SetTimestamp(end).SetStartTime(o.Start))
	f.Activity = mesgdef.NewActivity(nil).SetTimestamp(end).SetNumSessions(1)

	fit := f.ToFIT(nil)
	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&fit); err != nil {
		return nil, fmt.Errorf("fittest: encode: %w", err)
	}
	return buf.Bytes(), nil
}
