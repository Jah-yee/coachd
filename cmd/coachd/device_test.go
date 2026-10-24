package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/store"
)

func runDev(t *testing.T, db *store.DB, args ...string) (int, string, string) {
	t.Helper()
	return runDevAt(t, db, tNow, time.UTC, args...)
}

func runDevAt(t *testing.T, db *store.DB, now time.Time, tz *time.Location, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runDevice(context.Background(), args, db, func() time.Time { return now }, tz, &out, &errb)
	return code, out.String(), errb.String()
}

func devPairing(t *testing.T, db *store.DB, id, code string) {
	t.Helper()
	if err := db.CreatePairing(context.Background(),
		store.Pairing{ID: id, Code: code, Model: "fenix7spro", CreatedAt: tNow, ExpiresAt: tNow.Add(10 * time.Minute)}, 5); err != nil {
		t.Fatal(err)
	}
}

func auditRows(t *testing.T, db *store.DB, event string) []store.AuditEvent {
	t.Helper()
	all, err := db.ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEvent
	for _, e := range all {
		if e.Event == event {
			out = append(out, e)
		}
	}
	return out
}

func TestDeviceCLI(t *testing.T) {
	db, ctx := tokDB(t), context.Background()
	id := strings.Repeat("ab", 16)
	devPairing(t, db, id, "123456")

	if code, out, errs := runDev(t, db, "pair", "999999"); code != 1 || out != "" ||
		!strings.Contains(errs, "coachd device: no pending pairing with that code") {
		t.Fatalf("unknown: %d %q %q", code, out, errs)
	}
	if code, _, errs := runDev(t, db, "pair", "12ab56"); code != 1 || !strings.Contains(errs, "no pending pairing") {
		t.Fatalf("non-digit: %d %q", code, errs)
	}
	code, out, errs := runDev(t, db, "pair", "123456", "--label", "Fenix")
	if code != 0 || out != "paired: the watch will finish pairing within a few seconds\n" {
		t.Fatalf("pair: %d %q %q", code, out, errs)
	}
	rows := auditRows(t, db, "device_pairing_confirmed")
	if len(rows) != 1 || rows[0].Lane != "cli" || rows[0].Actor != "cli" || rows[0].Client != "cli" {
		t.Fatalf("audit = %+v", rows)
	}
	h := sha256.Sum256([]byte("tok"))
	if st, _, err := db.ClaimPairing(ctx, id, h[:], tNow); err != nil || st != "paired" {
		t.Fatalf("claim %q %v", st, err)
	}

	// label before the code; default label
	devPairing(t, db, strings.Repeat("cd", 16), "222222")
	if code, _, errs := runDev(t, db, "pair", "--label", "Second", "222222"); code != 0 {
		t.Fatalf("label first: %d %q", code, errs)
	}
	devPairing(t, db, strings.Repeat("ef", 16), "333333")
	if code, _, errs := runDev(t, db, "pair", "333333"); code != 0 {
		t.Fatalf("default label: %d %q", code, errs)
	}
	h2 := sha256.Sum256([]byte("tok3"))
	db.ClaimPairing(ctx, strings.Repeat("ef", 16), h2[:], tNow)
	ds, _ := db.ListDevices(ctx)
	if len(ds) != 2 || ds[0].Label != "Fenix" || ds[1].Label != "watch 2026-10-23" {
		t.Fatalf("devices = %+v", ds)
	}
	if code, _, errs := runDev(t, db, "pair", "444444", "--label", strings.Repeat("x", 41)); code != 1 ||
		!strings.Contains(errs, "41 characters exceeds the 40 limit") {
		t.Fatalf("long label: %d %q", code, errs)
	}
	for _, label := range []string{"Fen\tix", "Fen\nix"} {
		if code, _, errs := runDev(t, db, "pair", "444444", "--label", label); code != 1 ||
			errs != "coachd device: label: must be a single line (no tab or newline)\n" {
			t.Fatalf("label %q: %d %q", label, code, errs)
		}
	}

	code, out, _ = runDev(t, db, "list")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 3 {
		t.Fatalf("list: %d %q", code, out)
	}
	for _, want := range []string{"ID", "LABEL", "MODEL", "PAIRED", "LAST SEEN", "STATUS"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("header lacks %s: %q", want, lines[0])
		}
	}
	if !strings.Contains(lines[1], "Fenix") || !strings.Contains(lines[1], "active") || !strings.Contains(lines[1], " -  ") {
		t.Errorf("row = %q", lines[1])
	}

	if code, out, errs := runDev(t, db, "revoke", "1"); code != 0 || out != "revoked\n" {
		t.Fatalf("revoke: %d %q %q", code, out, errs)
	}
	if code, _, errs := runDev(t, db, "revoke", "1"); code != 1 || !strings.Contains(errs, "coachd device: no active device with that id") {
		t.Fatalf("revoke again: %d %q", code, errs)
	}
	if _, out, _ := runDev(t, db, "list"); !strings.Contains(out, "revoked") {
		t.Errorf("list lacks revoked: %q", out)
	}
	if rows := auditRows(t, db, "device_revoked"); len(rows) != 1 || rows[0].Lane != "cli" || rows[0].Actor != "cli" || rows[0].Client != "cli" {
		t.Fatalf("revoke audit = %+v", rows)
	}

	for _, args := range [][]string{nil, {"bogus"}, {"pair"}, {"pair", "1", "2"}, {"list", "x"}, {"revoke"}, {"revoke", "abc"}, {"revoke", "1", "2"}} {
		if code, _, errs := runDev(t, db, args...); code != 2 || !strings.Contains(errs, "usage: coachd device") {
			t.Errorf("%v: %d %q", args, code, errs)
		}
	}
}

// The default label's date is in COACHD_TIMEZONE when set, UTC otherwise.
func TestDevicePairDefaultLabelTimezone(t *testing.T) {
	bratislava, err := time.LoadLocation("Europe/Bratislava")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		env  string
		want *time.Location
	}{{"", time.UTC}, {"Europe/Bratislava", bratislava}} {
		got, err := deviceTZ(func(string) string { return c.env })
		if err != nil || got.String() != c.want.String() {
			t.Errorf("deviceTZ(%q) = %v, %v", c.env, got, err)
		}
	}
	if _, err := deviceTZ(func(string) string { return "Nowhere/Bogus" }); err == nil {
		t.Error("deviceTZ accepted an unknown zone")
	}

	db, ctx := tokDB(t), context.Background()
	late := time.Date(2026, 10, 23, 22, 30, 0, 0, time.UTC) // 00:30 on 24 Oct in Bratislava
	for i, c := range []struct {
		tz   *time.Location
		want string
	}{{time.UTC, "watch 2026-10-23"}, {bratislava, "watch 2026-10-24"}} {
		id, code := strings.Repeat(string(rune('a'+i)), 32), "55555"+string(rune('0'+i))
		if err := db.CreatePairing(ctx, store.Pairing{ID: id, Code: code, CreatedAt: late, ExpiresAt: late.Add(10 * time.Minute)}, 5); err != nil {
			t.Fatal(err)
		}
		if code, _, errs := runDevAt(t, db, late, c.tz, "pair", code); code != 0 {
			t.Fatalf("%s: %d %q", c.tz, code, errs)
		}
		h := sha256.Sum256([]byte(id))
		if st, _, err := db.ClaimPairing(ctx, id, h[:], late); err != nil || st != "paired" {
			t.Fatalf("claim %q %v", st, err)
		}
	}
	ds, err := db.ListDevices(ctx)
	if err != nil || len(ds) != 2 || ds[0].Label != "watch 2026-10-23" || ds[1].Label != "watch 2026-10-24" {
		t.Fatalf("devices = %+v, %v", ds, err)
	}
}
