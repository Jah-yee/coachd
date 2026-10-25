package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/stefiix92/coachd/internal/civil"
)

// Approval is the runner's approval of one proposal, authorised by a passkey
// assertion whose challenge is Nonce.
type Approval struct {
	ID, ContentHash string
	Today           civil.Date // runner's timezone; the proposal's date must be >= Today
	Nonce           string
	NonceExpires    time.Time
	At              time.Time
	Lane, Client    string // actor is "runner"
}

// ApproveResult is the outcome of ApproveProposal.
type ApproveResult struct {
	Proposal Proposal  // after approval
	Replaced *Proposal // the replaced proposal after the transaction; nil without replaces
}

// ApprovalConflict reports why a pending check failed. It matches ErrConflict.
type ApprovalConflict struct {
	Status ProposalStatus
	Reason string // "not_pending" | "content_changed" | "date_passed"
}

func (e *ApprovalConflict) Error() string {
	switch e.Reason {
	case "not_pending":
		return "proposal is " + string(e.Status)
	case "content_changed":
		return "proposal content changed"
	case "date_passed":
		return "proposal date has passed"
	}
	return "approval conflict"
}

// Is reports whether target is ErrConflict.
func (e *ApprovalConflict) Is(target error) bool { return target == ErrConflict }

// ApproveProposal approves a pending proposal in one transaction: it consumes
// the nonce (ErrNonceUsed on replay), checks status, content hash and date
// (*ApprovalConflict, which rolls the nonce back), moves pending to approved
// and settles a replaced proposal (pending becomes superseded; approved,
// delivering, delivery_failed or delivered becomes withdrawn; any other status
// is untouched). Audit rows are written for each change. ErrNotFound when
// the proposal is absent.
func (db *DB) ApproveProposal(ctx context.Context, a Approval) (ApproveResult, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return ApproveResult{}, fmt.Errorf("store: approve proposal: %w", err)
	}
	defer tx.Rollback()
	if err := consumeNonceTx(ctx, tx, a.Nonce, a.At, a.NonceExpires); err != nil {
		return ApproveResult{}, err
	}
	out, err := approveTx(ctx, tx, a.ID, a.ContentHash, a.Today, a.At, a.Lane, a.Client)
	if err != nil {
		return ApproveResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApproveResult{}, fmt.Errorf("store: approve proposal: %w", err)
	}
	return out, nil
}

// ApprovalSet is an approve-all: the proposals to approve in order, each
// with the content hash the runner saw, under one nonce.
type ApprovalSet struct {
	Items        []ApprovalItem
	Today        civil.Date // runner's timezone; every proposal's date must be >= Today
	Nonce        string
	NonceExpires time.Time
	At           time.Time
	Lane, Client string // actor is "runner"
}

// ApprovalItem is one proposal of an ApprovalSet.
type ApprovalItem struct{ ID, ContentHash string }

// SetConflict names the proposal that stopped an approve-all. Err is
// ErrNotFound or an *ApprovalConflict, and errors.Is and errors.As see it.
type SetConflict struct {
	ID  string
	Err error
}

func (e *SetConflict) Error() string { return "proposal " + e.ID + ": " + e.Err.Error() }
func (e *SetConflict) Unwrap() error { return e.Err }

// ApproveProposals approves every proposal of a in one transaction, in
// order, with one nonce: each as ApproveProposal does. The first proposal
// that is absent or fails a check stops it with a *SetConflict, and nothing
// is approved and the nonce is not consumed. ErrNonceUsed on replay.
func (db *DB) ApproveProposals(ctx context.Context, a ApprovalSet) ([]ApproveResult, error) {
	if len(a.Items) == 0 {
		return nil, errors.New("store: approve proposals: empty set")
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: approve proposals: %w", err)
	}
	defer tx.Rollback()
	if err := consumeNonceTx(ctx, tx, a.Nonce, a.At, a.NonceExpires); err != nil {
		return nil, err
	}
	out := make([]ApproveResult, 0, len(a.Items))
	for _, it := range a.Items {
		r, err := approveTx(ctx, tx, it.ID, it.ContentHash, a.Today, a.At, a.Lane, a.Client)
		var conflict *ApprovalConflict
		if errors.Is(err, ErrNotFound) || errors.As(err, &conflict) {
			return nil, &SetConflict{ID: it.ID, Err: err}
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: approve proposals: %w", err)
	}
	return out, nil
}

// approveTx is one approval inside tx, after the nonce is consumed: the
// checks, the move to approved, the replaced proposal and the audit rows.
func approveTx(ctx context.Context, tx *sql.Tx, id, hash string, today civil.Date, at time.Time, lane, client string) (ApproveResult, error) {
	p, err := scanProposal(tx.QueryRowContext(ctx, `SELECT `+proposalCols+` FROM proposals WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ApproveResult{}, ErrNotFound
	}
	if err != nil {
		return ApproveResult{}, fmt.Errorf("store: approve proposal: %w", err)
	}
	switch {
	case p.Status != ProposalPending:
		return ApproveResult{}, &ApprovalConflict{Status: p.Status, Reason: "not_pending"}
	case p.ContentHash != hash:
		return ApproveResult{}, &ApprovalConflict{Status: p.Status, Reason: "content_changed"}
	case p.Date.String() < today.String():
		return ApproveResult{}, &ApprovalConflict{Status: p.Status, Reason: "date_passed"}
	}
	res, err := tx.ExecContext(ctx, `UPDATE proposals SET status='approved', status_at=? WHERE id=? AND status='pending'`, fmtTime(at), id)
	if err != nil {
		return ApproveResult{}, fmt.Errorf("store: approve proposal: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return ApproveResult{}, err
	} else if n == 0 {
		return ApproveResult{}, &ApprovalConflict{Status: p.Status, Reason: "not_pending"}
	}
	audit := func(event, target string) error {
		return appendAuditTx(ctx, tx, AuditEvent{At: at, Lane: lane, Actor: "runner", Client: client,
			Event: event, Target: target, Outcome: "ok"})
	}
	if err := audit("proposal_approved", id); err != nil {
		return ApproveResult{}, fmt.Errorf("store: approve audit: %w", err)
	}
	out := ApproveResult{}
	if p.Replaces != nil {
		old, err := scanProposal(tx.QueryRowContext(ctx, `SELECT `+proposalCols+` FROM proposals WHERE id=?`, *p.Replaces))
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return ApproveResult{}, fmt.Errorf("store: read replaced proposal: %w", err)
		default:
			to := ProposalStatus("")
			switch old.Status {
			case ProposalPending:
				to = ProposalSuperseded
			case ProposalApproved, ProposalDelivering, ProposalDeliveryFailed, ProposalDelivered:
				to = ProposalWithdrawn
			}
			if to != "" {
				if _, err := tx.ExecContext(ctx, `UPDATE proposals SET status=?, status_at=? WHERE id=? AND status=?`,
					string(to), fmtTime(at), old.ID, string(old.Status)); err != nil {
					return ApproveResult{}, fmt.Errorf("store: settle replaced proposal: %w", err)
				}
				if err := audit("proposal_"+string(to), old.ID); err != nil {
					return ApproveResult{}, fmt.Errorf("store: replaced audit: %w", err)
				}
				old.Status, old.StatusAt = to, at
			}
			out.Replaced = &old
		}
	}
	if out.Proposal, err = scanProposal(tx.QueryRowContext(ctx, `SELECT `+proposalCols+` FROM proposals WHERE id=?`, id)); err != nil {
		return ApproveResult{}, fmt.Errorf("store: read proposal: %w", err)
	}
	return out, nil
}

// UpdateRunnerNote sets category, text and updated_at of a note and marks it
// created_by "runner", because the runner now vouches for the text. It returns
// ErrNotFound when the note is absent.
func (db *DB) UpdateRunnerNote(ctx context.Context, n Note) (Note, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return Note{}, fmt.Errorf("store: update runner note: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE notes SET category=?, text=?, created_by='runner', updated_at=? WHERE id=?`,
		string(n.Category), n.Text, fmtTime(n.UpdatedAt), n.ID)
	if err != nil {
		return Note{}, fmt.Errorf("store: update runner note: %w", err)
	}
	if c, err := res.RowsAffected(); err != nil {
		return Note{}, err
	} else if c == 0 {
		return Note{}, ErrNotFound
	}
	out, err := scanNote(tx.QueryRowContext(ctx, `SELECT `+noteCols+` FROM notes WHERE id=?`, n.ID))
	if err != nil {
		return Note{}, fmt.Errorf("store: read note: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Note{}, fmt.Errorf("store: update runner note: %w", err)
	}
	return out, nil
}

// ReplacementsOf returns the proposals whose replaces is id, by creation time.
func (db *DB) ReplacementsOf(ctx context.Context, id string) ([]Proposal, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+proposalCols+` FROM proposals WHERE replaces=? ORDER BY created_at, id`, id)
	if err != nil {
		return nil, fmt.Errorf("store: replacements of: %w", err)
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan proposal: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProposalCounts returns the number of proposals per status; every status in
// ProposalStatuses is present, zero included.
func (db *DB) ProposalCounts(ctx context.Context) (map[ProposalStatus]int, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT status, count(*) FROM proposals GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("store: proposal counts: %w", err)
	}
	defer rows.Close()
	out := make(map[ProposalStatus]int, len(ProposalStatuses))
	for _, s := range ProposalStatuses {
		out[s] = 0
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, fmt.Errorf("store: proposal counts: %w", err)
		}
		out[ProposalStatus(s)] = n
	}
	return out, rows.Err()
}

// ListAudit returns the newest audit events first; limit is clamped to 1..500.
func (db *DB) ListAudit(ctx context.Context, limit int) ([]AuditEvent, error) {
	limit = min(max(limit, 1), 500)
	rows, err := db.sql.QueryContext(ctx, `SELECT at, lane, actor, client, event, target, outcome FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var at string
		if err := rows.Scan(&at, &e.Lane, &e.Actor, &e.Client, &e.Event, &e.Target, &e.Outcome); err != nil {
			return nil, fmt.Errorf("store: scan audit: %w", err)
		}
		if e.At, err = parseTime(at); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
