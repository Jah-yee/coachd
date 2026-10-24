// Package civil provides a calendar date without time or zone, so day
// arithmetic cannot be skewed by DST.
package civil

import (
	"fmt"
	"time"
)

const layout = "2006-01-02"

// Date is a calendar date.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// Parse parses "2006-01-02".
func Parse(s string) (Date, error) {
	t, err := time.Parse(layout, s)
	if err != nil {
		return Date{}, fmt.Errorf("parse date %q: %w", s, err)
	}
	return Date{t.Year(), t.Month(), t.Day()}, nil
}

// Of returns the calendar date of t in loc.
func Of(t time.Time, loc *time.Location) Date {
	y, m, d := t.In(loc).Date()
	return Date{y, m, d}
}

func (d Date) utc() time.Time {
	return time.Date(d.Year, d.Month, d.Day, 12, 0, 0, 0, time.UTC)
}

func (d Date) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, int(d.Month), d.Day) }

// AddDays returns the date n days later (n may be negative).
func (d Date) AddDays(n int) Date {
	t := time.Date(d.Year, d.Month, d.Day+n, 12, 0, 0, 0, time.UTC)
	return Date{t.Year(), t.Month(), t.Day()}
}

// Sub returns the number of days from o to d.
func (d Date) Sub(o Date) int {
	return int(d.utc().Sub(o.utc()) / (24 * time.Hour))
}

// Before reports whether d is earlier than o.
func (d Date) Before(o Date) bool { return d.Sub(o) < 0 }

// WeekStart returns the Monday on or before d.
func (d Date) WeekStart() Date {
	back := (int(d.utc().Weekday()) + 6) % 7
	return d.AddDays(-back)
}

func (d Date) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

func (d *Date) UnmarshalText(b []byte) error {
	p, err := Parse(string(b))
	if err != nil {
		return err
	}
	*d = p
	return nil
}
