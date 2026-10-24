package store

import (
	"context"
	"fmt"
	"time"
)

func (db *DB) AppendAudit(ctx context.Context, e AuditEvent) error {
	_, err := db.sql.ExecContext(ctx, `INSERT INTO audit_log (at, lane, actor, client, event, target, outcome)
		VALUES (?,?,?,?,?,?,?)`, fmtTime(e.At), e.Lane, e.Actor, e.Client, e.Event, e.Target, e.Outcome)
	if err != nil {
		return fmt.Errorf("store: append audit: %w", err)
	}
	return nil
}

// PurgeAudit deletes events strictly before the given time and returns how many were removed.
func (db *DB) PurgeAudit(ctx context.Context, before time.Time) (int64, error) {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM audit_log WHERE at < ?`, fmtTime(before))
	if err != nil {
		return 0, fmt.Errorf("store: purge audit: %w", err)
	}
	return res.RowsAffected()
}
