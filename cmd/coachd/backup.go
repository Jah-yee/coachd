package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/stefiix92/coachd/internal/backup"
	"github.com/stefiix92/coachd/internal/store"
)

const restoreUsage = "usage: coachd restore <snapshot.db>\n  stop coachd first: docker compose stop coachd\n"

func dataDirFromEnv() string {
	if dir := os.Getenv("COACHD_DATA_DIR"); dir != "" {
		return dir
	}
	return "/data"
}

// backupMain writes a backup now. It does not take the data lock, so it works
// while serve runs; the backups lock keeps it from overlapping serve's own.
func backupMain(args []string) int {
	return runBackup(context.Background(), args, dataDirFromEnv(), time.Now(), os.Stdout, os.Stderr)
}

func runBackup(ctx context.Context, args []string, dataDir string, now time.Time, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: coachd backup")
		return 2
	}
	// The backups lock comes first: a restore holding it must not see the
	// store opened (and migrated) under it.
	open := func() (*store.DB, error) { return store.Open(ctx, filepath.Join(dataDir, "coachd.db")) }
	res, err := backup.RunWith(ctx, open, dataDir, version, now)
	if errors.Is(err, backup.ErrBusy) {
		fmt.Fprintln(stderr, "coachd backup: another backup or restore is running")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "coachd backup: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "backup written: %s (database %d bytes, %d FIT files, %d old backups removed)\n",
		res.Snapshot, res.DBBytes, res.FITFiles, res.Pruned)
	return 0
}

// restoreMain replaces the database from a snapshot. It never opens the live
// store, which would migrate it.
func restoreMain(args []string) int {
	return runRestore(context.Background(), args, dataDirFromEnv(), time.Now(), os.Stdout, os.Stderr)
}

func runRestore(ctx context.Context, args []string, dataDir string, now time.Time, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, restoreUsage)
		return 2
	}
	res, err := backup.Restore(ctx, dataDir, args[0], now)
	if errors.Is(err, backup.ErrLocked) {
		fmt.Fprintln(stderr, "coachd restore: coachd serve is running on this data directory; stop it first: docker compose stop coachd")
		return 1
	}
	if errors.Is(err, backup.ErrSnapshotHasWAL) {
		fmt.Fprintln(stderr, "coachd restore: the snapshot has a -wal file next to it with uncommitted changes; to undo a restore, move all three files back instead")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "coachd restore: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "restored %s (schema %s)\n", args[0], res.SchemaVersion)
	if res.PreRestoreDir != "" {
		fmt.Fprintf(stdout, "previous database moved to %s\n", res.PreRestoreDir)
	}
	if !res.ManifestChecked {
		fmt.Fprintln(stdout, "note: no manifest next to the snapshot; FIT files not checked")
	}
	if res.FITMissing > 0 {
		fmt.Fprintf(stdout, "warning: %d FIT files listed in the manifest are missing; restore fit/ from your off-site copy\n", res.FITMissing)
	}
	if res.FITMismatched > 0 {
		fmt.Fprintf(stdout, "warning: %d FIT files differ from the manifest\n", res.FITMismatched)
	}
	switch {
	case res.CarryForwardErr != nil:
		fmt.Fprintf(stdout, "warning: could not carry revocations forward (%v); check docker compose exec coachd /coachd token list and device list, and revoke again anything revoked after the snapshot\n", res.CarryForwardErr)
	case res.PreRestoreDir != "":
		fmt.Fprintf(stdout, "carried forward %d revocations and %d used approval nonces from the previous database\n", res.RevocationsCarried, res.NoncesCarried)
	}
	fmt.Fprintln(stdout, "note: runner sign-in sessions were cleared; tokens, passkeys and watches added after the snapshot are gone and must be created again")
	return 0
}
