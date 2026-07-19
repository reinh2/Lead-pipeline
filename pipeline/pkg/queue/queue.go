package queue

import (
	"context"
	"log/slog"
	"sync"

	"github.com/anker-relocation/pipeline/pkg/models"
)

// Handler processes one lead. It must be idempotent: a persistent fallback
// keeps records that cannot enter this in-memory demo queue.
type Handler func(ctx context.Context, lead models.Lead)

// Queue is a bounded, in-memory worker queue. It deliberately refuses new
// work when full instead of spawning unbounded goroutines. Call Close before
// Wait to drain every lead that was accepted by Submit.
type Queue struct {
	ch  chan models.Lead
	log *slog.Logger

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func New(buf int, log *slog.Logger) *Queue {
	return &Queue{ch: make(chan models.Lead, buf), log: log}
}

// Submit never blocks. A false result means the lead must remain in durable
// fallback storage and the caller should return a controlled unavailable error.
func (q *Queue) Submit(l models.Lead) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	select {
	case q.ch <- l:
		return true
	default:
		q.log.Warn("queue full, lead retained only in fallback storage", "lead_id", l.ID)
		return false
	}
}

// Run starts a single bounded worker. The worker exits only after Close has
// closed the channel and it has processed all already accepted leads.
func (q *Queue) Run(h Handler) {
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		for lead := range q.ch {
			func() {
				defer func() {
					if r := recover(); r != nil {
						q.log.Error("worker panic", "lead_id", lead.ID, "panic", r)
					}
				}()
				h(context.Background(), lead)
			}()
		}
	}()
}

// Close prevents future submissions and lets the worker drain its buffer.
func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	close(q.ch)
}

// Len reports the current number of leads waiting in the bounded buffer.
func (q *Queue) Len() int { return len(q.ch) }

// Ready reports whether the queue can still accept work.
func (q *Queue) Ready() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return !q.closed
}

// Wait blocks until draining is complete or the shutdown context expires.
func (q *Queue) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
