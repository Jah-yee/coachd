package store

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// AuditQueue appends audit events off the caller's path so read tools never
// wait on the SQLite write lock or an fsync. A full queue or a closed queue
// falls back to a synchronous write. No event is lost on a clean shutdown
// (Close drains the queue); queued events can be lost on a hard kill or when
// Close times out.
type AuditQueue struct {
	db   *DB
	log  *slog.Logger
	mu   sync.RWMutex // guards closed and sends on ch
	ch   chan AuditEvent
	done chan struct{}

	closed bool
}

// NewAuditQueue starts one writer goroutine. Call Close to drain it.
func NewAuditQueue(db *DB, log *slog.Logger, size int) *AuditQueue {
	if size < 1 {
		size = 1
	}
	q := &AuditQueue{db: db, log: log.With("component", "auditq"), ch: make(chan AuditEvent, size), done: make(chan struct{})}
	go q.run()
	return q
}

func (q *AuditQueue) run() {
	defer close(q.done)
	for e := range q.ch {
		if err := q.db.AppendAudit(context.Background(), e); err != nil {
			q.log.Warn("audit write failed", "error_class", fmt.Sprintf("%T", err), "error", err)
		}
	}
}

// AppendAudit enqueues e. If the queue is full or closed it writes synchronously.
func (q *AuditQueue) AppendAudit(ctx context.Context, e AuditEvent) error {
	q.mu.RLock()
	if !q.closed {
		select {
		case q.ch <- e:
			q.mu.RUnlock()
			return nil
		default:
		}
	}
	q.mu.RUnlock()
	return q.db.AppendAudit(ctx, e)
}

// Close stops accepting events, drains the queue and waits for the writer,
// bounded by ctx. It is safe to call more than once.
func (q *AuditQueue) Close(ctx context.Context) error {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.ch)
	}
	q.mu.Unlock()
	select {
	case <-q.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("store: audit queue drain: %w", ctx.Err())
	}
}
