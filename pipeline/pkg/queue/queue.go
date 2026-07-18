package queue

import (
	"context"
	"log/slog"
	"sync"

	"github.com/anker-relocation/pipeline/pkg/models"
)

// Handler processes one lead. Must be idempotent and non-blocking on external
// deps for too long — the queue has a single worker by default.
type Handler func(ctx context.Context, lead models.Lead)

// Queue is an in-memory buffered channel with a single worker. Enough for the
// scope of this project; swap for NATS/SQS when the real thing needs it.
type Queue struct {
	ch   chan models.Lead
	log  *slog.Logger
	wg   sync.WaitGroup
	stop chan struct{}
}

func New(buf int, log *slog.Logger) *Queue {
	return &Queue{
		ch:   make(chan models.Lead, buf),
		log:  log,
		stop: make(chan struct{}),
	}
}

// Submit never blocks; if the buffer is full the lead is dropped-to-log and
// the caller is expected to have already written it to the CRM sink first.
func (q *Queue) Submit(l models.Lead) bool {
	select {
	case q.ch <- l:
		return true
	default:
		q.log.Warn("queue full, dropping lead from pipeline", "lead_id", l.ID)
		return false
	}
}

func (q *Queue) Run(ctx context.Context, h Handler) {
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case lead := <-q.ch:
				func() {
					defer func() {
						if r := recover(); r != nil {
							q.log.Error("worker panic", "lead_id", lead.ID, "panic", r)
						}
					}()
					h(ctx, lead)
				}()
			}
		}
	}()
}

func (q *Queue) Wait() { q.wg.Wait() }
