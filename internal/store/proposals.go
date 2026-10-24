package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stefiix92/coachd/internal/civil"
)

const proposalCols = `id, date, sport, name, content_json, content_hash, rationale, replaces, warnings_json,
	status, status_at, created_at, created_lane, created_by, created_client, trace_id, span_id`

func scanProposal(s scanner) (Proposal, error) {
	var p Proposal
	var date, sport, content, warnings, status, statusAt, createdAt string
	var replaces sql.NullString
	if err := s.Scan(&p.ID, &date, &sport, &p.Name, &content, &p.ContentHash, &p.Rationale, &replaces, &warnings,
		&status, &statusAt, &createdAt, &p.CreatedLane, &p.CreatedBy, &p.CreatedClient, &p.TraceID, &p.SpanID); err != nil {
		return p, err
	}
	var err error
	if p.Date, err = civil.Parse(date); err != nil {
		return p, err
	}
	p.Sport = Sport(sport)
	p.ContentJSON = []byte(content)
	p.WarningsJSON = []byte(warnings)
	p.Status = ProposalStatus(status)
	if replaces.Valid {
		r := replaces.String
		p.Replaces = &r
	}
	if p.StatusAt, err = parseTime(statusAt); err != nil {
		return p, err
	}
	p.CreatedAt, err = parseTime(createdAt)
	return p, err
}

func appendAuditTx(ctx context.Context, tx *sql.Tx, e AuditEvent) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_log (at, lane, actor, client, event, target, outcome)
		VALUES (?,?,?,?,?,?,?)`, fmtTime(e.At), e.Lane, e.Actor, e.Client, e.Event, e.Target, e.Outcome)
	return err
}

// InsertProposal stores p and its audit row in one transaction.
func (db *DB) InsertProposal(ctx context.Context, p Proposal, audit AuditEvent) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: insert proposal: %w", err)
	}
	defer tx.Rollback()
	warnings := p.WarningsJSON
	if len(warnings) == 0 {
		warnings = []byte("[]")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO proposals (`+proposalCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Date.String(), string(p.Sport), p.Name, string(p.ContentJSON), p.ContentHash, p.Rationale, p.Replaces,
		string(warnings), string(p.Status), fmtTime(p.StatusAt), fmtTime(p.CreatedAt), p.CreatedLane, p.CreatedBy, p.CreatedClient, p.TraceID, p.SpanID)
	if err != nil {
		return fmt.Errorf("store: insert proposal: %w", err)
	}
	if err := appendAuditTx(ctx, tx, audit); err != nil {
		return fmt.Errorf("store: insert proposal audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: insert proposal: %w", err)
	}
	return nil
}

// GetProposal returns the proposal or ErrNotFound.
func (db *DB) GetProposal(ctx context.Context, id string) (Proposal, error) {
	p, err := scanProposal(db.sql.QueryRowContext(ctx, `SELECT `+proposalCols+` FROM proposals WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, ErrNotFound
	}
	if err != nil {
		return Proposal{}, fmt.Errorf("store: get proposal: %w", err)
	}
	return p, nil
}

// ListProposals returns proposals with from <= date <= to, ordered by date then
// creation time. An empty statuses slice matches every status.
func (db *DB) ListProposals(ctx context.Context, from, to civil.Date, statuses []ProposalStatus) ([]Proposal, error) {
	q := `SELECT ` + proposalCols + ` FROM proposals WHERE date >= ? AND date <= ?`
	args := []any{from.String(), to.String()}
	if len(statuses) > 0 {
		q += ` AND status IN (?` + strings.Repeat(",?", len(statuses)-1) + `)`
		for _, s := range statuses {
			args = append(args, string(s))
		}
	}
	q += ` ORDER BY date, created_at, id`
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list proposals: %w", err)
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

// PendingProposals returns at most limit pending proposals' id, date and
// name, ordered by date, creation time, then id.
func (db *DB) PendingProposals(ctx context.Context, limit int) ([]ProposalSummary, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT id, date, name FROM proposals WHERE status=?
		ORDER BY date, created_at, id LIMIT ?`, string(ProposalPending), limit)
	if err != nil {
		return nil, fmt.Errorf("store: pending proposals: %w", err)
	}
	defer rows.Close()
	var out []ProposalSummary
	for rows.Next() {
		var p ProposalSummary
		var date string
		if err := rows.Scan(&p.ID, &date, &p.Name); err != nil {
			return nil, fmt.Errorf("store: scan pending proposal: %w", err)
		}
		if p.Date, err = civil.Parse(date); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// TransitionProposal moves a proposal to t.To if its status is in t.From (compare-and-swap)
// and writes the audit row "proposal_<to>" in the same transaction. It returns
// ErrNotFound when the id is absent and ErrConflict when the status is not in From.
func (db *DB) TransitionProposal(ctx context.Context, id string, t Transition) (Proposal, error) {
	if len(t.From) == 0 {
		return Proposal{}, ErrConflict
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return Proposal{}, fmt.Errorf("store: transition proposal: %w", err)
	}
	defer tx.Rollback()
	args := []any{string(t.To), fmtTime(t.At), id}
	for _, s := range t.From {
		args = append(args, string(s))
	}
	res, err := tx.ExecContext(ctx, `UPDATE proposals SET status=?, status_at=? WHERE id=? AND status IN (?`+strings.Repeat(",?", len(t.From)-1)+`)`, args...)
	if err != nil {
		return Proposal{}, fmt.Errorf("store: transition proposal: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Proposal{}, err
	}
	if n == 0 {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM proposals WHERE id=?`, id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return Proposal{}, ErrNotFound
		}
		if err != nil {
			return Proposal{}, fmt.Errorf("store: transition proposal: %w", err)
		}
		return Proposal{}, ErrConflict
	}
	if err := appendAuditTx(ctx, tx, AuditEvent{At: t.At, Lane: t.Lane, Actor: t.Actor, Client: t.Client,
		Event: "proposal_" + string(t.To), Target: id, Outcome: "ok"}); err != nil {
		return Proposal{}, fmt.Errorf("store: transition audit: %w", err)
	}
	p, err := scanProposal(tx.QueryRowContext(ctx, `SELECT `+proposalCols+` FROM proposals WHERE id=?`, id))
	if err != nil {
		return Proposal{}, fmt.Errorf("store: read proposal: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Proposal{}, fmt.Errorf("store: transition proposal: %w", err)
	}
	return p, nil
}

// ExpireProposals moves pending, approved, delivering or delivery_failed proposals dated before `before` to
// expired, one audit row each (Lane and Actor "system"). It returns the count.
func (db *DB) ExpireProposals(ctx context.Context, before civil.Date, at time.Time) (int, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: expire proposals: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM proposals WHERE status IN ('pending','approved','delivering','delivery_failed') AND date < ? ORDER BY date, id`, before.String())
	if err != nil {
		return 0, fmt.Errorf("store: expire proposals: %w", err)
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
		res, err := tx.ExecContext(ctx, `UPDATE proposals SET status='expired', status_at=? WHERE id=? AND status IN ('pending','approved','delivering','delivery_failed')`, fmtTime(at), id)
		if err != nil {
			return 0, fmt.Errorf("store: expire proposal: %w", err)
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		if changed != 1 {
			continue
		}
		if err := appendAuditTx(ctx, tx, AuditEvent{At: at, Lane: "system", Actor: "system", Event: "proposal_expired", Target: id, Outcome: "ok"}); err != nil {
			return 0, fmt.Errorf("store: expire audit: %w", err)
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: expire proposals: %w", err)
	}
	return n, nil
}
