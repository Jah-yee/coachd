package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const grantCols = `g.id, g.client_row_id, c.client_id, g.scopes, g.resource, g.passkey_id, g.created_at, g.activated_at, g.last_used_at, g.revoked_at`
const grantFrom = ` FROM oauth_grants g JOIN oauth_clients c ON c.id=g.client_row_id `

func splitScopes(s string) []string { return strings.Fields(s) }

func scanGrant(s scanner) (OAuthGrant, error) {
	var g OAuthGrant
	var scopes, created string
	var act, used, rev sql.NullString
	if err := s.Scan(&g.ID, &g.ClientRowID, &g.ClientID, &scopes, &g.Resource, &g.PasskeyID, &created, &act, &used, &rev); err != nil {
		return g, err
	}
	g.Scopes = splitScopes(scopes)
	var err error
	if g.CreatedAt, err = parseTime(created); err != nil {
		return g, err
	}
	if g.ActivatedAt, err = parseTimePtr(act); err != nil {
		return g, err
	}
	if g.LastUsedAt, err = parseTimePtr(used); err != nil {
		return g, err
	}
	g.RevokedAt, err = parseTimePtr(rev)
	return g, err
}

func grantTx(ctx context.Context, tx *sql.Tx, id int64) (OAuthGrant, error) {
	g, err := scanGrant(tx.QueryRowContext(ctx, `SELECT `+grantCols+grantFrom+`WHERE g.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

func grantTarget(id int64) string { return "g" + strconv.FormatInt(id, 10) }

// revokeGrantTx sets revoked_at when the grant is still live and reports whether it did.
func revokeGrantTx(ctx context.Context, tx *sql.Tx, id int64, at time.Time) (bool, error) {
	res, err := tx.ExecContext(ctx, `UPDATE oauth_grants SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, fmtTime(at), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func putClientTx(ctx context.Context, tx *sql.Tx, c OAuthClient) (int64, error) {
	uris, err := json.Marshal(append([]string{}, c.RedirectURIs...))
	if err != nil {
		return 0, err
	}
	created := c.CreatedAt
	if created.IsZero() {
		created = c.FetchedAt
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO oauth_clients (client_id, kind, name, redirect_uris, metadata, fetched_at, cache_until, created_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(client_id) DO UPDATE SET name=excluded.name, redirect_uris=excluded.redirect_uris, metadata=excluded.metadata,
			fetched_at=excluded.fetched_at, cache_until=excluded.cache_until
		RETURNING id`,
		c.ClientID, c.Kind, c.Name, string(uris), string(c.Metadata), fmtTime(c.FetchedAt), fmtTime(c.CacheUntil), fmtTime(created)).Scan(&id)
	return id, err
}

// PutOAuthClient upserts a client by client_id and returns its row id, which an update keeps.
func (db *DB) PutOAuthClient(ctx context.Context, c OAuthClient) (int64, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: put oauth client: %w", err)
	}
	defer tx.Rollback()
	id, err := putClientTx(ctx, tx, c)
	if err != nil {
		return 0, fmt.Errorf("store: put oauth client: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: put oauth client: %w", err)
	}
	return id, nil
}

// OAuthClientByClientID returns the client, or ErrNotFound.
func (db *DB) OAuthClientByClientID(ctx context.Context, clientID string) (OAuthClient, error) {
	var c OAuthClient
	var uris, meta, fetched, until, created string
	err := db.sql.QueryRowContext(ctx, `SELECT id, client_id, kind, name, redirect_uris, metadata, fetched_at, cache_until, created_at
		FROM oauth_clients WHERE client_id=?`, clientID).Scan(&c.ID, &c.ClientID, &c.Kind, &c.Name, &uris, &meta, &fetched, &until, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthClient{}, ErrNotFound
	}
	if err != nil {
		return OAuthClient{}, fmt.Errorf("store: oauth client: %w", err)
	}
	if err := json.Unmarshal([]byte(uris), &c.RedirectURIs); err != nil {
		return OAuthClient{}, fmt.Errorf("store: oauth client: %w", err)
	}
	c.Metadata = []byte(meta)
	for _, p := range []struct {
		dst *time.Time
		s   string
	}{{&c.FetchedAt, fetched}, {&c.CacheUntil, until}, {&c.CreatedAt, created}} {
		if *p.dst, err = parseTime(p.s); err != nil {
			return OAuthClient{}, fmt.Errorf("store: oauth client: %w", err)
		}
	}
	return c, nil
}

// ClientHasLiveGrant reports whether the client has an activated, unrevoked grant with an unexpired token.
func (db *DB) ClientHasLiveGrant(ctx context.Context, clientRowID int64, now time.Time) (bool, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM oauth_grants g WHERE g.client_row_id=? AND g.activated_at IS NOT NULL AND g.revoked_at IS NULL
		AND EXISTS (SELECT 1 FROM oauth_tokens t WHERE t.grant_id=g.id AND t.expires_at>?)`, clientRowID, fmtTime(now)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: client live grant: %w", err)
	}
	return n > 0, nil
}

// GrantConsent records a runner's approval in one transaction: the passkey use,
// the nonce, the client (when new), the grant, its code and the audit row.
// ErrNonceUsed: nonce replay. ErrNotFound: the passkey is absent or revoked.
func (db *DB) GrantConsent(ctx context.Context, g ConsentGrant) (int64, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: grant consent: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE passkeys SET credential_json=?, last_used_at=? WHERE id=? AND revoked_at IS NULL`,
		string(g.CredentialJSON), fmtTime(g.At), g.PasskeyID)
	if err != nil {
		return 0, fmt.Errorf("store: grant consent: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	if err := consumeNonceTx(ctx, tx, g.Nonce, g.At, g.NonceExpires); err != nil {
		return 0, err
	}
	clientRow := g.Client.ID
	if clientRow == 0 {
		if clientRow, err = putClientTx(ctx, tx, g.Client); err != nil {
			return 0, fmt.Errorf("store: grant consent: %w", err)
		}
	}
	res, err = tx.ExecContext(ctx, `INSERT INTO oauth_grants (client_row_id, scopes, resource, passkey_id, created_at) VALUES (?,?,?,?,?)`,
		clientRow, strings.Join(g.Scopes, " "), g.Resource, g.PasskeyID, fmtTime(g.At))
	if err != nil {
		return 0, fmt.Errorf("store: grant consent: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: grant consent: %w", err)
	}
	explicit := 0
	if g.RedirectExplicit {
		explicit = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_codes (hash, grant_id, redirect_uri, redirect_explicit, code_challenge, expires_at) VALUES (?,?,?,?,?,?)`,
		g.CodeHash, id, g.RedirectURI, explicit, g.CodeChallenge, fmtTime(g.CodeExpires)); err != nil {
		return 0, fmt.Errorf("store: grant consent: %w", err)
	}
	if err := appendAuditTx(ctx, tx, AuditEvent{At: g.At, Lane: "public", Actor: "runner", Event: "oauth_consent",
		Target: grantTarget(id), Outcome: "approved"}); err != nil {
		return 0, fmt.Errorf("store: grant consent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: grant consent: %w", err)
	}
	return id, nil
}

func insertTokensTx(ctx context.Context, tx *sql.Tx, grantID int64, scopes []string, at time.Time, t NewTokens) error {
	for _, r := range []struct {
		hash    []byte
		kind    string
		expires time.Time
	}{{t.AccessHash, "access", t.AccessExpires}, {t.RefreshHash, "refresh", t.RefreshExpires}} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_tokens (hash, grant_id, kind, scopes, expires_at, created_at) VALUES (?,?,?,?,?,?)`,
			r.hash, grantID, r.kind, strings.Join(scopes, " "), fmtTime(r.expires), fmtTime(at)); err != nil {
			return err
		}
	}
	return nil
}

// RedeemCode exchanges a code for a first token pair. check validates the
// request against the stored code (client, redirect, PKCE) and its error is
// returned as is, leaving the code unused. A used code whose check passes is a
// replay: the grant is revoked and ErrCodeReused returned. A used code whose
// check fails is ErrNotFound.
func (db *DB) RedeemCode(ctx context.Context, codeHash []byte, at time.Time, check func(OAuthCode) error, t NewTokens) (Redeemed, error) {
	if check == nil {
		return Redeemed{}, errors.New("store: redeem code: nil check")
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return Redeemed{}, fmt.Errorf("store: redeem code: %w", err)
	}
	defer tx.Rollback()
	var code OAuthCode
	var expires string
	var used sql.NullString
	var explicit int
	err = tx.QueryRowContext(ctx, `SELECT o.grant_id, c.client_id, o.redirect_uri, o.redirect_explicit, o.code_challenge, o.expires_at, o.used_at
		FROM oauth_codes o JOIN oauth_grants g ON g.id=o.grant_id JOIN oauth_clients c ON c.id=g.client_row_id WHERE o.hash=?`, codeHash).
		Scan(&code.GrantID, &code.ClientID, &code.RedirectURI, &explicit, &code.CodeChallenge, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return Redeemed{}, ErrNotFound
	}
	if err != nil {
		return Redeemed{}, fmt.Errorf("store: redeem code: %w", err)
	}
	code.RedirectExplicit = explicit != 0
	code.Used = used.Valid
	if code.ExpiresAt, err = parseTime(expires); err != nil {
		return Redeemed{}, fmt.Errorf("store: redeem code: %w", err)
	}
	if cerr := check(code); cerr != nil {
		if code.Used {
			return Redeemed{}, ErrNotFound
		}
		return Redeemed{}, cerr
	}
	grant, err := grantTx(ctx, tx, code.GrantID)
	if err != nil {
		return Redeemed{}, fmt.Errorf("store: redeem code: %w", err)
	}
	if code.Used {
		if grant.RevokedAt != nil {
			return Redeemed{}, ErrRevoked
		}
		if _, err := revokeGrantTx(ctx, tx, grant.ID, at); err != nil {
			return Redeemed{}, fmt.Errorf("store: redeem code: %w", err)
		}
		if err := appendAuditTx(ctx, tx, AuditEvent{At: at, Lane: "public", Actor: "client", Event: "oauth_code_reuse",
			Target: grantTarget(grant.ID), Outcome: "revoked"}); err != nil {
			return Redeemed{}, fmt.Errorf("store: redeem code: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return Redeemed{}, fmt.Errorf("store: redeem code: %w", err)
		}
		return Redeemed{}, ErrCodeReused
	}
	if grant.RevokedAt != nil {
		return Redeemed{}, ErrRevoked
	}
	if !code.ExpiresAt.After(at) {
		return Redeemed{}, ErrExpired
	}
	if _, err := tx.ExecContext(ctx, `UPDATE oauth_codes SET used_at=? WHERE hash=?`, fmtTime(at), codeHash); err != nil {
		return Redeemed{}, fmt.Errorf("store: redeem code: %w", err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE oauth_grants SET activated_at=? WHERE id=? AND activated_at IS NULL`, fmtTime(at), grant.ID)
	if err != nil {
		return Redeemed{}, fmt.Errorf("store: redeem code: %w", err)
	}
	first := false
	if n, _ := res.RowsAffected(); n > 0 {
		first = true
		grant.ActivatedAt = &at
	}
	if err := insertTokensTx(ctx, tx, grant.ID, grant.Scopes, at, t); err != nil {
		return Redeemed{}, fmt.Errorf("store: redeem code: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Redeemed{}, fmt.Errorf("store: redeem code: %w", err)
	}
	return Redeemed{Grant: grant, FirstUse: first}, nil
}

func subset(sub, of []string) bool {
	for _, s := range sub {
		found := false
		for _, o := range of {
			if s == o {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// RotateRefresh spends a refresh token and issues a new pair. A token already
// used is accepted again within grace (Sibling true); later, the grant is
// revoked and ErrTokenReused returned. scopes nil keeps the token's scopes;
// otherwise they must be a subset of them (ErrScope) and carry forward.
func (db *DB) RotateRefresh(ctx context.Context, refreshHash []byte, clientID string, at time.Time, grace time.Duration, scopes []string, t NewTokens) (Rotated, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return Rotated{}, fmt.Errorf("store: rotate refresh: %w", err)
	}
	defer tx.Rollback()
	var grantID int64
	var tscopes, expires string
	var used sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT t.grant_id, t.scopes, t.expires_at, t.used_at FROM oauth_tokens t
		JOIN oauth_grants g ON g.id=t.grant_id JOIN oauth_clients c ON c.id=g.client_row_id
		WHERE t.hash=? AND t.kind='refresh' AND c.client_id=?`, refreshHash, clientID).Scan(&grantID, &tscopes, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return Rotated{}, ErrNotFound
	}
	if err != nil {
		return Rotated{}, fmt.Errorf("store: rotate refresh: %w", err)
	}
	grant, err := grantTx(ctx, tx, grantID)
	if err != nil {
		return Rotated{}, fmt.Errorf("store: rotate refresh: %w", err)
	}
	if grant.RevokedAt != nil {
		return Rotated{}, ErrRevoked
	}
	exp, err := parseTime(expires)
	if err != nil {
		return Rotated{}, fmt.Errorf("store: rotate refresh: %w", err)
	}
	if !exp.After(at) {
		return Rotated{}, ErrExpired
	}
	usedAt, err := parseTimePtr(used)
	if err != nil {
		return Rotated{}, fmt.Errorf("store: rotate refresh: %w", err)
	}
	sibling := false
	if usedAt != nil {
		if at.Sub(*usedAt) >= grace {
			if _, err := revokeGrantTx(ctx, tx, grant.ID, at); err != nil {
				return Rotated{}, fmt.Errorf("store: rotate refresh: %w", err)
			}
			if err := appendAuditTx(ctx, tx, AuditEvent{At: at, Lane: "public", Actor: "client", Event: "oauth_refresh_reuse",
				Target: grantTarget(grant.ID), Outcome: "revoked"}); err != nil {
				return Rotated{}, fmt.Errorf("store: rotate refresh: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return Rotated{}, fmt.Errorf("store: rotate refresh: %w", err)
			}
			return Rotated{}, ErrTokenReused
		}
		sibling = true
	}
	eff := splitScopes(tscopes)
	if scopes != nil {
		if !subset(scopes, eff) {
			return Rotated{}, ErrScope
		}
		eff = scopes
	}
	if !sibling {
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_tokens SET used_at=? WHERE hash=?`, fmtTime(at), refreshHash); err != nil {
			return Rotated{}, fmt.Errorf("store: rotate refresh: %w", err)
		}
	}
	if err := insertTokensTx(ctx, tx, grant.ID, eff, at, t); err != nil {
		return Rotated{}, fmt.Errorf("store: rotate refresh: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Rotated{}, fmt.Errorf("store: rotate refresh: %w", err)
	}
	return Rotated{Grant: grant, Scopes: eff, Sibling: sibling}, nil
}

// AccessGrant resolves an access token to its grant and token scopes.
// ErrNotFound: unknown (or a refresh token). ErrRevoked: grant revoked.
// ErrExpired: token past its expiry. It refreshes the grant's last_used_at at
// most once a minute.
func (db *DB) AccessGrant(ctx context.Context, accessHash []byte, now time.Time) (OAuthGrant, []string, error) {
	var tscopes, expires string
	var g OAuthGrant
	var gscopes, created string
	var act, used, rev sql.NullString
	err := db.sql.QueryRowContext(ctx, `SELECT `+grantCols+`, t.scopes, t.expires_at FROM oauth_tokens t
		JOIN oauth_grants g ON g.id=t.grant_id JOIN oauth_clients c ON c.id=g.client_row_id
		WHERE t.hash=? AND t.kind='access'`, accessHash).
		Scan(&g.ID, &g.ClientRowID, &g.ClientID, &gscopes, &g.Resource, &g.PasskeyID, &created, &act, &used, &rev, &tscopes, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthGrant{}, nil, ErrNotFound
	}
	if err != nil {
		return OAuthGrant{}, nil, fmt.Errorf("store: access grant: %w", err)
	}
	g.Scopes = splitScopes(gscopes)
	if g.CreatedAt, err = parseTime(created); err != nil {
		return OAuthGrant{}, nil, fmt.Errorf("store: access grant: %w", err)
	}
	if g.ActivatedAt, err = parseTimePtr(act); err != nil {
		return OAuthGrant{}, nil, fmt.Errorf("store: access grant: %w", err)
	}
	if g.LastUsedAt, err = parseTimePtr(used); err != nil {
		return OAuthGrant{}, nil, fmt.Errorf("store: access grant: %w", err)
	}
	if g.RevokedAt, err = parseTimePtr(rev); err != nil {
		return OAuthGrant{}, nil, fmt.Errorf("store: access grant: %w", err)
	}
	if g.RevokedAt != nil {
		return OAuthGrant{}, nil, ErrRevoked
	}
	exp, err := parseTime(expires)
	if err != nil {
		return OAuthGrant{}, nil, fmt.Errorf("store: access grant: %w", err)
	}
	if !exp.After(now) {
		return OAuthGrant{}, nil, ErrExpired
	}
	// An UPDATE takes the write lock even when nothing matches, so only write when stale.
	if g.LastUsedAt == nil || g.LastUsedAt.Before(now.Add(-time.Minute)) {
		if _, err := db.sql.ExecContext(ctx, `UPDATE oauth_grants SET last_used_at=? WHERE id=? AND (last_used_at IS NULL OR last_used_at<?)`,
			fmtTime(now), g.ID, fmtTime(now.Add(-time.Minute))); err != nil {
			return OAuthGrant{}, nil, fmt.Errorf("store: access grant: %w", err)
		}
		g.LastUsedAt = &now
	}
	return g, splitScopes(tscopes), nil
}

// RevokeGrant revokes a live grant (and so all its tokens) and audits it. ErrNotFound when absent or already revoked.
func (db *DB) RevokeGrant(ctx context.Context, grantID int64, at time.Time, actor string) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: revoke grant: %w", err)
	}
	defer tx.Rollback()
	ok, err := revokeGrantTx(ctx, tx, grantID, at)
	if err != nil {
		return fmt.Errorf("store: revoke grant: %w", err)
	}
	if !ok {
		return ErrNotFound
	}
	if err := appendAuditTx(ctx, tx, AuditEvent{At: at, Lane: "public", Actor: actor, Event: "oauth_grant_revoked",
		Target: grantTarget(grantID), Outcome: "ok"}); err != nil {
		return fmt.Errorf("store: revoke grant: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: revoke grant: %w", err)
	}
	return nil
}

// RevokeGrantByToken is RFC 7009 revocation: it revokes the grant of a token
// that is unexpired and belongs to clientID (any client when empty), and returns nil, revoking nothing, otherwise.
func (db *DB) RevokeGrantByToken(ctx context.Context, tokenHash []byte, clientID string, at time.Time) error {
	var gid int64
	err := db.sql.QueryRowContext(ctx, `SELECT t.grant_id FROM oauth_tokens t JOIN oauth_grants g ON g.id=t.grant_id
		JOIN oauth_clients c ON c.id=g.client_row_id WHERE t.hash=? AND (?='' OR c.client_id=?) AND t.expires_at>?`,
		tokenHash, clientID, clientID, fmtTime(at)).Scan(&gid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: revoke grant by token: %w", err)
	}
	if err := db.RevokeGrant(ctx, gid, at, "client"); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// ConnectedApps lists activated, unrevoked grants that have an unexpired token, newest first.
func (db *DB) ConnectedApps(ctx context.Context, now time.Time) ([]ConnectedApp, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT g.id, c.client_id, c.kind, c.name, c.redirect_uris, g.scopes, g.created_at, g.last_used_at
		`+grantFrom+`WHERE g.activated_at IS NOT NULL AND g.revoked_at IS NULL
		AND EXISTS (SELECT 1 FROM oauth_tokens t WHERE t.grant_id=g.id AND t.expires_at>?)
		ORDER BY g.created_at DESC, g.id DESC`, fmtTime(now))
	if err != nil {
		return nil, fmt.Errorf("store: connected apps: %w", err)
	}
	defer rows.Close()
	var out []ConnectedApp
	for rows.Next() {
		var a ConnectedApp
		var uris, scopes, created string
		var used sql.NullString
		if err := rows.Scan(&a.GrantID, &a.ClientID, &a.Kind, &a.Name, &uris, &scopes, &created, &used); err != nil {
			return nil, fmt.Errorf("store: connected apps: %w", err)
		}
		if err := json.Unmarshal([]byte(uris), &a.RedirectURIs); err != nil {
			return nil, fmt.Errorf("store: connected apps: %w", err)
		}
		a.Scopes = splitScopes(scopes)
		if a.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("store: connected apps: %w", err)
		}
		if a.LastUsedAt, err = parseTimePtr(used); err != nil {
			return nil, fmt.Errorf("store: connected apps: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func execCount(ctx context.Context, tx *sql.Tx, total *int64, q string, args ...any) error {
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	*total += n
	return nil
}

// DeleteOAuthState deletes every grant, code and token (clients stay) and returns the rows removed.
func (db *DB) DeleteOAuthState(ctx context.Context) (int64, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: delete oauth state: %w", err)
	}
	defer tx.Rollback()
	var n int64
	for _, q := range []string{`DELETE FROM oauth_tokens`, `DELETE FROM oauth_codes`, `DELETE FROM oauth_grants`} {
		if err := execCount(ctx, tx, &n, q); err != nil {
			return 0, fmt.Errorf("store: delete oauth state: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: delete oauth state: %w", err)
	}
	return n, nil
}

// PurgeOAuth deletes codes and tokens expired over 24 h ago, grants older than
// 30 days with no live token, and unused CIMD clients fetched over 30 days ago.
// It returns the rows removed.
func (db *DB) PurgeOAuth(ctx context.Context, now time.Time) (int64, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: purge oauth: %w", err)
	}
	defer tx.Rollback()
	day, month, cur := fmtTime(now.Add(-24*time.Hour)), fmtTime(now.Add(-30*24*time.Hour)), fmtTime(now)
	const dead = ` WHERE grant_id IN (SELECT g.id FROM oauth_grants g WHERE g.created_at<?
		AND NOT EXISTS (SELECT 1 FROM oauth_tokens t WHERE t.grant_id=g.id AND t.expires_at>?))`
	var n int64
	for _, s := range []struct {
		q    string
		args []any
	}{
		{`DELETE FROM oauth_codes WHERE expires_at<?`, []any{day}},
		{`DELETE FROM oauth_tokens WHERE expires_at<?`, []any{day}},
		{`DELETE FROM oauth_codes` + dead, []any{month, cur}},
		{`DELETE FROM oauth_tokens` + dead, []any{month, cur}},
		{`DELETE FROM oauth_grants WHERE created_at<?
			AND NOT EXISTS (SELECT 1 FROM oauth_tokens t WHERE t.grant_id=oauth_grants.id AND t.expires_at>?)`, []any{month, cur}},
		{`DELETE FROM oauth_clients WHERE kind='cimd' AND fetched_at<?
			AND NOT EXISTS (SELECT 1 FROM oauth_grants g WHERE g.client_row_id=oauth_clients.id)`, []any{month}},
	} {
		if err := execCount(ctx, tx, &n, s.q, s.args...); err != nil {
			return 0, fmt.Errorf("store: purge oauth: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: purge oauth: %w", err)
	}
	return n, nil
}
