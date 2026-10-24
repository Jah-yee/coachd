package oauth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/stefiix92/coachd/internal/store"
)

var tNow = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

type fetchStub struct {
	calls int
	fail  bool
	ttl   time.Duration
}

func (f *fetchStub) fetch(_ context.Context, clientID, _ string) (document, time.Duration, error) {
	f.calls++
	if f.fail {
		return document{}, 0, errFetch
	}
	return document{ClientID: clientID, Name: "App\u202e", RedirectURIs: []string{"http://localhost/cb"}, Raw: []byte(`{"x":1}`)}, f.ttl, nil
}

func newTestResolver(t *testing.T, db *store.DB) (*resolver, *fetchStub, *time.Time) {
	t.Helper()
	if db == nil {
		var err error
		if db, err = store.Open(context.Background(), filepath.Join(t.TempDir(), "c.db")); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
	}
	now := tNow
	f := &fetchStub{ttl: time.Hour}
	r := newResolver(db, testSelf, deriveKey(make([]byte, 32), labelDCR))
	r.now = func() time.Time { return now }
	r.fetchDoc = f.fetch
	return r, f, &now
}

func TestResolveCIMDFetchesAndCaches(t *testing.T) {
	r, f, now := newTestResolver(t, nil)
	ctx := context.Background()
	c, err := r.resolve(ctx, testClientID)
	if err != nil || f.calls != 1 {
		t.Fatalf("first: %v calls=%d", err, f.calls)
	}
	if c.Kind != "cimd" || c.RowID == 0 || c.Host != "app.example" || c.Name != "App" {
		t.Fatalf("client = %+v", c)
	}
	if _, err := r.resolve(ctx, testClientID); err != nil || f.calls != 1 {
		t.Fatalf("second: %v calls=%d", err, f.calls)
	}
	*now = now.Add(time.Hour + time.Second)
	if _, err := r.resolve(ctx, testClientID); err != nil || f.calls != 2 {
		t.Fatalf("third: %v calls=%d", err, f.calls)
	}
}

func TestResolveNegativeCacheInMemory(t *testing.T) {
	r, f, now := newTestResolver(t, nil)
	f.fail = true
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := r.resolve(ctx, testClientID); !errors.Is(err, errUnknownClient) {
			t.Fatalf("err = %v", err)
		}
	}
	if f.calls != 1 {
		t.Fatalf("calls = %d", f.calls)
	}
	if _, err := r.db.OAuthClientByClientID(ctx, testClientID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row stored: %v", err)
	}
	r2 := newResolver(r.db, testSelf, r.dcrKey)
	r2.now = func() time.Time { return *now }
	f2 := &fetchStub{fail: true}
	r2.fetchDoc = f2.fetch
	r2.resolve(ctx, testClientID)
	if f2.calls != 1 {
		t.Fatalf("new resolver calls = %d", f2.calls)
	}
	*now = now.Add(time.Minute)
	r.resolve(ctx, testClientID)
	if f.calls != 2 {
		t.Fatalf("after expiry calls = %d", f.calls)
	}
}

func TestResolveBudgetRefusalNotCached(t *testing.T) {
	r, f, _ := newTestResolver(t, nil)
	ctx := context.Background()
	good := r.budget
	r.budget = rate.NewLimiter(0, 0)
	if _, err := r.resolve(ctx, testClientID); !errors.Is(err, errUnknownClient) || f.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
	if len(r.negative) != 0 {
		t.Fatal("negative entry recorded")
	}
	r.budget = good
	if _, err := r.resolve(ctx, testClientID); err != nil || f.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
}

func liveGrant(t *testing.T, db *store.DB, clientRow int64) {
	t.Helper()
	ctx := context.Background()
	eid, err := db.CreateEnrollment(ctx, []byte("enr"), tNow, tNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	pk, err := db.EnrollPasskey(ctx, store.EnrollParams{EnrollmentID: eid, Nonce: "n0", NonceExpires: tNow.Add(time.Hour),
		Passkey: store.Passkey{CredentialID: []byte("cred"), CredentialJSON: []byte(`{}`), Label: "p"}, At: tNow, Lane: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.GrantConsent(ctx, store.ConsentGrant{Client: store.OAuthClient{ID: clientRow}, PasskeyID: pk, CredentialJSON: []byte(`{}`),
		Nonce: "n1", NonceExpires: tNow.Add(time.Hour), Scopes: []string{"coach:read"}, Resource: "https://x/mcp", CodeHash: []byte("code"),
		RedirectURI: "http://localhost/cb", RedirectExplicit: true, CodeChallenge: "c", CodeExpires: tNow.Add(time.Hour), At: tNow}); err != nil {
		t.Fatal(err)
	}
	_, err = db.RedeemCode(ctx, []byte("code"), tNow, func(store.OAuthCode) error { return nil },
		store.NewTokens{AccessHash: []byte("a"), RefreshHash: []byte("r"), AccessExpires: tNow.Add(24 * time.Hour), RefreshExpires: tNow.Add(30 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestResolveStaleIfErrorWithLiveGrant(t *testing.T) {
	r, f, now := newTestResolver(t, nil)
	ctx := context.Background()
	c, err := r.resolve(ctx, testClientID)
	if err != nil {
		t.Fatal(err)
	}
	liveGrant(t, r.db, c.RowID)
	*now = now.Add(2 * time.Hour)
	f.fail = true
	got, err := r.resolve(ctx, testClientID)
	if err != nil || len(got.RedirectURIs) != 1 || got.RedirectURIs[0] != "http://localhost/cb" || got.RowID != c.RowID {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestResolveStaleWithoutGrantRefused(t *testing.T) {
	r, f, now := newTestResolver(t, nil)
	ctx := context.Background()
	if _, err := r.resolve(ctx, testClientID); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Hour)
	f.fail = true
	if _, err := r.resolve(ctx, testClientID); !errors.Is(err, errUnknownClient) {
		t.Fatalf("err = %v", err)
	}
}

func TestStoredNeverFetches(t *testing.T) {
	r, f, _ := newTestResolver(t, nil)
	ctx := context.Background()
	if _, err := r.stored(ctx, testClientID); !errors.Is(err, errUnknownClient) || f.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
	if _, err := r.resolve(ctx, testClientID); err != nil {
		t.Fatal(err)
	}
	if c, err := r.stored(ctx, testClientID); err != nil || c.Host != "app.example" || f.calls != 1 {
		t.Fatalf("stored: %+v %v calls=%d", c, err, f.calls)
	}
}

func TestResolveDCRRowIDAfterConsent(t *testing.T) {
	r, _, _ := newTestResolver(t, nil)
	ctx := context.Background()
	id, _, err := registerDCR(r.dcrKey, registration{RedirectURIs: []string{"http://localhost/cb"}, ClientName: "Cli"}, testSelf, tNow)
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.resolve(ctx, id)
	if err != nil || c.RowID != 0 || c.Host != "" || c.Kind != "dcr" {
		t.Fatalf("%+v %v", c, err)
	}
	rowID, err := r.db.PutOAuthClient(ctx, store.OAuthClient{ClientID: id, Kind: "dcr", Name: "Cli", RedirectURIs: c.RedirectURIs, CreatedAt: tNow})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ = r.stored(ctx, id); c.RowID != rowID {
		t.Fatalf("RowID = %d want %d", c.RowID, rowID)
	}
	if _, err := r.resolve(ctx, id[:len(id)-2]+"AA"); err == nil || strings.Contains(err.Error(), "dcr_") {
		t.Fatalf("tampered: %v", err)
	}
}
