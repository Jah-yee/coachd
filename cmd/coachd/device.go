package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
)

const (
	deviceUsage = "usage: coachd device <pair CODE [--label LABEL] | list | revoke ID>\n" +
		"  pair: confirm the 6-digit code the watch shows; LABEL defaults to \"watch <date>\" (in COACHD_TIMEZONE, else UTC),\n" +
		"  one line of at most 40 characters\n"
	msgNoPairing = "no pending pairing with that code; codes expire after 10 minutes"
)

// deviceMain opens the store (without requiring serve-only config) and runs a device subcommand.
func deviceMain(args []string) int {
	dir := os.Getenv("COACHD_DATA_DIR")
	if dir == "" {
		dir = "/data"
	}
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(dir, "coachd.db"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "coachd device: cannot open store: %v\n", err)
		return 1
	}
	defer db.Close()
	tz, err := deviceTZ(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coachd device: %v\n", err)
		return 1
	}
	return runDevice(ctx, args, db, time.Now, tz, os.Stdout, os.Stderr)
}

// deviceTZ is COACHD_TIMEZONE when set (the default label's date), UTC
// otherwise.
func deviceTZ(getenv func(string) string) (*time.Location, error) {
	name := getenv("COACHD_TIMEZONE")
	if name == "" {
		return time.UTC, nil
	}
	tz, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("COACHD_TIMEZONE: %w", err)
	}
	return tz, nil
}

func runDevice(ctx context.Context, args []string, db *store.DB, now func() time.Time, tz *time.Location, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, deviceUsage)
		return 2
	}
	switch args[0] {
	case "pair":
		return devicePair(ctx, args[1:], db, now, tz, stdout, stderr)
	case "list":
		if len(args) != 1 {
			fmt.Fprint(stderr, deviceUsage)
			return 2
		}
		return deviceList(ctx, db, stdout, stderr)
	case "revoke":
		return deviceRevoke(ctx, args[1:], db, now, stdout, stderr)
	default:
		fmt.Fprint(stderr, deviceUsage)
		return 2
	}
}

// parseInterspersed parses fs over args allowing flags before or after the
// positional arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func validCode(c string) bool {
	if len(c) != 6 {
		return false
	}
	for i := 0; i < len(c); i++ {
		if c[i] < '0' || c[i] > '9' {
			return false
		}
	}
	return true
}

func devicePair(ctx context.Context, args []string, db *store.DB, now func() time.Time, tz *time.Location, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("device pair", flag.ContinueOnError)
	fs.SetOutput(stderr)
	label := fs.String("label", "", "device label (default \"watch <date>\", in COACHD_TIMEZONE, else UTC)")
	pos, err := parseInterspersed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(stderr, deviceUsage)
		return 2
	}
	t := now().UTC()
	lbl := strings.TrimSpace(*label)
	if lbl == "" {
		lbl = "watch " + t.In(tz).Format("2006-01-02")
	}
	var inv *workout.Invalid
	if err := workout.CheckLine("label", lbl, 40); errors.As(err, &inv) {
		fmt.Fprintln(stderr, "coachd device:", inv.Reason)
		return 1
	}
	code := strings.TrimSpace(pos[0])
	if !validCode(code) {
		fmt.Fprintln(stderr, "coachd device:", msgNoPairing)
		return 1
	}
	if _, err := db.ConfirmPairing(ctx, code, lbl, t, "cli", "cli", "cli"); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintln(stderr, "coachd device:", msgNoPairing)
		} else {
			fmt.Fprintln(stderr, "coachd device: cannot confirm pairing")
		}
		return 1
	}
	checkpoint(ctx, db, stderr)
	fmt.Fprintln(stdout, "paired: the watch will finish pairing within a few seconds")
	return 0
}

func deviceList(ctx context.Context, db *store.DB, stdout, stderr io.Writer) int {
	ds, err := db.ListDevices(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "coachd device: cannot list devices")
		return 1
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tLABEL\tMODEL\tPAIRED\tLAST SEEN\tSTATUS")
	for _, d := range ds {
		seen, status := "-", "active"
		if d.LastSeenAt != nil {
			seen = d.LastSeenAt.UTC().Format(time.RFC3339)
		}
		if d.RevokedAt != nil {
			status = "revoked"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Label, d.Model, d.CreatedAt.UTC().Format(time.RFC3339), seen, status)
	}
	w.Flush()
	return 0
}

func deviceRevoke(ctx context.Context, args []string, db *store.DB, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, deviceUsage)
		return 2
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprint(stderr, deviceUsage)
		return 2
	}
	if err := db.RevokeDevice(ctx, id, now().UTC(), "cli", "cli", "cli"); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintln(stderr, "coachd device: no active device with that id")
		} else {
			fmt.Fprintln(stderr, "coachd device: cannot revoke device")
		}
		return 1
	}
	checkpoint(ctx, db, stderr)
	fmt.Fprintln(stdout, "revoked")
	return 0
}
