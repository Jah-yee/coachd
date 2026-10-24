package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func oauthPasskey(t *testing.T, db *DB) int64 {
	t.Helper()
	res, err := db.sql.Exec(`INSERT INTO passkeys (credential_id, credential_json, label, created_at) VALUES (?,?,?,?)`,
		[]byte("cred"), `{"v":1}`, "phone", fmtTime(t0))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func testClient(id string) OAuthClient {
	return OAuthClient{ClientID: id, Kind: "cimd", Name: "App", RedirectURIs: []string{"http://localhost/callback"},
		Metadata: []byte(`{"a":1}`), FetchedAt: t0, CacheUntil: t0.Add(time.Hour), CreatedAt: t0}
}

type consentOpts struct {
	nonce, code string
	scopes      []string
	at          time.Time
}

// consent stores client c, a passkey and one consent, returning the grant id.
func consent(t *testing.T, db *DB, pk, clientRow int64, o consentOpts) int64 {
	t.Helper()
	if o.scopes == nil {
		o.scopes = []string{"coach:read", "coach:write"}
	}
	if o.at.IsZero() {
		o.at = t0
	}
	id, err := db.GrantConsent(context.Background(), ConsentGrant{Client: OAuthClient{ID: clientRow}, PasskeyID: pk,
		CredentialJSON: []byte(`{"v":2}`), Nonce: o.nonce, NonceExpires: o.at.Add(10 * time.Minute), Scopes: o.scopes,
		Resource: "https://x/mcp", CodeHash: []byte(o.code), RedirectURI: "http://localhost:1/callback", RedirectExplicit: true,
		CodeChallenge: "chal", CodeExpires: o.at.Add(5 * time.Minute), At: o.at})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func accept(OAuthCode) error { return nil }

func nt(a, r string, at time.Time) NewTokens {
	return NewTokens{AccessHash: []byte(a), RefreshHash: []byte(r), AccessExpires: at.Add(time.Hour), RefreshExpires: at.Add(30 * 24 * time.Hour)}
}

// oauthFixture: client, passkey, one consented grant with code "code1".
func oauthFixture(t *testing.T) (*DB, int64, int64) {
	t.Helper()
	db := openTest(t)
	cid, err := db.PutOAuthClient(context.Background(), testClient("https://a.example/c"))
	if err != nil {
		t.Fatal(err)
	}
	pk := oauthPasskey(t, db)
	return db, consent(t, db, pk, cid, consentOpts{nonce: "n1", code: "code1"}), cid
}

func redeem(t *testing.T, db *DB, code string, at time.Time) Redeemed {
	t.Helper()
	r, err := db.RedeemCode(context.Background(), []byte(code), at, func(OAuthCode) error { return nil }, nt("acc1", "ref1", at))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPutOAuthClientUpserts(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	id, err := db.PutOAuthClient(ctx, testClient("https://a.example/c"))
	if err != nil {
		t.Fatal(err)
	}
	c2 := testClient("https://a.example/c")
	c2.Name, c2.RedirectURIs, c2.Metadata, c2.CacheUntil = "New", []string{"https://a.example/cb"}, []byte(`{"b":2}`), t0.Add(2*time.Hour)
	id2, err := db.PutOAuthClient(ctx, c2)
	if err != nil || id2 != id {
		t.Fatalf("id %d vs %d err=%v", id2, id, err)
	}
	got, err := db.OAuthClientByClientID(ctx, "https://a.example/c")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.Name != "New" || !reflect.DeepEqual(got.RedirectURIs, c2.RedirectURIs) ||
		string(got.Metadata) != `{"b":2}` || !got.CacheUntil.Equal(c2.CacheUntil) || got.Kind != "cimd" {
		t.Fatalf("got %+v", got)
	}
	if _, err := db.OAuthClientByClientID(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestClientHasLiveGrant(t *testing.T) {
	db, gid, cid := oauthFixture(t)
	ctx := context.Background()
	if ok, err := db.ClientHasLiveGrant(ctx, cid, t0); err != nil || ok {
		t.Fatalf("not activated: %v %v", ok, err)
	}
	redeem(t, db, "code1", t0)
	if ok, err := db.ClientHasLiveGrant(ctx, cid, t0.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("live: %v %v", ok, err)
	}
	if ok, _ := db.ClientHasLiveGrant(ctx, cid, t0.Add(31*24*time.Hour)); ok {
		t.Fatal("expired tokens must not count")
	}
	if err := db.RevokeGrant(ctx, gid, t0, "runner"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.ClientHasLiveGrant(ctx, cid, t0.Add(time.Minute)); ok {
		t.Fatal("revoked must not count")
	}
}

func TestGrantConsentAtomic(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	cid, _ := db.PutOAuthClient(ctx, testClient("https://a.example/c"))
	pk := oauthPasskey(t, db)
	gid := consent(t, db, pk, cid, consentOpts{nonce: "n1", code: "c1"})
	count := func(q string) int {
		var n int
		if err := db.sql.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	var activated, pkUsed *string
	if err := db.sql.QueryRow(`SELECT activated_at FROM oauth_grants WHERE id=?`, gid).Scan(&activated); err != nil || activated != nil {
		t.Fatalf("activated %v %v", activated, err)
	}
	if err := db.sql.QueryRow(`SELECT last_used_at FROM passkeys WHERE id=?`, pk).Scan(&pkUsed); err != nil || pkUsed == nil {
		t.Fatalf("passkey last_used %v %v", pkUsed, err)
	}
	var pkid int64
	db.sql.QueryRow(`SELECT passkey_id FROM oauth_grants WHERE id=?`, gid).Scan(&pkid)
	if pkid != pk || count(`SELECT count(*) FROM oauth_codes WHERE used_at IS NULL`) != 1 || count(`SELECT count(*) FROM used_nonces WHERE nonce='n1'`) != 1 {
		t.Fatal("rows missing")
	}
	var lane, actor, event, target, outcome string
	if err := db.sql.QueryRow(`SELECT lane, actor, event, target, outcome FROM audit_log WHERE event='oauth_consent'`).Scan(&lane, &actor, &event, &target, &outcome); err != nil ||
		lane != "public" || actor != "runner" || target != grantTarget(gid) || outcome != "approved" {
		t.Fatalf("audit %q %q %q %q %v", lane, actor, target, outcome, err)
	}

	// nonce replay
	_, err := db.GrantConsent(ctx, ConsentGrant{Client: OAuthClient{ID: cid}, PasskeyID: pk, CredentialJSON: []byte("{}"), Nonce: "n1",
		NonceExpires: t0.Add(time.Hour), Scopes: []string{"coach:read"}, CodeHash: []byte("c2"), CodeExpires: t0.Add(time.Hour), At: t0})
	if !errors.Is(err, ErrNonceUsed) {
		t.Fatalf("replay err = %v", err)
	}
	if count(`SELECT count(*) FROM oauth_grants`) != 1 || count(`SELECT count(*) FROM oauth_codes`) != 1 {
		t.Fatal("replay left rows")
	}

	// revoked passkey
	if err := db.RevokePasskey(ctx, pk, t0, "public", "x"); err != nil {
		t.Fatal(err)
	}
	_, err = db.GrantConsent(ctx, ConsentGrant{Client: OAuthClient{ID: cid}, PasskeyID: pk, CredentialJSON: []byte("{}"), Nonce: "n2",
		NonceExpires: t0.Add(time.Hour), Scopes: []string{"coach:read"}, CodeHash: []byte("c3"), CodeExpires: t0.Add(time.Hour), At: t0})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked passkey err = %v", err)
	}
	if count(`SELECT count(*) FROM oauth_grants`) != 1 || count(`SELECT count(*) FROM used_nonces WHERE nonce='n2'`) != 0 {
		t.Fatal("revoked passkey left rows")
	}
}

func TestGrantConsentInsertsClient(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	pk := oauthPasskey(t, db)
	c := testClient("dcr-1")
	c.Kind = "dcr"
	gid, err := db.GrantConsent(ctx, ConsentGrant{Client: c, PasskeyID: pk, CredentialJSON: []byte("{}"), Nonce: "n", NonceExpires: t0.Add(time.Hour),
		Scopes: []string{"coach:read"}, CodeHash: []byte("c"), CodeExpires: t0.Add(time.Hour), At: t0})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.OAuthClientByClientID(ctx, "dcr-1")
	if err != nil || got.Kind != "dcr" || got.ID == 0 {
		t.Fatalf("client %+v %v", got, err)
	}
	var row int64
	db.sql.QueryRow(`SELECT client_row_id FROM oauth_grants WHERE id=?`, gid).Scan(&row)
	if row != got.ID {
		t.Fatal("grant not tied to inserted client")
	}
}

func TestRedeemCode(t *testing.T) {
	db, gid, _ := oauthFixture(t)
	ctx := context.Background()
	at := t0.Add(time.Minute)

	boom := errors.New("boom")
	if _, err := db.RedeemCode(ctx, []byte("code1"), at, func(OAuthCode) error { return boom }, nt("a", "r", at)); !errors.Is(err, boom) {
		t.Fatalf("check err = %v", err)
	}
	var seen OAuthCode
	r, err := db.RedeemCode(ctx, []byte("code1"), at, func(c OAuthCode) error { seen = c; return nil }, nt("acc1", "ref1", at))
	if err != nil || !r.FirstUse || r.Grant.ID != gid || r.Grant.ActivatedAt == nil {
		t.Fatalf("redeem %+v %v", r, err)
	}
	if seen.ClientID != "https://a.example/c" || seen.RedirectURI != "http://localhost:1/callback" || !seen.RedirectExplicit || seen.CodeChallenge != "chal" || seen.Used {
		t.Fatalf("code %+v", seen)
	}
	rows, err := db.sql.Query(`SELECT kind, scopes FROM oauth_tokens ORDER BY kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var k, s string
		rows.Scan(&k, &s)
		if s != "coach:read coach:write" {
			t.Fatalf("scopes %q", s)
		}
		kinds = append(kinds, k)
	}
	if !reflect.DeepEqual(kinds, []string{"access", "refresh"}) {
		t.Fatalf("kinds %v", kinds)
	}

	// expired
	cid, _ := db.OAuthClientByClientID(ctx, "https://a.example/c")
	consent(t, db, 1, cid.ID, consentOpts{nonce: "n2", code: "code2"})
	if _, err := db.RedeemCode(ctx, []byte("code2"), t0.Add(6*time.Minute), accept, nt("a2", "r2", t0)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired err = %v", err)
	}
	if _, err := db.RedeemCode(ctx, []byte("zzz"), at, accept, nt("a3", "r3", t0)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown err = %v", err)
	}
	// revoked grant
	consent(t, db, 1, cid.ID, consentOpts{nonce: "n3", code: "code3"})
	var g3 int64
	db.sql.QueryRow(`SELECT grant_id FROM oauth_codes WHERE hash=?`, []byte("code3")).Scan(&g3)
	if err := db.RevokeGrant(ctx, g3, at, "runner"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RedeemCode(ctx, []byte("code3"), at, accept, nt("a4", "r4", t0)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked err = %v", err)
	}
}

func TestRedeemCodeReuse(t *testing.T) {
	db, gid, _ := oauthFixture(t)
	ctx := context.Background()
	at := t0.Add(time.Minute)
	redeem(t, db, "code1", at)

	// mismatching replay: not found, grant stays
	_, err := db.RedeemCode(ctx, []byte("code1"), at, func(OAuthCode) error { return errors.New("mismatch") }, nt("x", "y", at))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("mismatch err = %v", err)
	}
	if _, _, err := db.AccessGrant(ctx, []byte("acc1"), at); err != nil {
		t.Fatalf("grant should stay: %v", err)
	}
	if countAudit(t, db, "oauth_code_reuse") != 0 {
		t.Fatal("audit on mismatch")
	}

	_, err = db.RedeemCode(ctx, []byte("code1"), at, func(OAuthCode) error { return nil }, nt("x", "y", at))
	if !errors.Is(err, ErrCodeReused) {
		t.Fatalf("reuse err = %v", err)
	}
	if _, _, err := db.AccessGrant(ctx, []byte("acc1"), at); !errors.Is(err, ErrRevoked) {
		t.Fatalf("access after reuse = %v", err)
	}
	var target, actor, outcome string
	if err := db.sql.QueryRow(`SELECT target, actor, outcome FROM audit_log WHERE event='oauth_code_reuse'`).Scan(&target, &actor, &outcome); err != nil ||
		target != grantTarget(gid) || actor != "client" || outcome != "revoked" {
		t.Fatalf("audit %q %q %q %v", target, actor, outcome, err)
	}
}

func TestRotateRefresh(t *testing.T) {
	db, _, _ := oauthFixture(t)
	ctx := context.Background()
	redeem(t, db, "code1", t0)
	at := t0.Add(time.Minute)
	if _, err := db.RotateRefresh(ctx, []byte("ref1"), "https://other", at, time.Minute, nil, nt("a2", "r2", at)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong client err = %v", err)
	}
	r, err := db.RotateRefresh(ctx, []byte("ref1"), "https://a.example/c", at, time.Minute, []string{"coach:read"}, nt("a2", "r2", at))
	if err != nil || r.Sibling || !reflect.DeepEqual(r.Scopes, []string{"coach:read"}) {
		t.Fatalf("rotate %+v %v", r, err)
	}
	var used *string
	db.sql.QueryRow(`SELECT used_at FROM oauth_tokens WHERE hash=?`, []byte("ref1")).Scan(&used)
	if used == nil {
		t.Fatal("used_at not set")
	}
	if _, sc, err := db.AccessGrant(ctx, []byte("a2"), at); err != nil || !reflect.DeepEqual(sc, []string{"coach:read"}) {
		t.Fatalf("narrowed access %v %v", sc, err)
	}
	r, err = db.RotateRefresh(ctx, []byte("r2"), "https://a.example/c", at.Add(time.Hour), time.Minute, nil, nt("a3", "r3", at))
	if err != nil || !reflect.DeepEqual(r.Scopes, []string{"coach:read"}) {
		t.Fatalf("keep narrowed %+v %v", r, err)
	}
	if _, err := db.RotateRefresh(ctx, []byte("r3"), "https://a.example/c", at.Add(time.Hour), time.Minute, []string{"coach:write"}, nt("a4", "r4", at)); !errors.Is(err, ErrScope) {
		t.Fatalf("scope err = %v", err)
	}
}

func TestRotateRefreshGrace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		after   time.Duration
		sibling bool
	}{{"within", 59 * time.Second, true}, {"after", 61 * time.Second, false}} {
		t.Run(tc.name, func(t *testing.T) {
			db, gid, _ := oauthFixture(t)
			ctx := context.Background()
			redeem(t, db, "code1", t0)
			first := t0.Add(time.Minute)
			if _, err := db.RotateRefresh(ctx, []byte("ref1"), "https://a.example/c", first, time.Minute, nil, nt("a2", "r2", first)); err != nil {
				t.Fatal(err)
			}
			at := first.Add(tc.after)
			r, err := db.RotateRefresh(ctx, []byte("ref1"), "https://a.example/c", at, time.Minute, nil, nt("a3", "r3", at))
			if tc.sibling {
				if err != nil || !r.Sibling {
					t.Fatalf("sibling %+v %v", r, err)
				}
				if _, _, err := db.AccessGrant(ctx, []byte("a3"), at); err != nil {
					t.Fatalf("sibling access: %v", err)
				}
				if _, err := db.RotateRefresh(ctx, []byte("r3"), "https://a.example/c", at, time.Minute, nil, nt("a4", "r4", at)); err != nil {
					t.Fatalf("sibling refresh single use: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrTokenReused) {
				t.Fatalf("err = %v", err)
			}
			if _, _, err := db.AccessGrant(ctx, []byte("a2"), at); !errors.Is(err, ErrRevoked) {
				t.Fatalf("access after reuse = %v", err)
			}
			var target, actor, outcome string
			if err := db.sql.QueryRow(`SELECT target, actor, outcome FROM audit_log WHERE event='oauth_refresh_reuse'`).Scan(&target, &actor, &outcome); err != nil ||
				target != grantTarget(gid) || actor != "client" || outcome != "revoked" {
				t.Fatalf("audit %q %q %q %v", target, actor, outcome, err)
			}
		})
	}
}

func TestAccessGrant(t *testing.T) {
	db, gid, _ := oauthFixture(t)
	ctx := context.Background()
	redeem(t, db, "code1", t0)
	at := t0.Add(time.Minute)
	g, sc, err := db.AccessGrant(ctx, []byte("acc1"), at)
	if err != nil || g.ID != gid || g.ClientID != "https://a.example/c" || !reflect.DeepEqual(sc, []string{"coach:read", "coach:write"}) {
		t.Fatalf("valid %+v %v %v", g, sc, err)
	}
	if _, _, err := db.AccessGrant(ctx, []byte("ref1"), at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refresh hash err = %v", err)
	}
	if _, _, err := db.AccessGrant(ctx, []byte("acc1"), t0.Add(2*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired err = %v", err)
	}
	lastUsed := func() string {
		var s *string
		db.sql.QueryRow(`SELECT last_used_at FROM oauth_grants WHERE id=?`, gid).Scan(&s)
		if s == nil {
			return ""
		}
		return *s
	}
	if lastUsed() != fmtTime(at) {
		t.Fatalf("last_used null case: %q", lastUsed())
	}
	db.AccessGrant(ctx, []byte("acc1"), at.Add(30*time.Second))
	if lastUsed() != fmtTime(at) {
		t.Fatalf("throttle broken: %q", lastUsed())
	}
	later := at.Add(2 * time.Minute)
	db.AccessGrant(ctx, []byte("acc1"), later)
	if lastUsed() != fmtTime(later) {
		t.Fatalf("update broken: %q", lastUsed())
	}
	if err := db.RevokeGrant(ctx, gid, later, "runner"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AccessGrant(ctx, []byte("acc1"), later); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked err = %v", err)
	}
}

func TestRevokeGrant(t *testing.T) {
	db, gid, _ := oauthFixture(t)
	ctx := context.Background()
	if err := db.RevokeGrant(ctx, gid, t0, "runner"); err != nil {
		t.Fatal(err)
	}
	var target, actor string
	if err := db.sql.QueryRow(`SELECT target, actor FROM audit_log WHERE event='oauth_grant_revoked'`).Scan(&target, &actor); err != nil ||
		target != grantTarget(gid) || actor != "runner" {
		t.Fatalf("audit %q %q %v", target, actor, err)
	}
	if err := db.RevokeGrant(ctx, gid, t0, "runner"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke = %v", err)
	}
}

func TestRevokeGrantByToken(t *testing.T) {
	db, _, _ := oauthFixture(t)
	ctx := context.Background()
	redeem(t, db, "code1", t0)
	if err := db.RevokeGrantByToken(ctx, []byte("acc1"), "https://other", t0); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeGrantByToken(ctx, []byte("unknown"), "https://a.example/c", t0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AccessGrant(ctx, []byte("acc1"), t0); err != nil {
		t.Fatalf("must not be revoked: %v", err)
	}
	if err := db.RevokeGrantByToken(ctx, []byte("acc1"), "https://a.example/c", t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err) // expired token: no-op
	}
	if _, _, err := db.AccessGrant(ctx, []byte("ref1"), t0); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := db.RevokeGrantByToken(ctx, []byte("ref1"), "https://a.example/c", t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err) // refresh token still valid at +2h, matching client
	}
	if _, _, err := db.AccessGrant(ctx, []byte("acc1"), t0); !errors.Is(err, ErrRevoked) {
		t.Fatalf("want revoked: %v", err)
	}
	if err := db.RevokeGrantByToken(ctx, []byte("ref1"), "https://a.example/c", t0); err != nil {
		t.Fatalf("repeat revoke: %v", err)
	}
}

func TestConnectedApps(t *testing.T) {
	db, _, cid := oauthFixture(t)
	ctx := context.Background()
	redeem(t, db, "code1", t0) // grant 1: live
	consent(t, db, 1, cid, consentOpts{nonce: "n2", code: "code2", at: t0.Add(time.Hour)})
	if _, err := db.RedeemCode(ctx, []byte("code2"), t0.Add(time.Hour), accept, nt("acc2", "ref2", t0.Add(time.Hour))); err != nil { // grant 2: live, newer
		t.Fatal(err)
	}
	consent(t, db, 1, cid, consentOpts{nonce: "n3", code: "code3", at: t0.Add(2 * time.Hour)}) // grant 3: never activated
	apps, err := db.ConnectedApps(ctx, t0.Add(2*time.Hour))
	if err != nil || len(apps) != 2 || apps[0].GrantID != 2 || apps[1].GrantID != 1 {
		t.Fatalf("apps %+v %v", apps, err)
	}
	if apps[0].Name != "App" || apps[0].Kind != "cimd" || !reflect.DeepEqual(apps[0].RedirectURIs, []string{"http://localhost/callback"}) ||
		!reflect.DeepEqual(apps[0].Scopes, []string{"coach:read", "coach:write"}) {
		t.Fatalf("app %+v", apps[0])
	}
	if err := db.RevokeGrant(ctx, 2, t0, "runner"); err != nil {
		t.Fatal(err)
	}
	if apps, _ := db.ConnectedApps(ctx, t0.Add(2*time.Hour)); len(apps) != 1 {
		t.Fatalf("revoked still listed: %+v", apps)
	}
	if apps, _ := db.ConnectedApps(ctx, t0.Add(60*24*time.Hour)); len(apps) != 0 {
		t.Fatalf("expired still listed: %+v", apps)
	}
}

func TestDeleteOAuthStateAndPurge(t *testing.T) {
	db, _, cid := oauthFixture(t)
	ctx := context.Background()
	redeem(t, db, "code1", t0)
	if n, err := db.DeleteOAuthState(ctx); err != nil || n == 0 {
		t.Fatalf("delete %d %v", n, err)
	}
	for _, tbl := range []string{"oauth_grants", "oauth_codes", "oauth_tokens"} {
		var n int
		db.sql.QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n)
		if n != 0 {
			t.Fatalf("%s has %d rows", tbl, n)
		}
	}
	if _, err := db.OAuthClientByClientID(ctx, "https://a.example/c"); err != nil {
		t.Fatalf("client must stay: %v", err)
	}
	_ = cid

	// purge
	db2, ctx := openTest(t), context.Background()
	c1, _ := db2.PutOAuthClient(ctx, testClient("https://a.example/c")) // fetched t0
	old := testClient("https://old.example/c")
	old.FetchedAt = t0.Add(-60 * 24 * time.Hour)
	db2.PutOAuthClient(ctx, old)
	dcr := testClient("dcr-old")
	dcr.Kind, dcr.FetchedAt = "dcr", t0.Add(-60*24*time.Hour)
	db2.PutOAuthClient(ctx, dcr)
	pk := oauthPasskey(t, db2)
	// grant A: old, tokens long expired -> removed. Grant B: young with expired tokens -> stays (grant), tokens go.
	consent(t, db2, pk, c1, consentOpts{nonce: "a", code: "ca", at: t0})
	if _, err := db2.RedeemCode(ctx, []byte("ca"), t0, accept, NewTokens{AccessHash: []byte("aa"), RefreshHash: []byte("ra"),
		AccessExpires: t0.Add(time.Hour), RefreshExpires: t0.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	now := t0.Add(40 * 24 * time.Hour)
	consent(t, db2, pk, c1, consentOpts{nonce: "b", code: "cb", at: now.Add(-2 * 24 * time.Hour)})
	if _, err := db2.RedeemCode(ctx, []byte("cb"), now.Add(-2*24*time.Hour), accept, NewTokens{AccessHash: []byte("ab"), RefreshHash: []byte("rb"),
		AccessExpires: now.Add(-47 * time.Hour), RefreshExpires: now.Add(-25 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if n, err := db2.PurgeOAuth(ctx, now); err != nil || n == 0 {
		t.Fatalf("purge %d %v", n, err)
	}
	cnt := func(q string) int {
		var n int
		db2.sql.QueryRow(q).Scan(&n)
		return n
	}
	if cnt(`SELECT count(*) FROM oauth_grants`) != 1 || cnt(`SELECT count(*) FROM oauth_tokens`) != 0 || cnt(`SELECT count(*) FROM oauth_codes`) != 0 {
		t.Fatalf("grants=%d tokens=%d codes=%d", cnt(`SELECT count(*) FROM oauth_grants`), cnt(`SELECT count(*) FROM oauth_tokens`), cnt(`SELECT count(*) FROM oauth_codes`))
	}
	if _, err := db2.OAuthClientByClientID(ctx, "https://old.example/c"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old cimd client should go: %v", err)
	}
	if _, err := db2.OAuthClientByClientID(ctx, "dcr-old"); err != nil {
		t.Fatalf("dcr client must stay: %v", err)
	}
	if _, err := db2.OAuthClientByClientID(ctx, "https://a.example/c"); err != nil {
		t.Fatalf("client with grant must stay: %v", err)
	}
}

func TestRedeemCodeNilCheck(t *testing.T) {
	db, _, _ := oauthFixture(t)
	if _, err := db.RedeemCode(context.Background(), []byte("code1"), t0, nil, nt("a", "r", t0)); err == nil {
		t.Fatal("nil check must be rejected")
	}
	var n int
	db.sql.QueryRow(`SELECT count(*) FROM oauth_tokens`).Scan(&n)
	if n != 0 {
		t.Fatal("tokens issued")
	}
	redeem(t, db, "code1", t0) // code still unused
}

func TestRevokeGrantByTokenEmptyClient(t *testing.T) {
	db, _, _ := oauthFixture(t)
	redeem(t, db, "code1", t0)
	if err := db.RevokeGrantByToken(context.Background(), []byte("ref1"), "", t0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AccessGrant(context.Background(), []byte("acc1"), t0); !errors.Is(err, ErrRevoked) {
		t.Fatalf("empty client_id should revoke: %v", err)
	}
}
