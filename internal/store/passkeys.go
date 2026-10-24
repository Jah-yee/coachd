package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const passkeyCols = `id, credential_id, credential_json, label, created_at, last_used_at, revoked_at`

func scanPasskey(s scanner) (Passkey, error) {
	var p Passkey
	var cj, created string
	var used, revoked sql.NullString
	if err := s.Scan(&p.ID, &p.CredentialID, &cj, &p.Label, &created, &used, &revoked); err != nil {
		return p, err
	}
	p.CredentialJSON = []byte(cj)
	var err error
	if p.CreatedAt, err = parseTime(created); err != nil {
		return p, err
	}
	if p.LastUsedAt, err = parseTimePtr(used); err != nil {
		return p, err
	}
	p.RevokedAt, err = parseTimePtr(revoked)
	return p, err
}

// CreateEnrollment stores a one-time enrolment (by token hash) and its audit row.
func (db *DB) CreateEnrollment(ctx context.Context, tokenHash []byte, at, expires time.Time) (int64, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: create enrollment: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO enrollments (token_hash, created_at, expires_at) VALUES (?,?,?)`,
		tokenHash, fmtTime(at), fmtTime(expires))
	if err != nil {
		return 0, fmt.Errorf("store: create enrollment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: create enrollment: %w", err)
	}
	if err := appendAuditTx(ctx, tx, AuditEvent{At: at, Lane: "cli", Actor: "cli", Event: "enrollment_created",
		Target: strconv.FormatInt(id, 10), Outcome: "ok"}); err != nil {
		return 0, fmt.Errorf("store: create enrollment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: create enrollment: %w", err)
	}
	return id, nil
}

// EnrollmentByHash returns the enrolment id, or ErrNotFound when it is absent, used or expired.
func (db *DB) EnrollmentByHash(ctx context.Context, tokenHash []byte, now time.Time) (int64, error) {
	var id int64
	err := db.sql.QueryRowContext(ctx, `SELECT id FROM enrollments WHERE token_hash=? AND used_at IS NULL AND expires_at>?`,
		tokenHash, fmtTime(now)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("store: enrollment by hash: %w", err)
	}
	return id, nil
}

// EnrollPasskey uses the enrolment, consumes the nonce, inserts the passkey and
// audits it, all in one transaction. The audit client is the new passkey
// ("passkey:<id>"), known only after the insert. ErrNotFound: enrolment
// absent, used or expired. ErrNonceUsed: nonce replay. ErrConflict:
// credential ID already stored.
func (db *DB) EnrollPasskey(ctx context.Context, p EnrollParams) (int64, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: enroll passkey: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE enrollments SET used_at=? WHERE id=? AND used_at IS NULL AND expires_at>?`,
		fmtTime(p.At), p.EnrollmentID, fmtTime(p.At))
	if err != nil {
		return 0, fmt.Errorf("store: enroll passkey: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	if err := consumeNonceTx(ctx, tx, p.Nonce, p.At, p.NonceExpires); err != nil {
		return 0, err
	}
	res, err = tx.ExecContext(ctx, `INSERT INTO passkeys (credential_id, credential_json, label, created_at) VALUES (?,?,?,?)`,
		p.Passkey.CredentialID, string(p.Passkey.CredentialJSON), p.Passkey.Label, fmtTime(p.At))
	if err != nil {
		if strings.Contains(err.Error(), "passkeys.credential_id") {
			return 0, ErrConflict
		}
		return 0, fmt.Errorf("store: enroll passkey: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: enroll passkey: %w", err)
	}
	if err := appendAuditTx(ctx, tx, AuditEvent{At: p.At, Lane: p.Lane, Actor: "runner", Client: "passkey:" + strconv.FormatInt(id, 10),
		Event: "passkey_enrolled", Target: strconv.FormatInt(id, 10), Outcome: "ok"}); err != nil {
		return 0, fmt.Errorf("store: enroll passkey: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: enroll passkey: %w", err)
	}
	return id, nil
}

func (db *DB) queryPasskeys(ctx context.Context, where string) ([]Passkey, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+passkeyCols+` FROM passkeys `+where+` ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list passkeys: %w", err)
	}
	defer rows.Close()
	var out []Passkey
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list passkeys: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ActivePasskeys returns the passkeys that are not revoked, by id.
func (db *DB) ActivePasskeys(ctx context.Context) ([]Passkey, error) {
	return db.queryPasskeys(ctx, `WHERE revoked_at IS NULL`)
}

// ListPasskeys returns every passkey, revoked ones included, by id.
func (db *DB) ListPasskeys(ctx context.Context) ([]Passkey, error) {
	return db.queryPasskeys(ctx, ``)
}

// PasskeyByCredentialID returns the active passkey, or ErrNotFound when absent or revoked.
func (db *DB) PasskeyByCredentialID(ctx context.Context, credID []byte) (Passkey, error) {
	p, err := scanPasskey(db.sql.QueryRowContext(ctx,
		`SELECT `+passkeyCols+` FROM passkeys WHERE credential_id=? AND revoked_at IS NULL`, credID))
	if errors.Is(err, sql.ErrNoRows) {
		return Passkey{}, ErrNotFound
	}
	if err != nil {
		return Passkey{}, fmt.Errorf("store: passkey by credential: %w", err)
	}
	return p, nil
}

// UsePasskey stores the updated credential JSON and last_used_at of an active passkey.
func (db *DB) UsePasskey(ctx context.Context, id int64, credentialJSON []byte, at time.Time) error {
	res, err := db.sql.ExecContext(ctx, `UPDATE passkeys SET credential_json=?, last_used_at=? WHERE id=? AND revoked_at IS NULL`,
		string(credentialJSON), fmtTime(at), id)
	if err != nil {
		return fmt.Errorf("store: use passkey: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokePasskey revokes an active passkey, deletes its sessions and audits it.
func (db *DB) RevokePasskey(ctx context.Context, id int64, at time.Time, lane, client string) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: revoke passkey: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE passkeys SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, fmtTime(at), id)
	if err != nil {
		return fmt.Errorf("store: revoke passkey: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM web_sessions WHERE passkey_id=?`, id); err != nil {
		return fmt.Errorf("store: revoke passkey: %w", err)
	}
	if err := appendAuditTx(ctx, tx, AuditEvent{At: at, Lane: lane, Actor: "runner", Client: client,
		Event: "passkey_revoked", Target: strconv.FormatInt(id, 10), Outcome: "ok"}); err != nil {
		return fmt.Errorf("store: revoke passkey: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: revoke passkey: %w", err)
	}
	return nil
}

// ConsumeNonce records a single-use nonce; a repeat returns ErrNonceUsed.
func (db *DB) ConsumeNonce(ctx context.Context, nonce string, at, expires time.Time) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: consume nonce: %w", err)
	}
	defer tx.Rollback()
	if err := consumeNonceTx(ctx, tx, nonce, at, expires); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: consume nonce: %w", err)
	}
	return nil
}

// NonceUsed reports whether a nonce was consumed. It is a read-only pre-check
// so a replay is reported as such before any other verification; the
// consuming insert stays the guard against concurrent use.
func (db *DB) NonceUsed(ctx context.Context, nonce string) (bool, error) {
	var one int
	err := db.sql.QueryRowContext(ctx, `SELECT 1 FROM used_nonces WHERE nonce=?`, nonce).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: nonce used: %w", err)
	}
	return true, nil
}

func consumeNonceTx(ctx context.Context, tx *sql.Tx, nonce string, at, expires time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO used_nonces (nonce, used_at, expires_at) VALUES (?,?,?)`,
		nonce, fmtTime(at), fmtTime(expires))
	if err != nil {
		if strings.Contains(err.Error(), "used_nonces.nonce") {
			return ErrNonceUsed
		}
		return fmt.Errorf("store: consume nonce: %w", err)
	}
	return nil
}

// CreateSession stores a web session by token hash.
func (db *DB) CreateSession(ctx context.Context, tokenHash []byte, passkeyID int64, at, expires time.Time) error {
	_, err := db.sql.ExecContext(ctx, `INSERT INTO web_sessions (token_hash, passkey_id, created_at, expires_at) VALUES (?,?,?,?)`,
		tokenHash, passkeyID, fmtTime(at), fmtTime(expires))
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// SessionByHash returns the session, or ErrNotFound when absent, expired or its passkey is revoked.
func (db *DB) SessionByHash(ctx context.Context, tokenHash []byte, now time.Time) (WebSession, error) {
	var s WebSession
	var created, expires string
	err := db.sql.QueryRowContext(ctx, `SELECT s.passkey_id, s.created_at, s.expires_at FROM web_sessions s
		JOIN passkeys p ON p.id=s.passkey_id AND p.revoked_at IS NULL
		WHERE s.token_hash=? AND s.expires_at>?`, tokenHash, fmtTime(now)).Scan(&s.PasskeyID, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return WebSession{}, ErrNotFound
	}
	if err != nil {
		return WebSession{}, fmt.Errorf("store: session by hash: %w", err)
	}
	if s.CreatedAt, err = parseTime(created); err != nil {
		return WebSession{}, err
	}
	if s.ExpiresAt, err = parseTime(expires); err != nil {
		return WebSession{}, err
	}
	return s, nil
}

// DeleteSession removes a session; a missing one is not an error.
func (db *DB) DeleteSession(ctx context.Context, tokenHash []byte) error {
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM web_sessions WHERE token_hash=?`, tokenHash); err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// PurgeRunnerAuth deletes expired sessions and nonces and enrolments used or
// expired more than 24 h before now, and device pairings that expired more
// than 24 h before now. It returns the rows removed.
func (db *DB) PurgeRunnerAuth(ctx context.Context, now time.Time) (int64, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: purge runner auth: %w", err)
	}
	defer tx.Rollback()
	var total int64
	for _, q := range []struct {
		sql string
		arg string
	}{
		{`DELETE FROM web_sessions WHERE expires_at<=?`, fmtTime(now)},
		{`DELETE FROM used_nonces WHERE expires_at<=?`, fmtTime(now)},
		{`DELETE FROM enrollments WHERE used_at<=? OR expires_at<=?`, fmtTime(now.Add(-24 * time.Hour))},
		{`DELETE FROM device_pairings WHERE expires_at<=?`, fmtTime(now.Add(-24 * time.Hour))},
	} {
		args := []any{q.arg}
		if strings.Count(q.sql, "?") == 2 {
			args = append(args, q.arg)
		}
		res, err := tx.ExecContext(ctx, q.sql, args...)
		if err != nil {
			return 0, fmt.Errorf("store: purge runner auth: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: purge runner auth: %w", err)
	}
	return total, nil
}
