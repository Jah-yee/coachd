package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/store"
)

func TestEnrollCommand(t *testing.T) {
	db := tokDB(t)
	ctx := context.Background()
	var out, errb bytes.Buffer
	if code := runEnroll(ctx, db, "https://coachd.example.com", true, func() time.Time { return tNow }, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 || lines[0] != "Open this link within 10 minutes to enrol a passkey:" {
		t.Fatalf("output %q", out.String())
	}
	const prefix = "https://coachd.example.com/enroll#"
	if !strings.HasPrefix(lines[1], prefix) {
		t.Fatalf("link %q", lines[1])
	}
	token := strings.TrimPrefix(lines[1], prefix)
	if len(token) != 43 || strings.ContainsAny(token, "=+/") { // 32 bytes, raw base64url
		t.Fatalf("token shape %q", token)
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr %q", errb.String())
	}
	sum := sha256.Sum256([]byte(token))
	if _, err := db.EnrollmentByHash(ctx, sum[:], tNow.Add(9*time.Minute)); err != nil {
		t.Fatalf("stored hash does not match printed token: %v", err)
	}
	if _, err := db.EnrollmentByHash(ctx, sum[:], tNow.Add(11*time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expiry not 10 min: %v", err)
	}
	evs, err := db.ListAudit(ctx, 10)
	if err != nil || len(evs) != 1 || evs[0].Event != "enrollment_created" || evs[0].Actor != "cli" {
		t.Fatalf("audit %+v %v", evs, err)
	}
}

func TestEnrollCommandNeedsPublicURL(t *testing.T) {
	db := tokDB(t)
	var out, errb bytes.Buffer
	if code := runEnroll(context.Background(), db, "", true, time.Now, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if out.Len() != 0 || !strings.Contains(errb.String(), "COACHD_PUBLIC_URL") {
		t.Fatalf("out %q err %q", out.String(), errb.String())
	}
}

// enrollEnvFor runs `coachd enroll` with env on a fresh data directory.
func enrollEnvFor(t *testing.T, env map[string]string) (code int, dir, stdout, stderr string) {
	t.Helper()
	dir = t.TempDir()
	m := map[string]string{"COACHD_DATA_DIR": dir}
	for k, v := range env {
		m[k] = v
	}
	var out, errb bytes.Buffer
	code = enrollEnv(context.Background(), func(k string) string { return m[k] }, func() time.Time { return tNow }, &out, &errb)
	return code, dir, out.String(), errb.String()
}

var accessEnv = map[string]string{"COACHD_ACCESS_TEAM_DOMAIN": "t.cloudflareaccess.com", "COACHD_ACCESS_AUD": "aud",
	"COACHD_ACCESS_ALLOWED_EMAIL": "a@b.c"}

// enroll validates COACHD_PUBLIC_URL as serve does, before opening the store.
func TestEnrollValidatesPublicURL(t *testing.T) {
	for _, bad := range []string{"http://coachd.example.com", "https://coachd.example.com:", "coachd.example.com", "https://x.example.com/path"} {
		code, dir, out, errb := enrollEnvFor(t, map[string]string{"COACHD_PUBLIC_URL": bad})
		if code != 2 || out != "" || !strings.Contains(errb, "COACHD_PUBLIC_URL: must be https://") {
			t.Errorf("%q: exit %d, out %q, err %q", bad, code, out, errb)
		}
		if _, err := os.Stat(filepath.Join(dir, "coachd.db")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%q: the store was opened: %v", bad, err)
		}
	}
	code, _, out, errb := enrollEnvFor(t, nil)
	if code != 2 || out != "" || !strings.Contains(errb, "COACHD_PUBLIC_URL is required") {
		t.Errorf("unset: exit %d, out %q, err %q", code, out, errb)
	}
	env := map[string]string{"COACHD_PUBLIC_URL": "https://Coachd.Example.com/"}
	for k, v := range accessEnv {
		env[k] = v
	}
	code, _, out, errb = enrollEnvFor(t, env)
	if code != 0 || !strings.Contains(out, "\nhttps://coachd.example.com/enroll#") || errb != "" {
		t.Errorf("mixed case: exit %d, out %q, err %q", code, out, errb)
	}
}

// With the Access settings unset the public lane is off, so the link cannot
// work yet: enroll still prints it but says so on stderr.
func TestEnrollWarnsWithoutPublicLane(t *testing.T) {
	const note = "note: the public lane is disabled (COACHD_ACCESS_* unset); the link works once it is enabled\n"
	code, _, out, errb := enrollEnvFor(t, map[string]string{"COACHD_PUBLIC_URL": "https://coachd.example.com"})
	if code != 0 || !strings.Contains(out, "https://coachd.example.com/enroll#") || errb != note {
		t.Fatalf("exit %d, out %q, err %q", code, out, errb)
	}
	token := out[strings.Index(out, "#")+1:]
	if strings.Contains(errb, strings.TrimSpace(token)) {
		t.Fatal("stderr leaks the token")
	}
}

// In builtin mode the page is served without the Access settings, so the
// link works as printed and enroll adds no note.
func TestEnrollBuiltinNoNote(t *testing.T) {
	code, _, out, errb := enrollEnvFor(t, map[string]string{"COACHD_PUBLIC_URL": "https://coachd.example.com",
		"COACHD_PUBLIC_AUTH": "builtin"})
	if code != 0 || !strings.Contains(out, "https://coachd.example.com/enroll#") || errb != "" {
		t.Fatalf("exit %d, out %q, err %q", code, out, errb)
	}
}
