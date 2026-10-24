package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// CreatePairing stores a pending pairing and its audit row. Pending means
// unexpired at p.CreatedAt and unclaimed. ErrTooManyPairings at the cap;
// ErrConflict when the code matches another pending pairing.
func (db *DB) CreatePairing(ctx context.Context, p Pairing, maxPending int) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: create pairing: %w", err)
	}
	defer tx.Rollback()
	now := fmtTime(p.CreatedAt)
	var pending, same int
	if err := tx.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(code=?),0) FROM device_pairings
		WHERE expires_at>? AND claimed_at IS NULL`, p.Code, now).Scan(&pending, &same); err != nil {
		return fmt.Errorf("store: create pairing: %w", err)
	}
	if pending >= maxPending {
		return ErrTooManyPairings
	}
	if same > 0 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO device_pairings (id, code, model, created_at, expires_at) VALUES (?,?,?,?,?)`,
		p.ID, p.Code, p.Model, now, fmtTime(p.ExpiresAt)); err != nil {
		return fmt.Errorf("store: create pairing: %w", err)
	}
	if err := appendAuditTx(ctx, tx, AuditEvent{At: p.CreatedAt, Lane: "public", Actor: "device", Client: "device",
		Event: "device_pairing_started", Target: shortID(p.ID), Outcome: "ok"}); err != nil {
		return fmt.Errorf("store: create pairing: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: create pairing: %w", err)
	}
	return nil
}

// ConfirmPairing marks the pending, unexpired, unconfirmed pairing with that
// code as confirmed under label. ErrNotFound when there is none.
func (db *DB) ConfirmPairing(ctx context.Context, code, label string, at time.Time, lane, actor, client string) (Pairing, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return Pairing{}, fmt.Errorf("store: confirm pairing: %w", err)
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `UPDATE device_pairings SET confirmed_at=?, label=?
		WHERE id=(SELECT id FROM device_pairings WHERE code=? AND expires_at>? AND claimed_at IS NULL AND confirmed_at IS NULL
			ORDER BY created_at DESC LIMIT 1)
		RETURNING id`, fmtTime(at), label, code, fmtTime(at)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Pairing{}, ErrNotFound
	}
	if err != nil {
		return Pairing{}, fmt.Errorf("store: confirm pairing: %w", err)
	}
	var p Pairing
	var created, expires, confirmed string
	if err := tx.QueryRowContext(ctx, `SELECT id, code, model, created_at, expires_at, confirmed_at, label FROM device_pairings WHERE id=?`, id).
		Scan(&p.ID, &p.Code, &p.Model, &created, &expires, &confirmed, &p.Label); err != nil {
		return Pairing{}, fmt.Errorf("store: confirm pairing: %w", err)
	}
	if p.CreatedAt, err = parseTime(created); err != nil {
		return Pairing{}, err
	}
	if p.ExpiresAt, err = parseTime(expires); err != nil {
		return Pairing{}, err
	}
	ct, err := parseTime(confirmed)
	if err != nil {
		return Pairing{}, err
	}
	p.ConfirmedAt = &ct
	if err := appendAuditTx(ctx, tx, AuditEvent{At: at, Lane: lane, Actor: actor, Client: client,
		Event: "device_pairing_confirmed", Target: shortID(p.ID), Outcome: "ok"}); err != nil {
		return Pairing{}, fmt.Errorf("store: confirm pairing: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Pairing{}, fmt.Errorf("store: confirm pairing: %w", err)
	}
	return p, nil
}

// PendingPairings returns the pairings a watch has started and nobody has
// confirmed yet (unexpired at now, unconfirmed, unclaimed), newest first.
// Only model, creation and expiry are read: ID (the claim secret) and Code
// (the runner reads it from the watch) stay empty.
func (db *DB) PendingPairings(ctx context.Context, now time.Time) ([]Pairing, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT model, created_at, expires_at FROM device_pairings
		WHERE expires_at>? AND confirmed_at IS NULL AND claimed_at IS NULL ORDER BY created_at DESC, rowid DESC`, fmtTime(now))
	if err != nil {
		return nil, fmt.Errorf("store: pending pairings: %w", err)
	}
	defer rows.Close()
	var out []Pairing
	for rows.Next() {
		var p Pairing
		var created, expires string
		if err := rows.Scan(&p.Model, &created, &expires); err != nil {
			return nil, fmt.Errorf("store: scan pending pairing: %w", err)
		}
		if p.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if p.ExpiresAt, err = parseTime(expires); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ClaimPairing is the watch's poll. "paired" is returned once, for a confirmed,
// unexpired, unclaimed pairing: the device is created with tokenHash. "pending"
// is an unconfirmed, unexpired pairing; anything else is "expired".
func (db *DB) ClaimPairing(ctx context.Context, id string, tokenHash []byte, at time.Time) (string, int64, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, fmt.Errorf("store: claim pairing: %w", err)
	}
	defer tx.Rollback()
	now := fmtTime(at)
	var label, model string
	err = tx.QueryRowContext(ctx, `UPDATE device_pairings SET claimed_at=?
		WHERE id=? AND confirmed_at IS NOT NULL AND claimed_at IS NULL AND expires_at>?
		RETURNING label, model`, now, id, now).Scan(&label, &model)
	if errors.Is(err, sql.ErrNoRows) {
		var confirmed sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT confirmed_at FROM device_pairings
			WHERE id=? AND claimed_at IS NULL AND expires_at>?`, id, now).Scan(&confirmed)
		if errors.Is(err, sql.ErrNoRows) {
			return "expired", 0, nil
		}
		if err != nil {
			return "", 0, fmt.Errorf("store: claim pairing: %w", err)
		}
		return "pending", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("store: claim pairing: %w", err)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO devices (label, model, token_hash, created_at) VALUES (?,?,?,?)`,
		label, model, tokenHash, now)
	if err != nil {
		return "", 0, fmt.Errorf("store: claim pairing: %w", err)
	}
	devID, err := res.LastInsertId()
	if err != nil {
		return "", 0, fmt.Errorf("store: claim pairing: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE device_pairings SET device_id=? WHERE id=?`, devID, id); err != nil {
		return "", 0, fmt.Errorf("store: claim pairing: %w", err)
	}
	who := "device:" + strconv.FormatInt(devID, 10)
	if err := appendAuditTx(ctx, tx, AuditEvent{At: at, Lane: "public", Actor: who, Client: who,
		Event: "device_paired", Target: strconv.FormatInt(devID, 10), Outcome: "ok"}); err != nil {
		return "", 0, fmt.Errorf("store: claim pairing: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("store: claim pairing: %w", err)
	}
	return "paired", devID, nil
}

const deviceCols = `id, label, model, created_at, last_seen_at, revoked_at`

func scanDevice(s scanner) (Device, error) {
	var d Device
	var created string
	var seen, revoked sql.NullString
	if err := s.Scan(&d.ID, &d.Label, &d.Model, &created, &seen, &revoked); err != nil {
		return d, err
	}
	var err error
	if d.CreatedAt, err = parseTime(created); err != nil {
		return d, err
	}
	if d.LastSeenAt, err = parseTimePtr(seen); err != nil {
		return d, err
	}
	d.RevokedAt, err = parseTimePtr(revoked)
	return d, err
}

// DeviceByTokenHash returns the device, or ErrNotFound when absent or revoked.
func (db *DB) DeviceByTokenHash(ctx context.Context, hash []byte) (Device, error) {
	d, err := scanDevice(db.sql.QueryRowContext(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE token_hash=? AND revoked_at IS NULL`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("store: device by token: %w", err)
	}
	return d, nil
}

// TouchDevice sets last_seen_at when it is NULL or older than at-1m.
func (db *DB) TouchDevice(ctx context.Context, id int64, at time.Time) error {
	if _, err := db.sql.ExecContext(ctx, `UPDATE devices SET last_seen_at=?
		WHERE id=? AND (last_seen_at IS NULL OR last_seen_at<?)`, fmtTime(at), id, fmtTime(at.Add(-time.Minute))); err != nil {
		return fmt.Errorf("store: touch device: %w", err)
	}
	return nil
}

// ListDevices returns every device, revoked ones included, by id.
func (db *DB) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+deviceCols+` FROM devices ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list devices: %w", err)
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list devices: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RevokeDevice revokes an active device and audits it. ErrNotFound when absent or already revoked.
func (db *DB) RevokeDevice(ctx context.Context, id int64, at time.Time, lane, actor, client string) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: revoke device: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE devices SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, fmtTime(at), id)
	if err != nil {
		return fmt.Errorf("store: revoke device: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := appendAuditTx(ctx, tx, AuditEvent{At: at, Lane: lane, Actor: actor, Client: client,
		Event: "device_revoked", Target: strconv.FormatInt(id, 10), Outcome: "ok"}); err != nil {
		return fmt.Errorf("store: revoke device: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: revoke device: %w", err)
	}
	return nil
}
