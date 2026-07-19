package retry

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func noJitter(d time.Duration) time.Duration { return d }

func TestDoRetriesOnlyTransientErrors(t *testing.T) {
	attempts := 0
	err := Do(context.Background(), Policy{MaxAttempts: 3, InitialDelay: time.Millisecond, Jitter: noJitter}, func(context.Context) error {
		attempts++
		if attempts < 3 {
			return &HTTPStatusError{Status: 503}
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("retry result = (%v, %d attempts), want (nil, 3)", err, attempts)
	}

	attempts = 0
	err = Do(context.Background(), Policy{MaxAttempts: 3, InitialDelay: time.Millisecond, Jitter: noJitter}, func(context.Context) error {
		attempts++
		return &HTTPStatusError{Status: 400}
	})
	if err == nil || attempts != 1 {
		t.Fatalf("permanent error = (%v, %d attempts), want one attempt", err, attempts)
	}
}

func TestDoHonorsCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := Do(ctx, Policy{MaxAttempts: 3, InitialDelay: time.Second, Jitter: noJitter}, func(context.Context) error {
		attempts++
		cancel()
		return &net.DNSError{IsTemporary: true}
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("result = (%v, %d attempts), want canceled after first attempt", err, attempts)
	}
}

func TestTransient(t *testing.T) {
	if !Transient(&HTTPStatusError{Status: 429}) || !Transient(&HTTPStatusError{Status: 500}) {
		t.Fatal("429 and 5xx must be transient")
	}
	if Transient(&HTTPStatusError{Status: 401}) || Transient(context.Canceled) {
		t.Fatal("authentication and cancellation must not be transient")
	}
}
