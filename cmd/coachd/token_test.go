package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/store"
)

var tNow = time.Date(2026, 10, 23, 6, 0, 0, 0, time.UTC)

func tokDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "coachd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func runTok(t *testing.T, db *store.DB, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runToken(context.Background(), args, db, func() time.Time { return tNow }, &out, &errb)
	return code, out.String(), errb.String()
}

func TestTokenCLICreate(t *testing.T) {
	db := tokDB(t)
	code, out, errs := runTok(t, db, "create", "--name", "laptop", "--scope", "write", "--ttl", "30d")
	plain := strings.TrimSpace(out)
	if code != 0 || !strings.HasPrefix(plain, "cdt_") || strings.Count(out, "\n") != 1 {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	if strings.Contains(errs, plain) {
		t.Fatal("stderr leaks token")
	}
	list, _ := db.ListTokens(context.Background())
	if len(list) != 1 || list[0].Name != "laptop" || strings.Join(list[0].Scopes, ",") != "coach:read,coach:write" ||
		!list[0].ExpiresAt.Equal(tNow.Add(30*24*time.Hour)) {
		t.Fatalf("%+v", list)
	}
	if _, err := hex.DecodeString(list[0].ID); err != nil || len(list[0].ID) != 16 {
		t.Fatalf("id %q", list[0].ID)
	}
	runTok(t, db, "create", "--name", "r", "--scope", "read")
	list, _ = db.ListTokens(context.Background())
	for _, r := range list {
		if r.Name == "r" && (strings.Join(r.Scopes, ",") != "coach:read" || !r.ExpiresAt.Equal(tNow.Add(90*24*time.Hour))) {
			t.Fatalf("%+v", r)
		}
	}
}

func TestTokenCLICreateValidation(t *testing.T) {
	db := tokDB(t)
	bad := [][]string{
		{"create", "--scope", "read"},
		{"create", "--name", "x"},
		{"create", "--name", "x", "--scope", "admin"},
		{"create", "--name", "", "--scope", "read"},
		{"create", "--name", strings.Repeat("a", 65), "--scope", "read"},
		{"create", "--name", "a\nb", "--scope", "read"},
		{"create", "--name", "x", "--scope", "read", "--ttl", "0d"},
		{"create", "--name", "x", "--scope", "read", "--ttl", "366d"},
		{"create", "--name", "x", "--scope", "read", "--ttl", "-5h"},
		{"create", "--name", "x", "--scope", "read", "--ttl", "bogus"},
		{"bogus"},
		{},
	}
	for _, a := range bad {
		if code, out, _ := runTok(t, db, a...); code == 0 || out != "" {
			t.Fatalf("%v: code %d out %q", a, code, out)
		}
	}
	if list, _ := db.ListTokens(context.Background()); len(list) != 0 {
		t.Fatal("token created on invalid input")
	}
	if code, _, _ := runTok(t, db, "create", "--name", "x", "--scope", "read", "--ttl", "12h"); code != 0 {
		t.Fatal("go duration should be accepted")
	}
}

func TestTokenCLIListNeverPrintsHash(t *testing.T) {
	db := tokDB(t)
	_, plain, _ := runTok(t, db, "create", "--name", "laptop", "--scope", "read")
	list, _ := db.ListTokens(context.Background())
	code, out, _ := runTok(t, db, "list")
	if code != 0 || !strings.Contains(out, list[0].ID) || !strings.Contains(out, "laptop") || !strings.Contains(out, "coach:read") {
		t.Fatalf("%d %q", code, out)
	}
	h := list[0].Hash
	for _, bad := range []string{hex.EncodeToString(h), string(h), strings.TrimSpace(plain)} {
		if strings.Contains(out, bad) {
			t.Fatalf("list output leaks secret material: %q", out)
		}
	}
	for _, col := range []string{"id", "name", "scopes", "created", "expires", "last_used", "revoked"} {
		if !strings.Contains(out, col) {
			t.Fatalf("missing column %s", col)
		}
	}
}

func TestTokenCLIRevokeAndAudit(t *testing.T) {
	db := tokDB(t)
	runTok(t, db, "create", "--name", "laptop", "--scope", "read")
	list, _ := db.ListTokens(context.Background())
	id := list[0].ID
	if code, _, _ := runTok(t, db, "revoke", id); code != 0 {
		t.Fatal("revoke failed")
	}
	list, _ = db.ListTokens(context.Background())
	if list[0].RevokedAt == nil || !list[0].RevokedAt.Equal(tNow) {
		t.Fatalf("%+v", list[0])
	}
	if code, _, errs := runTok(t, db, "revoke", "nope"); code != 1 || errs == "" {
		t.Fatalf("%d %q", code, errs)
	}
	if code, _, _ := runTok(t, db, "revoke"); code == 0 {
		t.Fatal("missing id must fail")
	}
	_, out, _ := runTok(t, db, "list")
	if !strings.Contains(out, "2026-10-23T06:00:00Z") {
		t.Fatalf("revoked time missing: %q", out)
	}
}

func TestTokenCLIAuditEvents(t *testing.T) {
	db := tokDB(t)
	rec := &recAudit{db: db}
	var out, errb bytes.Buffer
	code := runTokenWith(context.Background(), []string{"create", "--name", "n", "--scope", "read"}, db, rec, func() time.Time { return tNow }, &out, &errb)
	list, _ := db.ListTokens(context.Background())
	runTokenWith(context.Background(), []string{"revoke", list[0].ID}, db, rec, func() time.Time { return tNow }, &out, &errb)
	if code != 0 || len(rec.ev) != 2 || rec.ev[0].Event != "token_created" || rec.ev[1].Event != "token_revoked" ||
		rec.ev[0].Lane != "cli" || rec.ev[0].Actor != "cli" || rec.ev[0].Target != list[0].ID || rec.ev[0].Outcome != "ok" {
		t.Fatalf("%d %+v", code, rec.ev)
	}
}

type recAudit struct {
	db *store.DB
	ev []store.AuditEvent
}

func (r *recAudit) AppendAudit(ctx context.Context, e store.AuditEvent) error {
	r.ev = append(r.ev, e)
	return r.db.AppendAudit(ctx, e)
}

// mainFileToken reads token id from a copy of only the database file, as if
// the -wal file were lost: what survives a power cut under synchronous=NORMAL.
func mainFileToken(t *testing.T, path, id string) (found, revoked bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(cp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := sql.Open("sqlite", "file:"+cp)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var rev sql.NullString
	if err := c.QueryRow(`SELECT revoked_at FROM api_tokens WHERE id=?`, id).Scan(&rev); err != nil {
		return false, false
	}
	return true, rev.Valid
}

// create and revoke checkpoint the WAL, so the new token and, above all, a
// revocation (the kill switch) are in the database file when the CLI exits.
func TestTokenCLIDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coachd.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if code, _, errs := runTok(t, db, "create", "--name", "laptop", "--scope", "read"); code != 0 {
		t.Fatalf("create: %d %q", code, errs)
	}
	list, _ := db.ListTokens(context.Background())
	id := list[0].ID
	if found, _ := mainFileToken(t, path, id); !found {
		t.Fatal("created token is not in the database file after create")
	}
	if code, _, errs := runTok(t, db, "revoke", id); code != 0 {
		t.Fatalf("revoke: %d %q", code, errs)
	}
	if found, revoked := mainFileToken(t, path, id); !found || !revoked {
		t.Fatalf("after revoke the database file has found=%v revoked=%v, want both", found, revoked)
	}
}
