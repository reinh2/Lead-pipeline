// Package retry provides bounded, context-aware retries for transient calls to
// external systems. Permanent failures are returned immediately.
package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"time"
)

// HTTPStatusError preserves an upstream response status so callers can make a
// retry decision without parsing an error string.
type HTTPStatusError struct {
	Status int
	Body   string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("upstream HTTP %d: %s", e.Status, e.Body)
}

// Policy controls a retry loop. Zero values use conservative defaults.
type Policy struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	// Jitter can be supplied by tests. The default randomizes each delay by
	// +/- 20 percent to avoid synchronized retries across workers.
	Jitter func(time.Duration) time.Duration
}

var DefaultPolicy = Policy{
	MaxAttempts:  3,
	InitialDelay: 250 * time.Millisecond,
	MaxDelay:     3 * time.Second,
}

// Do calls operation at most MaxAttempts times. It retries only network and
// rate-limit/server failures and never waits past ctx cancellation.
func Do(ctx context.Context, policy Policy, operation func(context.Context) error) error {
	policy = normalized(policy)
	var lastErr error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = operation(ctx)
		if lastErr == nil || !Transient(lastErr) || attempt == policy.MaxAttempts {
			return lastErr
		}
		if err := wait(ctx, delay(policy, attempt)); err != nil {
			return err
		}
	}
	return lastErr
}

// Transient reports whether retrying an error can reasonably succeed without
// changing request data or credentials.
func Transient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return status.Status == 408 || status.Status == 425 || status.Status == 429 || status.Status >= 500
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

func normalized(p Policy) Policy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = DefaultPolicy.MaxAttempts
	}
	if p.InitialDelay <= 0 {
		p.InitialDelay = DefaultPolicy.InitialDelay
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = DefaultPolicy.MaxDelay
	}
	if p.Jitter == nil {
		p.Jitter = func(d time.Duration) time.Duration {
			return d * time.Duration(80+rand.Intn(41)) / 100
		}
	}
	return p
}

func delay(p Policy, attempt int) time.Duration {
	d := p.InitialDelay
	for i := 1; i < attempt && d < p.MaxDelay; i++ {
		d *= 2
	}
	if d > p.MaxDelay {
		d = p.MaxDelay
	}
	return p.Jitter(d)
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
