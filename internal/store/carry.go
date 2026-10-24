package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
)

// A restore replaces coachd.db with an older snapshot. Revocations and used
// single-use nonces recorded after the snapshot must survive it, or a revoked
// credential or a used approval link would work again. ReadCarryForward and
// ApplyCarryForward open their files with plain database/sql, never through
// Open, so neither database is migrated.

// CarryForward is the security state a restore carries from the database it
// replaces into the restored one.
type CarryForward struct {
	revoked []revocation
	nonces  []usedNonce
}

type revocation struct {
	table string
	id    any    // TEXT for api_tokens, INTEGER for passkeys and devices
	key   []byte // the row's unique credential column
	at    string // revoked_at as stored
}

type usedNonce struct{ nonce, usedAt, expiresAt string }

// revocable lists the tables with revoked_at and, for each, the immutable
// unique column that must also match: an id alone could name a different
// credential when the replaced database does not descend from the snapshot.
var revocable = []struct{ table, key string }{
	{"api_tokens", "hash"}, {"passkeys", "credential_id"}, {"devices", "token_hash"},
}

// openPlain opens the existing SQLite file at path read-write, without
// migrations, with the given extra pragmas.
func openPlain(path string, pragmas ...string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("mode", "rw") // never create a missing file
	q.Add("_pragma", "busy_timeout(5000)")
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	u, err := fileURI(path, q.Encode())
	if err != nil {
		return nil, err
	}
	return sql.Open("sqlite", u)
}

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// tables returns the names of the tables in the database.
func tables(ctx context.Context, q querier) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// ReadCarryForward checkpoints the database at path, so the file holds every
// commit without its -wal, and reads its revocations and used nonces. Tables
// an older schema lacks are skipped. coachd serve must be stopped. A file
// without the SQLite header is refused unopened: SQLite would delete the -wal
// beside it, which the restore keeps for the runner.
func ReadCarryForward(ctx context.Context, path string) (CarryForward, error) {
	if err := sqliteHeader(path); err != nil {
		return CarryForward{}, fmt.Errorf("store: read carry-forward: %w", err)
	}
	sdb, err := openPlain(path)
	if err != nil {
		return CarryForward{}, fmt.Errorf("store: read carry-forward: %w", err)
	}
	defer sdb.Close()
	c, err := readCarryForward(ctx, sdb)
	if err != nil {
		return CarryForward{}, fmt.Errorf("store: read carry-forward: %w", err)
	}
	return c, nil
}

// sqliteHeader fails unless the file at path starts with the SQLite magic.
func sqliteHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	magic := make([]byte, 16)
	if _, err := io.ReadFull(f, magic); err != nil || string(magic) != "SQLite format 3\x00" {
		return errors.New("not an SQLite database")
	}
	return nil
}

func readCarryForward(ctx context.Context, sdb *sql.DB) (CarryForward, error) {
	var busy, frames, done int
	if err := sdb.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &done); err != nil {
		return CarryForward{}, err
	}
	if busy != 0 {
		return CarryForward{}, errors.New("checkpoint blocked by another connection")
	}
	have, err := tables(ctx, sdb)
	if err != nil {
		return CarryForward{}, err
	}
	var c CarryForward
	for _, r := range revocable {
		if !have[r.table] {
			continue
		}
		rows, err := sdb.QueryContext(ctx, `SELECT id, `+r.key+`, revoked_at FROM `+r.table+` WHERE revoked_at IS NOT NULL`)
		if err != nil {
			return CarryForward{}, err
		}
		for rows.Next() {
			rv := revocation{table: r.table}
			if err := rows.Scan(&rv.id, &rv.key, &rv.at); err != nil {
				rows.Close()
				return CarryForward{}, err
			}
			c.revoked = append(c.revoked, rv)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return CarryForward{}, err
		}
	}
	if have["used_nonces"] {
		rows, err := sdb.QueryContext(ctx, `SELECT nonce, used_at, expires_at FROM used_nonces`)
		if err != nil {
			return CarryForward{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var n usedNonce
			if err := rows.Scan(&n.nonce, &n.usedAt, &n.expiresAt); err != nil {
				return CarryForward{}, err
			}
			c.nonces = append(c.nonces, n)
		}
		if err := rows.Err(); err != nil {
			return CarryForward{}, err
		}
	}
	return c, nil
}

// ApplyCarryForward prepares the database at path, a copy of a snapshot about
// to replace coachd.db, in one transaction: it applies c's revocations to rows
// still active and adds c's used nonces, deletes every web session and pending
// enrolment so the runner signs in again, and appends audit. It returns how
// many revocations and nonces it added. The file is switched to rollback
// journal mode, so no -wal is left beside it; Open switches it back to WAL.
func ApplyCarryForward(ctx context.Context, path string, c CarryForward, audit AuditEvent) (revocations, nonces int, err error) {
	sdb, err := openPlain(path, "journal_mode(DELETE)", "synchronous(FULL)")
	if err != nil {
		return 0, 0, fmt.Errorf("store: apply carry-forward: %w", err)
	}
	defer sdb.Close()
	revocations, nonces, err = applyCarryForward(ctx, sdb, c, audit)
	if err != nil {
		return 0, 0, fmt.Errorf("store: apply carry-forward: %w", err)
	}
	return revocations, nonces, nil
}

func applyCarryForward(ctx context.Context, sdb *sql.DB, c CarryForward, audit AuditEvent) (revocations, nonces int, err error) {
	tx, err := sdb.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	have, err := tables(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	keys := map[string]string{}
	for _, r := range revocable {
		keys[r.table] = r.key
	}
	for _, rv := range c.revoked {
		if !have[rv.table] {
			continue
		}
		res, err := tx.ExecContext(ctx, `UPDATE `+rv.table+` SET revoked_at=? WHERE id=? AND `+keys[rv.table]+`=? AND revoked_at IS NULL`,
			rv.at, rv.id, rv.key)
		if err != nil {
			return 0, 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, 0, err
		}
		revocations += int(n)
	}
	if have["used_nonces"] {
		for _, u := range c.nonces {
			res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO used_nonces (nonce, used_at, expires_at) VALUES (?,?,?)`,
				u.nonce, u.usedAt, u.expiresAt)
			if err != nil {
				return 0, 0, err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return 0, 0, err
			}
			nonces += int(n)
		}
	}
	for _, d := range []struct{ table, sql string }{
		{"web_sessions", `DELETE FROM web_sessions`},
		{"oauth_tokens", `DELETE FROM oauth_tokens`},
		{"oauth_codes", `DELETE FROM oauth_codes`},
		{"oauth_grants", `DELETE FROM oauth_grants`},
		{"enrollments", `DELETE FROM enrollments WHERE used_at IS NULL`},
	} {
		if have[d.table] {
			if _, err := tx.ExecContext(ctx, d.sql); err != nil {
				return 0, 0, err
			}
		}
	}
	if err := appendAuditTx(ctx, tx, audit); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return revocations, nonces, nil
}
