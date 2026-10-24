package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stefiix92/coachd/internal/config"
	"github.com/stefiix92/coachd/internal/store"
)

const (
	enrollTTL = 10 * time.Minute

	msgNeedPublicURL = "coachd enroll: COACHD_PUBLIC_URL is required (the page's public origin, e.g. https://coachd.example.com)"
)

// enrollMain runs `coachd enroll` with the real environment.
func enrollMain() int {
	return enrollEnv(context.Background(), os.Getenv, time.Now, os.Stdout, os.Stderr)
}

// enrollEnv validates COACHD_PUBLIC_URL as serve does, then opens the store
// (without requiring serve-only config) and prints a one-time passkey
// enrolment link. A bad or missing URL exits 2 before the store is opened.
func enrollEnv(ctx context.Context, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	publicURL, err := config.ParsePublicURL(getenv("COACHD_PUBLIC_URL"))
	if err != nil {
		fmt.Fprintln(stderr, "coachd enroll:", err)
		return 2
	}
	if publicURL == "" {
		fmt.Fprintln(stderr, msgNeedPublicURL)
		return 2
	}
	dir := getenv("COACHD_DATA_DIR")
	if dir == "" {
		dir = "/data"
	}
	db, err := store.Open(ctx, filepath.Join(dir, "coachd.db"))
	if err != nil {
		fmt.Fprintf(stderr, "coachd enroll: cannot open store: %v\n", err)
		return 1
	}
	defer db.Close()
	// serve serves the page in Access mode (all three Access settings) and
	// in builtin mode.
	access := getenv("COACHD_ACCESS_TEAM_DOMAIN") != "" && getenv("COACHD_ACCESS_AUD") != "" &&
		getenv("COACHD_ACCESS_ALLOWED_EMAIL") != ""
	publicLane := access || getenv("COACHD_PUBLIC_AUTH") == string(config.PublicAuthBuiltin)
	return runEnroll(ctx, db, publicURL, publicLane, now, stdout, stderr)
}

// runEnroll creates a single-use enrolment valid for 10 minutes and prints
// its link. The token goes to stdout only; the store keeps SHA-256 of its
// base64url text, which is what the enrolment page hashes. Without the
// public lane the link is still printed, with a note on stderr.
func runEnroll(ctx context.Context, db *store.DB, publicURL string, publicLane bool, now func() time.Time, stdout, stderr io.Writer) int {
	if publicURL == "" {
		fmt.Fprintln(stderr, msgNeedPublicURL)
		return 2
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		fmt.Fprintln(stderr, "coachd enroll: cannot generate token")
		return 1
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(token))
	t := now().UTC()
	if _, err := db.CreateEnrollment(ctx, sum[:], t, t.Add(enrollTTL)); err != nil {
		fmt.Fprintln(stderr, "coachd enroll: cannot store enrolment")
		return 1
	}
	if err := db.Checkpoint(ctx); err != nil {
		fmt.Fprintln(stderr, "coachd enroll: warning: committed, but the WAL checkpoint did not complete")
	}
	fmt.Fprintln(stdout, "Open this link within 10 minutes to enrol a passkey:")
	fmt.Fprintf(stdout, "%s/enroll#%s\n", strings.TrimRight(publicURL, "/"), token)
	if !publicLane {
		fmt.Fprintln(stderr, "note: the public lane is disabled (COACHD_ACCESS_* unset); the link works once it is enabled")
	}
	return 0
}
