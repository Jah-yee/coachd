package civil

import (
	"testing"
	"time"
)

func TestAddDaysAcrossDST(t *testing.T) {
	d, err := Parse("2026-10-24")
	if err != nil {
		t.Fatal(err)
	}
	if got := d.AddDays(2).String(); got != "2026-10-26" {
		t.Fatalf("got %s", got)
	}
	if got := d.AddDays(-1).String(); got != "2026-10-23" {
		t.Fatalf("got %s", got)
	}
}

func TestWeekStart(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2026-10-25", "2026-10-19"}, // Sunday
		{"2026-10-26", "2026-10-26"}, // Monday
		{"2026-10-28", "2026-10-26"}, // Wednesday
	}
	for _, tc := range tests {
		d, err := Parse(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := d.WeekStart().String(); got != tc.want {
			t.Errorf("WeekStart(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestOfUsesLocation(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Bratislava")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	ts := time.Date(2026, 10, 23, 23, 30, 0, 0, time.UTC)
	if got := Of(ts, loc).String(); got != "2026-10-24" {
		t.Fatalf("got %s", got)
	}
}

func TestSub(t *testing.T) {
	a, _ := Parse("2026-10-26")
	b, _ := Parse("2026-10-24")
	if got := a.Sub(b); got != 2 {
		t.Fatalf("got %d", got)
	}
	if got := b.Sub(a); got != -2 {
		t.Fatalf("got %d", got)
	}
	// across a DST change and a year boundary
	c, _ := Parse("2027-01-01")
	if got := c.Sub(b); got != 69 {
		t.Fatalf("got %d", got)
	}
	if !b.Before(a) || a.Before(b) || a.Before(a) {
		t.Fatal("Before wrong")
	}
}

func TestParseErrorAndText(t *testing.T) {
	if _, err := Parse("2026-13-01"); err == nil {
		t.Fatal("want error")
	}
	d, _ := Parse("2026-10-24")
	b, err := d.MarshalText()
	if err != nil || string(b) != "2026-10-24" {
		t.Fatalf("marshal %q %v", b, err)
	}
	var e Date
	if err := e.UnmarshalText([]byte("2026-10-25")); err != nil || e.String() != "2026-10-25" {
		t.Fatalf("unmarshal %v %v", e, err)
	}
	if err := e.UnmarshalText([]byte("bad")); err == nil {
		t.Fatal("want error")
	}
}
