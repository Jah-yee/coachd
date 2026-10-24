package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/backup"
)

var backupNow = time.Date(2026, 10, 24, 3, 15, 0, 0, time.UTC)

func TestBackupCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COACHD_DATA_DIR", dir)
	var out, errb bytes.Buffer
	if code := runBackup(context.Background(), nil, dir, backupNow, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "backup written: ") || !strings.Contains(out.String(), "0 FIT files, 0 old backups removed)") {
		t.Fatalf("stdout %q", out.String())
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "backups", "coachd-*.json")); len(m) != 1 {
		t.Fatalf("manifests %v", m)
	}
}

func TestBackupCommandExtraArgs(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runBackup(context.Background(), []string{"x"}, t.TempDir(), backupNow, &out, &errb); code != 2 || errb.String() != "usage: coachd backup\n" {
		t.Fatalf("code %d %q", code, errb.String())
	}
}

// holdBackupLock takes the backups-directory lock under dataDir as another
// process would (a separate open file), until the test ends.
func holdBackupLock(t *testing.T, dataDir string) {
	t.Helper()
	dir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
}

func TestBackupCommandBusyDoesNotOpenStore(t *testing.T) {
	dir := t.TempDir()
	holdBackupLock(t, dir)
	var out, errb bytes.Buffer
	if code := runBackup(context.Background(), nil, dir, backupNow, &out, &errb); code != 1 || errb.String() != "coachd backup: another backup or restore is running\n" {
		t.Fatalf("code %d %q", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "coachd.db")); !os.IsNotExist(err) {
		t.Fatalf("coachd.db exists (err=%v): the store was opened while busy", err)
	}
}

func TestRestoreCommandUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runRestore(context.Background(), nil, t.TempDir(), backupNow, &out, &errb); code != 2 {
		t.Fatalf("code %d", code)
	}
	want := "usage: coachd restore <snapshot.db>\n  stop coachd first: docker compose stop coachd\n"
	if errb.String() != want {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestRestoreCommandRefusesWhileServing(t *testing.T) {
	dir := t.TempDir()
	release, err := backup.LockData(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var out, errb bytes.Buffer
	code := runRestore(context.Background(), []string{"snap.db"}, dir, backupNow, &out, &errb)
	want := "coachd restore: coachd serve is running on this data directory; stop it first: docker compose stop coachd\n"
	if code != 1 || errb.String() != want {
		t.Fatalf("code %d %q", code, errb.String())
	}
}

func TestRestoreCommandRestores(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := runBackup(context.Background(), nil, dir, backupNow, &out, &errb); code != 0 {
		t.Fatalf("backup %d: %s", code, errb.String())
	}
	snaps, _ := filepath.Glob(filepath.Join(dir, "backups", "coachd-*.db"))
	if len(snaps) != 1 {
		t.Fatalf("snapshots %v", snaps)
	}
	out.Reset()
	errb.Reset()
	if code := runRestore(context.Background(), []string{snaps[0]}, dir, backupNow.Add(time.Hour), &out, &errb); code != 0 {
		t.Fatalf("restore %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "restored ") || !strings.Contains(out.String(), "previous database moved to ") {
		t.Fatalf("stdout %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "coachd.db")); err != nil {
		t.Fatal(err)
	}
}

const sessionsNote = "note: runner sign-in sessions were cleared; tokens, passkeys and watches added after the snapshot are gone and must be created again\n"

func TestRestoreCommandReportsCarryForward(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := runBackup(context.Background(), nil, dir, backupNow, &out, &errb); code != 0 {
		t.Fatalf("backup %d: %s", code, errb.String())
	}
	snaps, _ := filepath.Glob(filepath.Join(dir, "backups", "coachd-*.db"))
	out.Reset()
	if code := runRestore(context.Background(), []string{snaps[0]}, dir, backupNow.Add(time.Hour), &out, &errb); code != 0 {
		t.Fatalf("restore %d: %s", code, errb.String())
	}
	want := "carried forward 0 revocations and 0 used approval nonces from the previous database\n" + sessionsNote
	if !strings.HasSuffix(out.String(), want) {
		t.Fatalf("stdout %q, want suffix %q", out.String(), want)
	}
}

func TestRestoreCommandWarnsWhenCarryForwardFails(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := runBackup(context.Background(), nil, dir, backupNow, &out, &errb); code != 0 {
		t.Fatalf("backup %d: %s", code, errb.String())
	}
	snaps, _ := filepath.Glob(filepath.Join(dir, "backups", "coachd-*.db"))
	if err := os.WriteFile(filepath.Join(dir, "coachd.db"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runRestore(context.Background(), []string{snaps[0]}, dir, backupNow.Add(time.Hour), &out, &errb); code != 0 {
		t.Fatalf("restore %d: %s", code, errb.String())
	}
	lines := strings.SplitAfter(out.String(), "\n")
	if len(lines) < 3 || lines[len(lines)-1] != "" || lines[len(lines)-2] != sessionsNote {
		t.Fatalf("stdout %q, want it to end with %q", out.String(), sessionsNote)
	}
	warn := lines[len(lines)-3]
	if !strings.HasPrefix(warn, "warning: could not carry revocations forward (") ||
		!strings.HasSuffix(warn, "); check docker compose exec coachd /coachd token list and device list, and revoke again anything revoked after the snapshot\n") {
		t.Fatalf("warning line %q", warn)
	}
	if strings.Contains(out.String(), "carried forward") {
		t.Fatalf("stdout %q reports a carry-forward", out.String())
	}
}

func TestRestoreCommandRefusesSnapshotWithWAL(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := runBackup(context.Background(), nil, dir, backupNow, &out, &errb); code != 0 {
		t.Fatalf("backup %d: %s", code, errb.String())
	}
	snaps, _ := filepath.Glob(filepath.Join(dir, "backups", "coachd-*.db"))
	if err := os.WriteFile(snaps[0]+"-wal", []byte("frames"), 0o600); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	code := runRestore(context.Background(), []string{snaps[0]}, dir, backupNow.Add(time.Hour), &out, &errb)
	want := "coachd restore: the snapshot has a -wal file next to it with uncommitted changes; to undo a restore, move all three files back instead\n"
	if code != 1 || errb.String() != want {
		t.Fatalf("code %d %q", code, errb.String())
	}
}
