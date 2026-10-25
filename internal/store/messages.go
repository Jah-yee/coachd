package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// MaxMessages is how many agent messages are kept; older ones are deleted.
const MaxMessages = 20

// Message is a short text an agent sent to the runner's watch.
type Message struct {
	ID        int64
	Text      string
	CreatedBy string // agent:<lane>:<client>
	CreatedAt time.Time
}

const messageCols = `id, text, created_by, created_at`

func scanMessage(s scanner) (Message, error) {
	var m Message
	var created string
	if err := s.Scan(&m.ID, &m.Text, &m.CreatedBy, &created); err != nil {
		return m, err
	}
	var err error
	m.CreatedAt, err = parseTime(created)
	return m, err
}

// InsertMessage stores m (m.ID must be 0) and deletes all but the newest
// MaxMessages, in one transaction. The stored message is returned.
func (db *DB) InsertMessage(ctx context.Context, m Message) (Message, error) {
	if m.ID != 0 {
		return Message{}, errors.New("store: insert message: id must be 0")
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, fmt.Errorf("store: insert message: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO messages (text, created_by, created_at) VALUES (?,?,?)`,
		m.Text, m.CreatedBy, fmtTime(m.CreatedAt))
	if err != nil {
		return Message{}, fmt.Errorf("store: insert message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Message{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id NOT IN (SELECT id FROM messages ORDER BY id DESC LIMIT ?)`, MaxMessages); err != nil {
		return Message{}, fmt.Errorf("store: trim messages: %w", err)
	}
	out, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages WHERE id=?`, id))
	if err != nil {
		return Message{}, fmt.Errorf("store: read message: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("store: insert message: %w", err)
	}
	return out, nil
}

// RecentMessages returns up to limit messages created at or after since,
// newest first.
func (db *DB) RecentMessages(ctx context.Context, since time.Time, limit int) ([]Message, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+messageCols+` FROM messages WHERE created_at >= ? ORDER BY id DESC LIMIT ?`,
		fmtTime(since), limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent messages: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("store: recent messages: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
