package queue

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
)

func TestQueueCloseDrainsAcceptedLeads(t *testing.T) {
	q := New(3, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var mu sync.Mutex
	var processed []string
	q.Run(func(_ context.Context, lead models.Lead) {
		mu.Lock()
		processed = append(processed, lead.ID)
		mu.Unlock()
	})
	for _, id := range []string{"ld-1", "ld-2", "ld-3"} {
		if !q.Submit(models.Lead{ID: id}) {
			t.Fatalf("Submit(%s) = false", id)
		}
	}
	q.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := q.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	sort.Strings(processed)
	if got, want := processed, []string{"ld-1", "ld-2", "ld-3"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("processed = %v, want %v", got, want)
	}
}

func TestQueueIsBoundedAndRejectsAfterClose(t *testing.T) {
	q := New(1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !q.Submit(models.Lead{ID: "ld-1"}) {
		t.Fatal("first submit should fit")
	}
	if q.Submit(models.Lead{ID: "ld-2"}) {
		t.Fatal("full queue must reject instead of spawning background work")
	}
	q.Close()
	if q.Submit(models.Lead{ID: "ld-3"}) {
		t.Fatal("closed queue must reject new work")
	}
}
