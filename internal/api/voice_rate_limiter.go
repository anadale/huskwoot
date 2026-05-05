package api

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type deviceLimiterEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// voiceRateLimiter is an in-memory per-device token-bucket rate limiter for
// POST /v1/chat/voice. Keyed by device ID (not IP), so different devices are
// rate-limited independently.
type voiceRateLimiter struct {
	mu        sync.Mutex
	limiters  map[string]*deviceLimiterEntry
	limit     rate.Limit
	burst     int
	perMinute int
}

func newVoiceRateLimiter(perMinute int) *voiceRateLimiter {
	if perMinute <= 0 {
		perMinute = 10
	}
	return &voiceRateLimiter{
		limiters:  make(map[string]*deviceLimiterEntry),
		limit:     rate.Every(time.Minute / time.Duration(perMinute)),
		burst:     perMinute,
		perMinute: perMinute,
	}
}

// RetryAfterSeconds returns the approximate number of seconds a client should
// wait before retrying after hitting the rate limit.
func (l *voiceRateLimiter) RetryAfterSeconds() int {
	// Ceiling of 60/perMinute gives the token refill interval in seconds.
	return (60 + l.perMinute - 1) / l.perMinute
}

// Allow reports whether a request from the given device is permitted.
func (l *voiceRateLimiter) Allow(deviceID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.limiters[deviceID]
	if !ok {
		entry = &deviceLimiterEntry{
			limiter: rate.NewLimiter(l.limit, l.burst),
		}
		l.limiters[deviceID] = entry
	}
	entry.lastUsed = time.Now()
	return entry.limiter.Allow()
}

// Sweep removes idle device entries every interval; stops when ctx is cancelled.
func (l *voiceRateLimiter) Sweep(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.mu.Lock()
			for deviceID, entry := range l.limiters {
				if time.Since(entry.lastUsed) > time.Hour {
					delete(l.limiters, deviceID)
				}
			}
			l.mu.Unlock()
		}
	}
}
