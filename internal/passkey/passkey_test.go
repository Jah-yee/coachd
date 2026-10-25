package passkey_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/stefiix92/coachd/internal/passkey"
	"github.com/stefiix92/coachd/internal/passkey/passkeytest"
	"github.com/stefiix92/coachd/internal/store"
)

const testOrigin = "http://localhost:8080"

var t0 = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func testKey() []byte { return bytes.Repeat([]byte{7}, 32) }

func testChallenge() string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)) }

func openDB(t *testing.T) *store.DB {
	t.Helper()
	return openDBAt(t, filepath.Join(t.TempDir(), "coachd.db"))
}

func openDBAt(t *testing.T, path string) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// rawExec runs SQL on the database at path over a second connection, to
// break the store underneath the service.
func rawExec(t *testing.T, path, query string) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(query); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// invalidCeremony reports whether err is ErrCeremony but not ErrCeremonyExpired.
func invalidCeremony(err error) bool {
	return errors.Is(err, passkey.ErrCeremony) && !errors.Is(err, passkey.ErrCeremonyExpired)
}

func newService(t *testing.T, db *store.DB, origin string, clk *clock) *passkey.Service {
	t.Helper()
	svc, err := passkey.New(origin, passkey.NewSigner(testKey()), db, clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func setup(t *testing.T) (*passkey.Service, *store.DB, *clock) {
	t.Helper()
	db := openDB(t)
	clk := &clock{t: t0}
	return newService(t, db, testOrigin, clk), db, clk
}

// enroll runs the whole enrolment ceremony for a and stores the passkey.
func enroll(t *testing.T, svc *passkey.Service, db *store.DB, clk *clock, a *passkeytest.Authenticator, label string) int64 {
	t.Helper()
	ctx := context.Background()
	exp := clk.Now().Add(10 * time.Minute)
	eid, err := db.CreateEnrollment(ctx, []byte("hash-"+label), clk.Now(), exp)
	if err != nil {
		t.Fatal(err)
	}
	opts, tok, err := svc.BeginEnroll(ctx, eid, exp)
	if err != nil {
		t.Fatalf("BeginEnroll: %v", err)
	}
	cred, err := a.Create(opts)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	np, err := svc.FinishEnroll(ctx, tok, cred)
	if err != nil {
		t.Fatalf("FinishEnroll: %v", err)
	}
	if np.Ceremony.EnrollmentID != eid || np.Ceremony.Purpose != passkey.PurposeEnroll {
		t.Fatalf("enrol ceremony = %+v", np.Ceremony)
	}
	id, err := db.EnrollPasskey(ctx, store.EnrollParams{
		EnrollmentID: eid, Nonce: np.Ceremony.Nonce(), NonceExpires: np.Ceremony.Expires,
		Passkey: store.Passkey{CredentialID: np.CredentialID, CredentialJSON: np.CredentialJSON, Label: label},
		At:      clk.Now(), Lane: "public",
	})
	if err != nil {
		t.Fatalf("EnrollPasskey: %v", err)
	}
	return id
}

// assert runs BeginAssert, the authenticator and FinishAssert.
func assert(t *testing.T, svc *passkey.Service, clk *clock, a *passkeytest.Authenticator, p passkey.Purpose, pid, hash string) (passkey.Assertion, error) {
	t.Helper()
	ctx := context.Background()
	opts, tok, err := svc.BeginAssert(ctx, p, pid, hash, clk.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatalf("BeginAssert: %v", err)
	}
	cred, err := a.Get(opts)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return svc.FinishAssert(ctx, tok, p, cred)
}

func TestSignerRoundTrip(t *testing.T) {
	s := passkey.NewSigner(testKey())
	c := passkey.Ceremony{
		Purpose: passkey.PurposeApprove,
		Session: webauthn.SessionData{
			Challenge: testChallenge(), RelyingPartyID: "localhost", UserID: []byte("coachd-runner"),
			AllowedCredentialIDs: [][]byte{{1, 2, 3}}, UserVerification: protocol.VerificationRequired,
		},
		ProposalID: "pid", ContentHash: "hash", Expires: t0.Add(time.Hour),
	}
	tok, err := s.Sign(c)
	if err != nil {
		t.Fatal(err)
	}
	payload, mac, ok := strings.Cut(tok, ".")
	if !ok || strings.Contains(mac, ".") || strings.ContainsAny(tok, "=+/") {
		t.Fatalf("token format: %q", tok)
	}
	if _, err := base64.RawURLEncoding.DecodeString(payload); err != nil {
		t.Fatalf("payload not base64url: %v", err)
	}
	if m, err := base64.RawURLEncoding.DecodeString(mac); err != nil || len(m) != 32 {
		t.Fatalf("mac: %d bytes, %v", len(m), err)
	}
	got, err := s.Verify(tok, passkey.PurposeApprove, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Purpose != c.Purpose || got.ProposalID != "pid" || got.ContentHash != "hash" || !got.Expires.Equal(c.Expires) {
		t.Fatalf("got %+v", got)
	}
	if !reflect.DeepEqual(got.Session, c.Session) {
		t.Fatalf("session = %+v, want %+v", got.Session, c.Session)
	}
	if got.Nonce() != c.Session.Challenge {
		t.Fatalf("Nonce = %q", got.Nonce())
	}
}

func TestSignerRejectsTampered(t *testing.T) {
	s := passkey.NewSigner(testKey())
	tok, err := s.Sign(passkey.Ceremony{Purpose: passkey.PurposeLogin,
		Session: webauthn.SessionData{Challenge: testChallenge()}, Expires: t0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	payload, mac, _ := strings.Cut(tok, ".")
	flip := func(part string, i int) string {
		b, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			t.Fatal(err)
		}
		b[i%len(b)] ^= 0x01
		return base64.RawURLEncoding.EncodeToString(b)
	}
	other, _ := passkey.NewSigner(bytes.Repeat([]byte{8}, 32)).Sign(passkey.Ceremony{Purpose: passkey.PurposeLogin,
		Session: webauthn.SessionData{Challenge: testChallenge()}, Expires: t0.Add(time.Hour)})
	cases := map[string]string{
		"payload byte":  flip(payload, 10) + "." + mac,
		"payload start": flip(payload, 0) + "." + mac,
		"mac byte":      payload + "." + flip(mac, 5),
		"mac last":      payload + "." + flip(mac, 31),
		"mac truncated": payload + "." + mac[:len(mac)-2],
		"other key":     other,
		"empty":         "",
		"no dot":        payload,
		"two dots":      tok + ".x",
		"not base64":    "!!!." + mac,
		"padded":        payload + "." + base64.URLEncoding.EncodeToString([]byte("x")),
	}
	for name, bad := range cases {
		if _, err := s.Verify(bad, passkey.PurposeLogin, t0); !invalidCeremony(err) {
			t.Errorf("%s: err = %v, want ErrCeremony and not expired", name, err)
		}
	}
	// A forged token reads as invalid, never as expired, even past its expiry.
	stale, err := passkey.NewSigner(bytes.Repeat([]byte{8}, 32)).Sign(passkey.Ceremony{Purpose: passkey.PurposeLogin,
		Session: webauthn.SessionData{Challenge: testChallenge()}, Expires: t0.Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(stale, passkey.PurposeLogin, t0); !invalidCeremony(err) {
		t.Errorf("forged and expired: err = %v, want ErrCeremony and not expired", err)
	}
	if _, err := s.Verify(tok, passkey.PurposeLogin, t0); err != nil {
		t.Fatalf("untampered: %v", err)
	}
}

func TestSignerWrongPurpose(t *testing.T) {
	s := passkey.NewSigner(testKey())
	tok, err := s.Sign(passkey.Ceremony{Purpose: passkey.PurposeLogin,
		Session: webauthn.SessionData{Challenge: testChallenge()}, Expires: t0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []passkey.Purpose{passkey.PurposeApprove, passkey.PurposeEnroll, ""} {
		if _, err := s.Verify(tok, p, t0); !invalidCeremony(err) {
			t.Errorf("%q: err = %v", p, err)
		}
		// Expired as well: the wrong purpose still makes it invalid.
		if _, err := s.Verify(tok, p, t0.Add(2*time.Hour)); !invalidCeremony(err) {
			t.Errorf("%q after expiry: err = %v", p, err)
		}
	}
}

func TestSignerExpired(t *testing.T) {
	s := passkey.NewSigner(testKey())
	exp := t0.Add(5 * time.Minute)
	tok, err := s.Sign(passkey.Ceremony{Purpose: passkey.PurposeLogin,
		Session: webauthn.SessionData{Challenge: testChallenge()}, Expires: exp})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(tok, passkey.PurposeLogin, exp.Add(-time.Nanosecond)); err != nil {
		t.Fatalf("just before expiry: %v", err)
	}
	for _, now := range []time.Time{exp, exp.Add(time.Second), exp.Add(24 * time.Hour)} {
		_, err := s.Verify(tok, passkey.PurposeLogin, now)
		if !errors.Is(err, passkey.ErrCeremonyExpired) || !errors.Is(err, passkey.ErrCeremony) {
			t.Errorf("at %v: err = %v, want ErrCeremonyExpired matching ErrCeremony", now, err)
		}
	}
	noExp, err := s.Sign(passkey.Ceremony{Purpose: passkey.PurposeLogin, Session: webauthn.SessionData{Challenge: testChallenge()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(noExp, passkey.PurposeLogin, t0); !invalidCeremony(err) {
		t.Errorf("zero expiry: err = %v", err)
	}
}

func TestSignerRequiresBinding(t *testing.T) {
	s := passkey.NewSigner(testKey())
	sess := webauthn.SessionData{Challenge: testChallenge()}
	exp := t0.Add(time.Hour)
	for name, c := range map[string]passkey.Ceremony{
		"approve without proposal": {Purpose: passkey.PurposeApprove, Session: sess, ContentHash: "h", Expires: exp},
		"approve without hash":     {Purpose: passkey.PurposeApprove, Session: sess, ProposalID: "p", Expires: exp},
		"enrol without enrolment":  {Purpose: passkey.PurposeEnroll, Session: sess, Expires: exp},
		"no challenge":             {Purpose: passkey.PurposeLogin, Expires: exp},
		"consent without request":  {Purpose: passkey.PurposeConsent, Session: sess, Scopes: []string{"coach:read"}, Expires: exp},
		"consent without scopes":   {Purpose: passkey.PurposeConsent, Session: sess, Request: "rq", Expires: exp},
		"approve all without set":  {Purpose: passkey.PurposeApproveAll, Session: sess, Expires: exp},
		"approve all, item without id": {Purpose: passkey.PurposeApproveAll, Session: sess, Expires: exp,
			Proposals: []passkey.Bound{{ID: "a", ContentHash: "h"}, {ContentHash: "h"}}},
		"approve all, item without hash": {Purpose: passkey.PurposeApproveAll, Session: sess, Expires: exp,
			Proposals: []passkey.Bound{{ID: "a"}}},
		"approve all with a single binding": {Purpose: passkey.PurposeApproveAll, Session: sess, Expires: exp,
			ProposalID: "p", ContentHash: "h", Proposals: []passkey.Bound{{ID: "a", ContentHash: "h"}}},
		"approve with a set": {Purpose: passkey.PurposeApprove, Session: sess, Expires: exp,
			ProposalID: "p", ContentHash: "h", Proposals: []passkey.Bound{{ID: "a", ContentHash: "h"}}},
	} {
		tok, err := s.Sign(c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Verify(tok, c.Purpose, t0); !invalidCeremony(err) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestSignerRejectsShortKey(t *testing.T) {
	if _, err := passkey.NewSigner([]byte("short")).Sign(passkey.Ceremony{Purpose: passkey.PurposeLogin,
		Session: webauthn.SessionData{Challenge: testChallenge()}, Expires: t0.Add(time.Hour)}); err == nil {
		t.Fatal("Sign with a 5-byte key succeeded")
	}
}

func TestLoadOrCreateKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nonce.key")
	k1, err := passkey.LoadOrCreateKey(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(k1) != 32 {
		t.Fatalf("key length = %d", len(k1))
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != hex.EncodeToString(k1)+"\n" {
		t.Fatalf("file content is not the key as 64 hex chars and a newline: %d bytes", len(raw))
	}
	k2, err := passkey.LoadOrCreateKey(p)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatalf("second call: equal=%v err=%v", bytes.Equal(k1, k2), err)
	}

	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = passkey.LoadOrCreateKey(p)
	if err == nil {
		t.Fatal("0644 key file accepted")
	}
	if strings.Contains(err.Error(), hex.EncodeToString(k1)) {
		t.Fatalf("error leaks the key: %v", err)
	}

	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("zz"+strings.Repeat("a", 62)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := passkey.LoadOrCreateKey(bad); err == nil || strings.Contains(err.Error(), "zz") {
		t.Fatalf("malformed key: err = %v", err)
	}
	short := filepath.Join(dir, "short.key")
	if err := os.WriteFile(short, []byte("abcd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := passkey.LoadOrCreateKey(short); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := passkey.LoadOrCreateKey(filepath.Join(dir, "missing", "nonce.key")); err == nil {
		t.Fatal("missing directory accepted")
	}
}

func TestEnrollThenAssert(t *testing.T) {
	svc, db, clk := setup(t)
	a := passkeytest.New(testOrigin)
	id := enroll(t, svc, db, clk, a, "phone")

	as, err := assert(t, svc, clk, a, passkey.PurposeApprove, "pid", "hash")
	if err != nil {
		t.Fatalf("FinishAssert: %v", err)
	}
	if as.PasskeyID != id {
		t.Fatalf("PasskeyID = %d, want %d", as.PasskeyID, id)
	}
	if as.Ceremony.Purpose != passkey.PurposeApprove || as.Ceremony.ProposalID != "pid" || as.Ceremony.ContentHash != "hash" {
		t.Fatalf("ceremony = %+v", as.Ceremony)
	}
	if as.Ceremony.Nonce() == "" {
		t.Fatal("empty nonce")
	}
	if !as.Ceremony.Session.Expires.IsZero() {
		t.Fatalf("SessionData.Expires = %v; expiry belongs to the signed ceremony only", as.Ceremony.Session.Expires)
	}
	var cred webauthn.Credential
	if err := json.Unmarshal(as.CredentialJSON, &cred); err != nil {
		t.Fatal(err)
	}
	if cred.Authenticator.SignCount != 1 || !cred.Flags.UserVerified || cred.Authenticator.CloneWarning {
		t.Fatalf("updated credential: sign count %d, uv %v, clone %v",
			cred.Authenticator.SignCount, cred.Flags.UserVerified, cred.Authenticator.CloneWarning)
	}
	pk, err := db.PasskeyByCredentialID(context.Background(), cred.ID)
	if err != nil || pk.ID != id {
		t.Fatalf("credential ID does not resolve to the passkey: %d %v", pk.ID, err)
	}

	// A login works as well, and the nonce differs per ceremony.
	login, err := assert(t, svc, clk, a, passkey.PurposeLogin, "", "")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if login.PasskeyID != id || login.Ceremony.Nonce() == as.Ceremony.Nonce() {
		t.Fatalf("login = %+v", login)
	}
}

func TestWebAuthnSettings(t *testing.T) {
	svc, db, clk := setup(t)
	a := passkeytest.New(testOrigin)
	enroll(t, svc, db, clk, a, "phone")
	ctx := context.Background()

	opts, _, err := svc.BeginEnroll(ctx, 99, clk.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var creation protocol.CredentialCreation
	if err := json.Unmarshal(opts, &creation); err != nil {
		t.Fatal(err)
	}
	pk := creation.Response
	if pk.RelyingParty.ID != "localhost" || pk.RelyingParty.Name != "coachd" {
		t.Errorf("rp = %+v", pk.RelyingParty)
	}
	if pk.User.Name != "runner" || pk.User.DisplayName != "Runner" {
		t.Errorf("user = %+v", pk.User)
	}
	if id, _ := pk.User.ID.(string); id != base64.RawURLEncoding.EncodeToString([]byte("coachd-runner")) {
		t.Errorf("user.id = %v", pk.User.ID)
	}
	sel := pk.AuthenticatorSelection
	if sel.UserVerification != protocol.VerificationRequired || sel.ResidentKey != protocol.ResidentKeyRequirementPreferred {
		t.Errorf("authenticatorSelection = %+v", sel)
	}
	if pk.Attestation != protocol.PreferNoAttestation {
		t.Errorf("attestation = %q", pk.Attestation)
	}

	opts, _, err = svc.BeginAssert(ctx, passkey.PurposeLogin, "", "", clk.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var assertion protocol.CredentialAssertion
	if err := json.Unmarshal(opts, &assertion); err != nil {
		t.Fatal(err)
	}
	if assertion.Response.UserVerification != protocol.VerificationRequired || assertion.Response.RelyingPartyID != "localhost" {
		t.Errorf("request options = %+v", assertion.Response)
	}
	if len(assertion.Response.AllowedCredentials) != 1 {
		t.Errorf("allowCredentials = %+v", assertion.Response.AllowedCredentials)
	}
}

func TestBeginEnrollExcludesExisting(t *testing.T) {
	svc, db, clk := setup(t)
	a := passkeytest.New(testOrigin)
	id := enroll(t, svc, db, clk, a, "phone")
	ctx := context.Background()
	pks, err := db.ActivePasskeys(ctx)
	if err != nil || len(pks) != 1 || pks[0].ID != id {
		t.Fatalf("ActivePasskeys = %+v %v", pks, err)
	}
	opts, _, err := svc.BeginEnroll(ctx, 99, clk.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var creation protocol.CredentialCreation
	if err := json.Unmarshal(opts, &creation); err != nil {
		t.Fatal(err)
	}
	ex := creation.Response.CredentialExcludeList
	if len(ex) != 1 || !bytes.Equal(ex[0].CredentialID, pks[0].CredentialID) {
		t.Fatalf("excludeCredentials = %+v", ex)
	}
	if _, err := a.Create(opts); err == nil {
		t.Fatal("authenticator re-registered an excluded credential")
	}
}

func TestEnrollRequiresUV(t *testing.T) {
	svc, db, clk := setup(t)
	ctx := context.Background()
	a := passkeytest.New(testOrigin)
	a.SetUserVerified(false)
	exp := clk.Now().Add(10 * time.Minute)
	eid, err := db.CreateEnrollment(ctx, []byte("h"), clk.Now(), exp)
	if err != nil {
		t.Fatal(err)
	}
	opts, tok, err := svc.BeginEnroll(ctx, eid, exp)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := a.Create(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FinishEnroll(ctx, tok, cred); err == nil {
		t.Fatal("FinishEnroll without user verification succeeded")
	}
}

func TestAssertRequiresUV(t *testing.T) {
	svc, db, clk := setup(t)
	a := passkeytest.New(testOrigin)
	enroll(t, svc, db, clk, a, "phone")
	a.SetUserVerified(false)
	for _, p := range []passkey.Purpose{passkey.PurposeLogin, passkey.PurposeApprove} {
		_, err := assert(t, svc, clk, a, p, "pid", "hash")
		if err == nil {
			t.Fatalf("%s without user verification succeeded", p)
		}
		if errors.Is(err, passkey.ErrCeremony) || errors.Is(err, passkey.ErrUnknownCredential) {
			t.Fatalf("%s: wrong failure: %v", p, err)
		}
	}
	a.SetUserVerified(true)
	if _, err := assert(t, svc, clk, a, passkey.PurposeLogin, "", ""); err != nil {
		t.Fatalf("with user verification: %v", err)
	}
}

func TestAssertRevokedPasskey(t *testing.T) {
	svc, db, clk := setup(t)
	ctx := context.Background()
	a := passkeytest.New(testOrigin)
	id := enroll(t, svc, db, clk, a, "phone")
	opts, tok, err := svc.BeginAssert(ctx, passkey.PurposeApprove, "pid", "hash", clk.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RevokePasskey(ctx, id, clk.Now(), "public", "web"); err != nil {
		t.Fatal(err)
	}
	cred, err := a.Get(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FinishAssert(ctx, tok, passkey.PurposeApprove, cred); !errors.Is(err, passkey.ErrUnknownCredential) {
		t.Fatalf("revoked passkey: err = %v", err)
	}
	if _, _, err := svc.BeginAssert(ctx, passkey.PurposeLogin, "", "", clk.Now().Add(5*time.Minute)); !errors.Is(err, passkey.ErrNoPasskeys) {
		t.Fatalf("BeginAssert after revoke: err = %v", err)
	}
}

func TestAssertWrongOrigin(t *testing.T) {
	db := openDB(t)
	clk := &clock{t: t0}
	svc := newService(t, db, testOrigin, clk)
	// A relying party at another origin shares the database, so the other
	// authenticator's credential is enrolled and found by ID: the failure
	// below comes from the origin and RP ID checks, not the lookup.
	const evilOrigin = "https://evil.example"
	evilSvc := newService(t, db, evilOrigin, clk)
	evil := passkeytest.New(evilOrigin)
	id := enroll(t, evilSvc, db, clk, evil, "evil")

	if as, err := assert(t, evilSvc, clk, evil, passkey.PurposeLogin, "", ""); err != nil || as.PasskeyID != id {
		t.Fatalf("control at its own origin: %+v %v", as, err)
	}
	_, err := assert(t, svc, clk, evil, passkey.PurposeApprove, "pid", "hash")
	if err == nil {
		t.Fatal("assertion from another origin succeeded")
	}
	if errors.Is(err, passkey.ErrUnknownCredential) || errors.Is(err, passkey.ErrCeremony) {
		t.Fatalf("wrong failure: %v", err)
	}
}

func TestAssertPurposeMismatch(t *testing.T) {
	svc, db, clk := setup(t)
	ctx := context.Background()
	a := passkeytest.New(testOrigin)
	enroll(t, svc, db, clk, a, "phone")
	opts, tok, err := svc.BeginAssert(ctx, passkey.PurposeLogin, "", "", clk.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	cred, err := a.Get(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FinishAssert(ctx, tok, passkey.PurposeApprove, cred); !errors.Is(err, passkey.ErrCeremony) {
		t.Fatalf("login token used for approve: err = %v", err)
	}
	if _, err := svc.FinishAssert(ctx, tok, passkey.PurposeEnroll, cred); err == nil {
		t.Fatal("FinishAssert accepted the enrol purpose")
	}
	if _, err := svc.FinishAssert(ctx, tok, passkey.PurposeLogin, cred); err != nil {
		t.Fatalf("same assertion with the right purpose: %v", err)
	}
}

func TestBeginAssertNoPasskeys(t *testing.T) {
	svc, _, clk := setup(t)
	ctx := context.Background()
	for _, p := range []passkey.Purpose{passkey.PurposeLogin, passkey.PurposeApprove} {
		if _, _, err := svc.BeginAssert(ctx, p, "pid", "hash", clk.Now().Add(time.Minute)); !errors.Is(err, passkey.ErrNoPasskeys) {
			t.Errorf("%s: err = %v", p, err)
		}
	}
}

func TestCeremonyExpiryUsesInjectedClock(t *testing.T) {
	svc, db, clk := setup(t)
	ctx := context.Background()
	a := passkeytest.New(testOrigin)
	enroll(t, svc, db, clk, a, "phone")
	opts, tok, err := svc.BeginAssert(ctx, passkey.PurposeApprove, "pid", "hash", clk.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	cred, err := a.Get(opts)
	if err != nil {
		t.Fatal(err)
	}
	clk.Add(5 * time.Minute)
	if _, err := svc.FinishAssert(ctx, tok, passkey.PurposeApprove, cred); !errors.Is(err, passkey.ErrCeremonyExpired) {
		t.Fatalf("expired approval: err = %v", err)
	}

	exp := clk.Now().Add(10 * time.Minute)
	eid, err := db.CreateEnrollment(ctx, []byte("h2"), clk.Now(), exp)
	if err != nil {
		t.Fatal(err)
	}
	b := passkeytest.New(testOrigin)
	opts, tok, err = svc.BeginEnroll(ctx, eid, exp)
	if err != nil {
		t.Fatal(err)
	}
	cred, err = b.Create(opts)
	if err != nil {
		t.Fatal(err)
	}
	clk.Add(11 * time.Minute)
	if _, err := svc.FinishEnroll(ctx, tok, cred); !errors.Is(err, passkey.ErrCeremonyExpired) {
		t.Fatalf("expired enrolment: err = %v", err)
	}
}

func TestAssertWithTwoPasskeys(t *testing.T) {
	svc, db, clk := setup(t)
	a, b := passkeytest.New(testOrigin), passkeytest.New(testOrigin)
	idA := enroll(t, svc, db, clk, a, "phone")
	idB := enroll(t, svc, db, clk, b, "laptop")
	for _, tc := range []struct {
		auth *passkeytest.Authenticator
		id   int64
	}{{a, idA}, {b, idB}} {
		as, err := assert(t, svc, clk, tc.auth, passkey.PurposeLogin, "", "")
		if err != nil {
			t.Fatalf("passkey %d: %v", tc.id, err)
		}
		if as.PasskeyID != tc.id {
			t.Fatalf("PasskeyID = %d, want %d", as.PasskeyID, tc.id)
		}
	}
}

func TestAssertCredentialNotOffered(t *testing.T) {
	svc, db, clk := setup(t)
	ctx := context.Background()
	a, b := passkeytest.New(testOrigin), passkeytest.New(testOrigin)
	enroll(t, svc, db, clk, a, "phone")
	opts, tok, err := svc.BeginAssert(ctx, passkey.PurposeApprove, "pid", "hash", clk.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	// b is enrolled only after the ceremony began, so it was not offered.
	enroll(t, svc, db, clk, b, "laptop")
	var m map[string]map[string]any
	if err := json.Unmarshal(opts, &m); err != nil {
		t.Fatal(err)
	}
	delete(m["publicKey"], "allowCredentials")
	stripped, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := b.Get(stripped)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FinishAssert(ctx, tok, passkey.PurposeApprove, cred); err == nil {
		t.Fatal("assertion by a credential not offered in the ceremony succeeded")
	}
}

func TestAssertCloneWarningRejected(t *testing.T) {
	svc, db, clk := setup(t)
	ctx := context.Background()
	a := passkeytest.New(testOrigin)
	id := enroll(t, svc, db, clk, a, "phone")
	exp := clk.Now().Add(5 * time.Minute)
	opts1, tok1, err := svc.BeginAssert(ctx, passkey.PurposeLogin, "", "", exp)
	if err != nil {
		t.Fatal(err)
	}
	opts2, tok2, err := svc.BeginAssert(ctx, passkey.PurposeLogin, "", "", exp)
	if err != nil {
		t.Fatal(err)
	}
	cred1, err := a.Get(opts1) // sign count 1
	if err != nil {
		t.Fatal(err)
	}
	cred2, err := a.Get(opts2) // sign count 2
	if err != nil {
		t.Fatal(err)
	}
	as, err := svc.FinishAssert(ctx, tok2, passkey.PurposeLogin, cred2)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UsePasskey(ctx, id, as.CredentialJSON, clk.Now()); err != nil {
		t.Fatal(err)
	}
	// The stored sign count is now 2, so an assertion carrying 1 looks cloned.
	if _, err := svc.FinishAssert(ctx, tok1, passkey.PurposeLogin, cred1); err == nil {
		t.Fatal("assertion with a regressed sign count succeeded")
	}
}

// A replayed assertion is a used nonce, not a cloned authenticator: the
// replay carries the sign count the first request already stored, so
// without the nonce pre-check go-webauthn would flag it as a clone first.
func TestAssertReplayIsNonceUsed(t *testing.T) {
	svc, db, clk := setup(t)
	ctx := context.Background()
	a := passkeytest.New(testOrigin)
	id := enroll(t, svc, db, clk, a, "phone")
	opts, tok, err := svc.BeginAssert(ctx, passkey.PurposeLogin, "", "", clk.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	cred, err := a.Get(opts)
	if err != nil {
		t.Fatal(err)
	}
	as, err := svc.FinishAssert(ctx, tok, passkey.PurposeLogin, cred)
	if err != nil {
		t.Fatal(err)
	}
	// The caller acts on the assertion: it stores the new sign count and
	// consumes the nonce.
	if err := db.UsePasskey(ctx, id, as.CredentialJSON, clk.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.ConsumeNonce(ctx, as.Ceremony.Nonce(), clk.Now(), as.Ceremony.Expires); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FinishAssert(ctx, tok, passkey.PurposeLogin, cred); !errors.Is(err, store.ErrNonceUsed) {
		t.Fatalf("replay: err = %v, want store.ErrNonceUsed", err)
	}
}

func TestFinishRejectsGarbage(t *testing.T) {
	svc, db, clk := setup(t)
	ctx := context.Background()
	a := passkeytest.New(testOrigin)
	enroll(t, svc, db, clk, a, "phone")
	_, tok, err := svc.BeginAssert(ctx, passkey.PurposeLogin, "", "", clk.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", "{}", "null", `{"id":"x"}`} {
		if _, err := svc.FinishAssert(ctx, tok, passkey.PurposeLogin, []byte(body)); err == nil {
			t.Errorf("FinishAssert(%q) succeeded", body)
		}
	}
	if _, err := svc.FinishEnroll(ctx, tok, []byte("{}")); !errors.Is(err, passkey.ErrCeremony) {
		t.Errorf("login token for enrolment: err = %v", err)
	}
}

// A store failure inside FinishAssert is ErrInternal (an outage, which the
// caller answers with 500), never a failed check; a failed check never is.
func TestFinishAssertInfrastructureErrorsAreInternal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coachd.db")
	db := openDBAt(t, path)
	clk := &clock{t: t0}
	svc := newService(t, db, testOrigin, clk)
	ctx := context.Background()
	a := passkeytest.New(testOrigin)
	enroll(t, svc, db, clk, a, "phone")

	type pending struct {
		tok  string
		cred []byte
	}
	begin := func() pending {
		t.Helper()
		opts, tok, err := svc.BeginAssert(ctx, passkey.PurposeLogin, "", "", clk.Now().Add(5*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		cred, err := a.Get(opts)
		if err != nil {
			t.Fatal(err)
		}
		return pending{tok, cred}
	}
	garbage, decode, lookup, nonce := begin(), begin(), begin(), begin()

	// Failed checks are not internal.
	if _, err := svc.FinishAssert(ctx, garbage.tok, passkey.PurposeLogin, []byte(`{"id":"x"}`)); err == nil || errors.Is(err, passkey.ErrInternal) {
		t.Fatalf("garbage credential: err = %v", err)
	}
	opts, otok, err := svc.BeginAssert(ctx, passkey.PurposeLogin, "", "", clk.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]map[string]any
	if err := json.Unmarshal(opts, &m); err != nil {
		t.Fatal(err)
	}
	delete(m["publicKey"], "allowCredentials") // so the unenrolled authenticator answers
	stripped, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	// other is enrolled with another database only, so this one does not know it.
	otherDB, other := openDB(t), passkeytest.New(testOrigin)
	enroll(t, newService(t, otherDB, testOrigin, clk), otherDB, clk, other, "elsewhere")
	ocred, err := other.Get(stripped)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FinishAssert(ctx, otok, passkey.PurposeLogin, ocred); !errors.Is(err, passkey.ErrUnknownCredential) || errors.Is(err, passkey.ErrInternal) {
		t.Fatalf("unknown credential: err = %v", err)
	}
	if _, err := svc.FinishAssert(ctx, "x.y", passkey.PurposeLogin, garbage.cred); errors.Is(err, passkey.ErrInternal) {
		t.Fatalf("forged token: err = %v", err)
	}

	// Store failures are.
	rawExec(t, path, `UPDATE passkeys SET credential_json='not json'`)
	if _, err := svc.FinishAssert(ctx, decode.tok, passkey.PurposeLogin, decode.cred); !errors.Is(err, passkey.ErrInternal) {
		t.Fatalf("undecodable stored credential: err = %v, want ErrInternal", err)
	}
	rawExec(t, path, `ALTER TABLE passkeys RENAME TO passkeys_gone`)
	if _, err := svc.FinishAssert(ctx, lookup.tok, passkey.PurposeLogin, lookup.cred); !errors.Is(err, passkey.ErrInternal) {
		t.Fatalf("passkey lookup failure: err = %v, want ErrInternal", err)
	}
	rawExec(t, path, `ALTER TABLE used_nonces RENAME TO used_nonces_gone`)
	if _, err := svc.FinishAssert(ctx, nonce.tok, passkey.PurposeLogin, nonce.cred); !errors.Is(err, passkey.ErrInternal) {
		t.Fatalf("nonce lookup failure: err = %v, want ErrInternal", err)
	}
}

func TestNewRejectsBadPublicURL(t *testing.T) {
	db := openDB(t)
	for _, u := range []string{"", "localhost", "ftp://x.example", "https://", "://bad"} {
		if _, err := passkey.New(u, passkey.NewSigner(testKey()), db, time.Now); err == nil {
			t.Errorf("New(%q) succeeded", u)
		}
	}
}

func TestConsentCeremony(t *testing.T) {
	svc, db, clk := setup(t)
	ctx := context.Background()
	exp := clk.Now().Add(10 * time.Minute)
	if _, _, err := svc.BeginConsent(ctx, "rq", []string{"coach:read"}, exp); !errors.Is(err, passkey.ErrNoPasskeys) {
		t.Fatalf("no passkey: err = %v", err)
	}
	a := passkeytest.New(testOrigin)
	id := enroll(t, svc, db, clk, a, "phone")

	scopes := []string{"coach:read", "coach:write"}
	opts, tok, err := svc.BeginConsent(ctx, "signed.request", scopes, exp)
	if err != nil {
		t.Fatalf("BeginConsent: %v", err)
	}
	cred, err := a.Get(opts)
	if err != nil {
		t.Fatal(err)
	}
	// A consent token is for consent only.
	for _, p := range []passkey.Purpose{passkey.PurposeLogin, passkey.PurposeApprove} {
		if _, err := svc.FinishAssert(ctx, tok, p, cred); !errors.Is(err, passkey.ErrCeremony) {
			t.Errorf("FinishAssert(%s) with a consent token: err = %v", p, err)
		}
	}
	as, err := svc.FinishAssert(ctx, tok, passkey.PurposeConsent, cred)
	if err != nil {
		t.Fatalf("FinishAssert(consent): %v", err)
	}
	c := as.Ceremony
	if as.PasskeyID != id || c.Purpose != passkey.PurposeConsent || c.Request != "signed.request" ||
		!reflect.DeepEqual(c.Scopes, scopes) || !c.Expires.Equal(exp) || c.Nonce() == "" {
		t.Fatalf("assertion = %+v", as)
	}

	// A login token is not a consent token either.
	lopts, ltok, err := svc.BeginAssert(ctx, passkey.PurposeLogin, "", "", exp)
	if err != nil {
		t.Fatal(err)
	}
	lcred, err := a.Get(lopts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FinishAssert(ctx, ltok, passkey.PurposeConsent, lcred); !errors.Is(err, passkey.ErrCeremony) {
		t.Errorf("FinishAssert(consent) with a login token: err = %v", err)
	}

	for name, tc := range map[string]struct {
		request string
		scopes  []string
	}{"no request": {"", scopes}, "nil scopes": {"rq", nil}, "empty scopes": {"rq", []string{}}} {
		if _, _, err := svc.BeginConsent(ctx, tc.request, tc.scopes, exp); err == nil {
			t.Errorf("%s: BeginConsent succeeded", name)
		}
	}
	if _, _, err := svc.BeginAssert(ctx, passkey.PurposeConsent, "", "", exp); err == nil {
		t.Error("BeginAssert(consent) succeeded; consent begins with BeginConsent")
	}
}

func TestApproveAllCeremony(t *testing.T) {
	svc, db, clk := setup(t)
	ctx := context.Background()
	exp := clk.Now().Add(10 * time.Minute)
	set := []passkey.Bound{{ID: "p1", ContentHash: "h1"}, {ID: "p2", ContentHash: "h2"}}
	if _, _, err := svc.BeginApproveAll(ctx, set, exp); !errors.Is(err, passkey.ErrNoPasskeys) {
		t.Fatalf("no passkey: err = %v", err)
	}
	a := passkeytest.New(testOrigin)
	id := enroll(t, svc, db, clk, a, "phone")

	opts, tok, err := svc.BeginApproveAll(ctx, set, exp)
	if err != nil {
		t.Fatalf("BeginApproveAll: %v", err)
	}
	cred, err := a.Get(opts)
	if err != nil {
		t.Fatal(err)
	}
	// An approve-all token is for approve-all only.
	for _, p := range []passkey.Purpose{passkey.PurposeLogin, passkey.PurposeApprove, passkey.PurposeConsent} {
		if _, err := svc.FinishAssert(ctx, tok, p, cred); !errors.Is(err, passkey.ErrCeremony) {
			t.Errorf("FinishAssert(%s) with an approve-all token: err = %v", p, err)
		}
	}
	as, err := svc.FinishAssert(ctx, tok, passkey.PurposeApproveAll, cred)
	if err != nil {
		t.Fatalf("FinishAssert(approve_all): %v", err)
	}
	c := as.Ceremony
	if as.PasskeyID != id || c.Purpose != passkey.PurposeApproveAll || !reflect.DeepEqual(c.Proposals, set) ||
		c.ProposalID != "" || !c.Expires.Equal(exp) || c.Nonce() == "" {
		t.Fatalf("assertion = %+v", as)
	}

	// A single approval token is not an approve-all token.
	sopts, stok, err := svc.BeginAssert(ctx, passkey.PurposeApprove, "p1", "h1", exp)
	if err != nil {
		t.Fatal(err)
	}
	scred, err := a.Get(sopts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FinishAssert(ctx, stok, passkey.PurposeApproveAll, scred); !errors.Is(err, passkey.ErrCeremony) {
		t.Errorf("FinishAssert(approve_all) with an approve token: err = %v", err)
	}

	for name, bad := range map[string][]passkey.Bound{
		"empty": {}, "nil": nil, "no id": {{ContentHash: "h"}}, "no hash": {{ID: "p"}},
	} {
		if _, _, err := svc.BeginApproveAll(ctx, bad, exp); err == nil {
			t.Errorf("%s: BeginApproveAll succeeded", name)
		}
	}
	if _, _, err := svc.BeginAssert(ctx, passkey.PurposeApproveAll, "p1", "h1", exp); err == nil {
		t.Error("BeginAssert(approve_all) succeeded; it begins with BeginApproveAll")
	}
}
