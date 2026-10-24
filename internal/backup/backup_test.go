package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/store"
)

var t0 = time.Date(2026, 10, 24, 3, 15, 0, 0, time.UTC)

// fitFiles are written by setup under <dataDir>/fit.
var fitFiles = map[string]string{
	"fit/2026/101.fit": "first activity",
	"fit/2026/102.fit": "second activity, longer",
}

// setup returns a data directory with an open coachd.db and the fitFiles.
func setup(t *testing.T) (string, *store.DB) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dataDir, "coachd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for rel, body := range fitFiles {
		writeFile(t, filepath.Join(dataDir, filepath.FromSlash(rel)), body)
	}
	return dataDir, db
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sha(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func readManifest(t *testing.T, path string) Manifest {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// listDir returns the names in dir, sorted.
func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestRunWritesSnapshotAndManifest(t *testing.T) {
	dataDir, db := setup(t)
	ctx := context.Background()
	// A symlink under fit/ is skipped, not followed.
	outside := filepath.Join(t.TempDir(), "outside.fit")
	writeFile(t, outside, "not ours")
	if err := os.Symlink(outside, filepath.Join(dataDir, "fit", "2026", "103.fit")); err != nil {
		t.Fatal(err)
	}
	res, err := Run(ctx, db, dataDir, "v-test", t0)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(dataDir, DirName)
	name := "coachd-20261024T031500Z"
	if res.Snapshot != filepath.Join(dir, name+".db") || res.Manifest != filepath.Join(dir, name+".json") {
		t.Fatalf("paths %q %q", res.Snapshot, res.Manifest)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v err=%v", fi.Mode(), err)
	}
	for _, p := range []string{res.Snapshot, res.Manifest} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v err=%v", filepath.Base(p), fi.Mode(), err)
		}
	}
	if got := listDir(t, dir); !slices.Equal(got, []string{".lock", name + ".db", name + ".json"}) {
		t.Fatalf("backups dir %v", got)
	}

	m := readManifest(t, res.Manifest)
	fi, _ := os.Stat(res.Snapshot)
	if m.DB.Path != name+".db" || m.DB.SHA256 != sha(t, res.Snapshot) || m.DB.Size != fi.Size() || res.DBBytes != fi.Size() {
		t.Fatalf("db entry %+v, file size %d, DBBytes %d", m.DB, fi.Size(), res.DBBytes)
	}
	if !m.CreatedAt.Equal(t0) || m.Version != "v-test" || m.SchemaVersion != store.LatestSchemaVersion() {
		t.Fatalf("manifest %+v", m)
	}
	var want []FileEntry
	var wantBytes int64
	for rel, body := range fitFiles {
		want = append(want, FileEntry{Path: rel, Size: int64(len(body)), SHA256: sha(t, filepath.Join(dataDir, rel))})
		wantBytes += int64(len(body))
	}
	slices.SortFunc(want, func(a, b FileEntry) int { return strings.Compare(a.Path, b.Path) })
	if !slices.Equal(m.FIT, want) {
		t.Fatalf("fit entries\n got %+v\nwant %+v", m.FIT, want)
	}
	if res.FITFiles != len(want) || res.FITBytes != wantBytes || res.Pruned != 0 {
		t.Fatalf("result %+v", res)
	}
	if info, err := store.VerifySnapshot(ctx, res.Snapshot); err != nil || info.SchemaVersion != m.SchemaVersion {
		t.Fatalf("verify %+v err=%v", info, err)
	}
}

// TestRunSkipsHiddenAndTempFIT: ingest writes FIT files as .fit-*.tmp and
// renames them; neither those nor hidden directories belong in a manifest.
func TestRunSkipsHiddenAndTempFIT(t *testing.T) {
	dataDir, db := setup(t)
	for _, rel := range []string{"fit/2026/.fit-123.tmp", "fit/2026/104.fit.tmp", "fit/.hidden/105.fit"} {
		writeFile(t, filepath.Join(dataDir, filepath.FromSlash(rel)), "in progress")
	}
	res, err := Run(context.Background(), db, dataDir, "v-test", t0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range readManifest(t, res.Manifest).FIT {
		got = append(got, e.Path)
	}
	if want := []string{"fit/2026/101.fit", "fit/2026/102.fit"}; !slices.Equal(got, want) {
		t.Fatalf("manifest FIT %v, want %v", got, want)
	}
}

// TestRunSkipsVanishedFIT: a FIT file removed between the directory listing
// and its hash is left out instead of failing the backup.
func TestRunSkipsVanishedFIT(t *testing.T) {
	dataDir, db := setup(t)
	vanished := filepath.Join(dataDir, "fit", "2026", "101.fit")
	hashFIT = func(path string) (FileEntry, error) {
		if path == vanished {
			if err := os.Remove(path); err != nil {
				t.Error(err)
			}
		}
		return hashFile(path)
	}
	t.Cleanup(func() { hashFIT = hashFile })
	res, err := Run(context.Background(), db, dataDir, "v-test", t0)
	if err != nil {
		t.Fatal(err)
	}
	if m := readManifest(t, res.Manifest); len(m.FIT) != 1 || m.FIT[0].Path != "fit/2026/102.fit" || res.FITFiles != 1 {
		t.Fatalf("manifest FIT %+v, FITFiles %d", m.FIT, res.FITFiles)
	}
}

func TestRunTightensExistingBackupsDir(t *testing.T) {
	dataDir, db := setup(t)
	dir := filepath.Join(dataDir, DirName)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), db, dataDir, "v-test", t0); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v err=%v, want 0700", fi.Mode(), err)
	}
}

func TestRunWithoutFITDir(t *testing.T) {
	dataDir, db := setup(t)
	if err := os.RemoveAll(filepath.Join(dataDir, "fit")); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), db, dataDir, "v-test", t0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(res.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if res.FITFiles != 0 || !strings.Contains(string(b), `"fit": []`) {
		t.Fatalf("FITFiles %d, manifest %s", res.FITFiles, b)
	}
}

func TestRunPrunesToKeep(t *testing.T) {
	dataDir, db := setup(t)
	ctx := context.Background()
	for i := range 9 {
		res, err := Run(ctx, db, dataDir, "v-test", t0.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		wantPruned := 0
		if i >= Keep {
			wantPruned = 1
		}
		if res.Pruned != wantPruned {
			t.Fatalf("run %d pruned %d, want %d", i+1, res.Pruned, wantPruned)
		}
	}
	var want []string
	for i := 2; i < 9; i++ {
		base := "coachd-" + t0.Add(time.Duration(i)*time.Hour).Format("20060102T150405Z")
		want = append(want, base+".db", base+".json")
	}
	want = append([]string{".lock"}, want...)
	if got := listDir(t, filepath.Join(dataDir, DirName)); !slices.Equal(got, want) {
		t.Fatalf("backups dir\n got %v\nwant %v", got, want)
	}
}

func TestRunBusy(t *testing.T) {
	dataDir, db := setup(t)
	dir := filepath.Join(dataDir, DirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := lockBackups(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := Run(context.Background(), db, dataDir, "v-test", t0); !errors.Is(err, ErrBusy) {
		t.Fatalf("err %v, want ErrBusy", err)
	}
	if got := listDir(t, dir); !slices.Equal(got, []string{".lock"}) {
		t.Fatalf("backups dir %v", got)
	}
}

func TestRunWithBusySkipsOpen(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, DirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := lockBackups(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	opened := false
	open := func() (*store.DB, error) {
		opened = true
		return store.Open(context.Background(), filepath.Join(dataDir, "coachd.db"))
	}
	if _, err := RunWith(context.Background(), open, dataDir, "v-test", t0); !errors.Is(err, ErrBusy) {
		t.Fatalf("err %v, want ErrBusy", err)
	}
	if opened {
		t.Fatal("RunWith opened the store while the backup lock was held")
	}
}

func TestRunWithOpensAndCloses(t *testing.T) {
	dataDir := t.TempDir()
	var db *store.DB
	open := func() (*store.DB, error) {
		var err error
		db, err = store.Open(context.Background(), filepath.Join(dataDir, "coachd.db"))
		return db, err
	}
	res, err := RunWith(context.Background(), open, dataDir, "v-test", t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.Manifest); err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(context.Background()); err == nil {
		t.Fatal("RunWith left the store open")
	}
	failed := errors.New("open failed")
	if _, err := RunWith(context.Background(), func() (*store.DB, error) { return nil, failed }, dataDir, "v-test", t0.Add(time.Hour)); !errors.Is(err, failed) {
		t.Fatalf("err %v, want the open error", err)
	}
}

// TestRunKeepsNewBackupWhenClockIsBehind: a backup named before 7 others
// (the clock went back) is kept; an older one goes instead.
func TestRunKeepsNewBackupWhenClockIsBehind(t *testing.T) {
	dataDir, db := setup(t)
	ctx := context.Background()
	for i := range Keep {
		if _, err := Run(ctx, db, dataDir, "v-test", t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Run(ctx, db, dataDir, "v-test", t0.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{res.Snapshot, res.Manifest} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("new backup pruned: %v", err)
		}
	}
	names, err := complete(filepath.Join(dataDir, DirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != Keep || res.Pruned != 1 || slices.Contains(names, "coachd-20261024T031500Z") {
		t.Fatalf("complete backups %v, pruned %d: want %d with the oldest of the others gone", names, res.Pruned, Keep)
	}
}

func TestRunExists(t *testing.T) {
	dataDir, db := setup(t)
	ctx := context.Background()
	res, err := Run(ctx, db, dataDir, "v-test", t0)
	if err != nil {
		t.Fatal(err)
	}
	before := sha(t, res.Snapshot)
	if _, err := Run(ctx, db, dataDir, "v-test", t0); !errors.Is(err, ErrExists) {
		t.Fatalf("err %v, want ErrExists", err)
	}
	if sha(t, res.Snapshot) != before {
		t.Fatal("existing backup changed")
	}
}

// eightBackups leaves 8 complete backups: 7 from Run plus a copy of the
// oldest under an older name, so a run that wrongly prunes would remove one.
func eightBackups(t *testing.T, dataDir string, db *store.DB) []string {
	t.Helper()
	ctx := context.Background()
	for i := range Keep {
		if _, err := Run(ctx, db, dataDir, "v-test", t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(dataDir, DirName)
	for _, ext := range []string{".db", ".json"} {
		b, err := os.ReadFile(filepath.Join(dir, "coachd-20261024T031500Z"+ext))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "coachd-20200101T000000Z"+ext), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return listDir(t, dir)
}

func TestRunFailureLeavesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads mode 0000 files")
	}
	dataDir, db := setup(t)
	before := eightBackups(t, dataDir, db)
	unreadable := filepath.Join(dataDir, "fit", "2026", "102.fit")
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0o600) })
	if _, err := Run(context.Background(), db, dataDir, "v-test", t0.Add(24*time.Hour)); err == nil || !strings.HasPrefix(err.Error(), "backup: ") {
		t.Fatalf("err %v, want a backup: error", err)
	}
	if got := listDir(t, filepath.Join(dataDir, DirName)); !slices.Equal(got, before) {
		t.Fatalf("backups dir changed\n got %v\nwant %v", got, before)
	}
}

func TestRunCancelledLeavesNothing(t *testing.T) {
	dataDir, db := setup(t)
	before := eightBackups(t, dataDir, db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, db, dataDir, "v-test", t0.Add(24*time.Hour)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want context.Canceled", err)
	}
	if got := listDir(t, filepath.Join(dataDir, DirName)); !slices.Equal(got, before) {
		t.Fatalf("backups dir changed\n got %v\nwant %v", got, before)
	}
}

func TestRunRemovesLeftovers(t *testing.T) {
	dataDir, db := setup(t)
	dir := filepath.Join(dataDir, DirName)
	writeFile(t, filepath.Join(dir, ".coachd-20261023T031500Z.db.tmp"), "partial")
	writeFile(t, filepath.Join(dir, ".coachd-20261023T031500Z.json.tmp"), "partial")
	writeFile(t, filepath.Join(dir, "coachd-20261023T031500Z.db"), "crash leftover without manifest")
	writeFile(t, filepath.Join(dir, "notes.json"), "foreign")
	writeFile(t, filepath.Join(dir, "coachd-old.db"), "foreign")
	if _, err := Run(context.Background(), db, dataDir, "v-test", t0); err != nil {
		t.Fatal(err)
	}
	want := []string{".lock", "coachd-20261024T031500Z.db", "coachd-20261024T031500Z.json", "coachd-old.db", "notes.json"}
	if got := listDir(t, dir); !slices.Equal(got, want) {
		t.Fatalf("backups dir\n got %v\nwant %v", got, want)
	}
}

func TestNewest(t *testing.T) {
	dataDir, db := setup(t)
	if _, ok, err := Newest(dataDir); ok || err != nil {
		t.Fatalf("no dir: ok=%v err=%v", ok, err)
	}
	dir := filepath.Join(dataDir, DirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := Newest(dataDir); ok || err != nil {
		t.Fatalf("empty dir: ok=%v err=%v", ok, err)
	}
	if _, err := Run(context.Background(), db, dataDir, "v-test", t0.Add(500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".coachd-20991231T000000Z.db.tmp"), "partial")
	writeFile(t, filepath.Join(dir, ".coachd-20991231T000000Z.json.tmp"), "partial")
	writeFile(t, filepath.Join(dir, "coachd-20990101T000000Z.db"), "orphan without manifest")
	writeFile(t, filepath.Join(dir, "coachd-20980101T000000Z.json"), "manifest without snapshot")
	writeFile(t, filepath.Join(dir, "notes.json"), "foreign")
	got, ok, err := Newest(dataDir)
	if err != nil || !ok || !got.Equal(t0) {
		t.Fatalf("Newest = %v ok=%v err=%v, want %v", got, ok, err, t0)
	}
}
