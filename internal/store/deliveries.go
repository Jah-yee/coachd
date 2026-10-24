package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/stefiix92/coachd/internal/civil"
)

// DesiredStatuses are the statuses a watch keeps on the device.
var DesiredStatuses = []ProposalStatus{ProposalApproved, ProposalDelivering, ProposalDeliveryFailed, ProposalDelivered}

const desiredIn = `('approved','delivering','delivery_failed','delivered')`

// StatusConflict reports the status that blocked a delivery step. It matches ErrConflict.
type StatusConflict struct{ Status ProposalStatus }

func (e *StatusConflict) Error() string { return "workout is " + string(e.Status) }

// Is reports whether target is ErrConflict.
func (e *StatusConflict) Is(target error) bool { return target == ErrConflict }

// DesiredWorkouts returns proposals in DesiredStatuses dated from..to, ordered
// by date, created_at then id, at most limit.
func (db *DB) DesiredWorkouts(ctx context.Context, from, to civil.Date, limit int) ([]Proposal, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+proposalCols+` FROM proposals WHERE status IN `+desiredIn+`
		AND date >= ? AND date <= ? ORDER BY date, created_at, id LIMIT ?`, from.String(), to.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: desired workouts: %w", err)
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, fmt.Errorf("store: desired workouts: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func deviceActor(id int64) string { return "device:" + strconv.FormatInt(id, 10) }

func deliveryAudit(ctx context.Context, tx *sql.Tx, at time.Time, deviceID int64, event, target string) error {
	return appendAuditTx(ctx, tx, AuditEvent{At: at, Lane: "public", Actor: deviceActor(deviceID), Client: deviceActor(deviceID),
		Event: event, Target: target, Outcome: "ok"})
}

func inDesired(s ProposalStatus) bool {
	for _, d := range DesiredStatuses {
		if d == s {
			return true
		}
	}
	return false
}

// RecordFetch records that the device downloaded the proposal and moves
// approved or delivery_failed to delivering. ErrNotFound when absent;
// *StatusConflict when the status is outside DesiredStatuses.
func (db *DB) RecordFetch(ctx context.Context, proposalID string, deviceID int64, at time.Time) (Proposal, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return Proposal{}, fmt.Errorf("store: record fetch: %w", err)
	}
	defer tx.Rollback()
	p, err := scanProposal(tx.QueryRowContext(ctx, `SELECT `+proposalCols+` FROM proposals WHERE id=?`, proposalID))
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, ErrNotFound
	}
	if err != nil {
		return Proposal{}, fmt.Errorf("store: record fetch: %w", err)
	}
	if !inDesired(p.Status) {
		return Proposal{}, &StatusConflict{Status: p.Status}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deliveries (proposal_id, device_id, fetched_at, acked_at) VALUES (?,?,?,NULL)
		ON CONFLICT(proposal_id, device_id) DO UPDATE SET fetched_at=excluded.fetched_at, acked_at=NULL`,
		proposalID, deviceID, fmtTime(at)); err != nil {
		return Proposal{}, fmt.Errorf("store: record fetch: %w", err)
	}
	if p.Status == ProposalApproved || p.Status == ProposalDeliveryFailed {
		if _, err := tx.ExecContext(ctx, `UPDATE proposals SET status='delivering', status_at=? WHERE id=?`, fmtTime(at), proposalID); err != nil {
			return Proposal{}, fmt.Errorf("store: record fetch: %w", err)
		}
		if err := deliveryAudit(ctx, tx, at, deviceID, "proposal_delivering", proposalID); err != nil {
			return Proposal{}, fmt.Errorf("store: record fetch audit: %w", err)
		}
		p.Status, p.StatusAt = ProposalDelivering, at
	}
	if err := tx.Commit(); err != nil {
		return Proposal{}, fmt.Errorf("store: record fetch: %w", err)
	}
	return p, nil
}

// AckDelivery records the device's acknowledgement and moves delivering or
// delivery_failed to delivered. ErrNotFound without a fetch row for the pair;
// delivered is a no-op; any other status is a *StatusConflict.
func (db *DB) AckDelivery(ctx context.Context, proposalID string, deviceID int64, at time.Time) (Proposal, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return Proposal{}, fmt.Errorf("store: ack delivery: %w", err)
	}
	defer tx.Rollback()
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM deliveries WHERE proposal_id=? AND device_id=?`, proposalID, deviceID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, ErrNotFound
	}
	if err != nil {
		return Proposal{}, fmt.Errorf("store: ack delivery: %w", err)
	}
	p, err := scanProposal(tx.QueryRowContext(ctx, `SELECT `+proposalCols+` FROM proposals WHERE id=?`, proposalID))
	if err != nil {
		return Proposal{}, fmt.Errorf("store: ack delivery: %w", err)
	}
	switch p.Status {
	case ProposalDelivered:
		return p, nil
	case ProposalDelivering, ProposalDeliveryFailed:
	default:
		return Proposal{}, &StatusConflict{Status: p.Status}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET acked_at=? WHERE proposal_id=? AND device_id=?`, fmtTime(at), proposalID, deviceID); err != nil {
		return Proposal{}, fmt.Errorf("store: ack delivery: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE proposals SET status='delivered', status_at=? WHERE id=?`, fmtTime(at), proposalID); err != nil {
		return Proposal{}, fmt.Errorf("store: ack delivery: %w", err)
	}
	if err := deliveryAudit(ctx, tx, at, deviceID, "proposal_delivered", proposalID); err != nil {
		return Proposal{}, fmt.Errorf("store: ack delivery audit: %w", err)
	}
	p.Status, p.StatusAt = ProposalDelivered, at
	if err := tx.Commit(); err != nil {
		return Proposal{}, fmt.Errorf("store: ack delivery: %w", err)
	}
	return p, nil
}

// FailStaleDeliveries moves delivering proposals with no acked row and a latest
// fetch before fetchedBefore to delivery_failed, one system audit row each. It
// returns the count.
func (db *DB) FailStaleDeliveries(ctx context.Context, fetchedBefore, at time.Time) (int, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: fail stale deliveries: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT p.id FROM proposals p WHERE p.status='delivering'
		AND NOT EXISTS (SELECT 1 FROM deliveries d WHERE d.proposal_id=p.id AND d.acked_at IS NOT NULL)
		AND (SELECT max(d.fetched_at) FROM deliveries d WHERE d.proposal_id=p.id) < ? ORDER BY p.date, p.id`, fmtTime(fetchedBefore))
	if err != nil {
		return 0, fmt.Errorf("store: fail stale deliveries: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, `UPDATE proposals SET status='delivery_failed', status_at=? WHERE id=? AND status='delivering'`, fmtTime(at), id)
		if err != nil {
			return 0, fmt.Errorf("store: fail stale delivery: %w", err)
		}
		if c, err := res.RowsAffected(); err != nil {
			return 0, err
		} else if c != 1 {
			continue
		}
		if err := appendAuditTx(ctx, tx, AuditEvent{At: at, Lane: "system", Actor: "system", Event: "proposal_delivery_failed", Target: id, Outcome: "ok"}); err != nil {
			return 0, fmt.Errorf("store: delivery failed audit: %w", err)
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: fail stale deliveries: %w", err)
	}
	return n, nil
}
