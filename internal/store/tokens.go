package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const tokenCols = `id, name, hash, scopes, created_at, expires_at, last_used_at, revoked_at`

func scanToken(s scanner) (TokenRecord, error) {
	var t TokenRecord
	var scopes, created, expires string
	var used, revoked sql.NullString
	if err := s.Scan(&t.ID, &t.Name, &t.Hash, &scopes, &created, &expires, &used, &revoked); err != nil {
		return t, err
	}
	if scopes != "" {
		t.Scopes = strings.Split(scopes, " ")
	}
	var err error
	if t.CreatedAt, err = parseTime(created); err != nil {
		return t, err
	}
	if t.ExpiresAt, err = parseTime(expires); err != nil {
		return t, err
	}
	if t.LastUsedAt, err = parseTimePtr(used); err != nil {
		return t, err
	}
	if t.RevokedAt, err = parseTimePtr(revoked); err != nil {
		return t, err
	}
	return t, nil
}

// CreateToken stores a token record. Scopes are persisted space-separated, so they must not contain spaces.
func (db *DB) CreateToken(ctx context.Context, t TokenRecord) error {
	for _, s := range t.Scopes {
		if s == "" || strings.ContainsAny(s, " \t\n") {
			return fmt.Errorf("store: invalid scope %q", s)
		}
	}
	_, err := db.sql.ExecContext(ctx, `INSERT INTO api_tokens (`+tokenCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		t.ID, t.Name, t.Hash, strings.Join(t.Scopes, " "), fmtTime(t.CreatedAt), fmtTime(t.ExpiresAt),
		fmtTimePtr(t.LastUsedAt), fmtTimePtr(t.RevokedAt))
	if err != nil {
		return fmt.Errorf("store: create token: %w", err)
	}
	return nil
}

func (db *DB) TokenByHash(ctx context.Context, hash []byte) (TokenRecord, error) {
	t, err := scanToken(db.sql.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE hash=?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return TokenRecord{}, ErrNotFound
	}
	if err != nil {
		return TokenRecord{}, fmt.Errorf("store: token by hash: %w", err)
	}
	return t, nil
}

func (db *DB) ListTokens(ctx context.Context) ([]TokenRecord, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+tokenCols+` FROM api_tokens ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list tokens: %w", err)
	}
	defer rows.Close()
	var out []TokenRecord
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (db *DB) RevokeToken(ctx context.Context, id string, at time.Time) error {
	return db.updateToken(ctx, `UPDATE api_tokens SET revoked_at=? WHERE id=?`, id, at)
}

func (db *DB) TouchToken(ctx context.Context, id string, at time.Time) error {
	return db.updateToken(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, id, at)
}

func (db *DB) updateToken(ctx context.Context, q, id string, at time.Time) error {
	res, err := db.sql.ExecContext(ctx, q, fmtTime(at), id)
	if err != nil {
		return fmt.Errorf("store: update token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
