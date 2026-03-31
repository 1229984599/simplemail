package middleware

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// rateLimitEntry tracks request counts for a single key.
type rateLimitEntry struct {
	count   int64
	resetAt time.Time
}

// InMemoryRateLimiter implements rate limiting using an in-memory map.
// For lightweight/single-instance deployments, this replaces Redis.
type InMemoryRateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rateLimitEntry
	limit   int64
	window  time.Duration
}

func NewInMemoryRateLimiter(limit int, windowSeconds int) *InMemoryRateLimiter {
	rl := &InMemoryRateLimiter{
		entries: make(map[string]*rateLimitEntry),
		limit:   int64(limit),
		window:  time.Duration(windowSeconds) * time.Second,
	}
	// Background cleanup of expired entries every 5 minutes
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			rl.cleanup()
		}
	}()
	return rl
}

// Allow checks if a request should be allowed and returns remaining count.
func (rl *InMemoryRateLimiter) Allow(key string) (allowed bool, remaining int64, resetAt time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	entry, exists := rl.entries[key]
	if !exists || now.After(entry.resetAt) {
		// New window
		rl.entries[key] = &rateLimitEntry{
			count:   1,
			resetAt: now.Add(rl.window),
		}
		return true, rl.limit - 1, now.Add(rl.window)
	}

	entry.count++
	remaining = rl.limit - entry.count
	if remaining < 0 {
		remaining = 0
	}

	return entry.count <= rl.limit, remaining, entry.resetAt
}

func (rl *InMemoryRateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	for key, entry := range rl.entries {
		if now.After(entry.resetAt) {
			delete(rl.entries, key)
		}
	}
}

// RateLimit returns a Gin middleware using in-memory rate limiting.
func RateLimit(rl *InMemoryRateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Use API Key as rate limit key
		key := c.GetHeader("Authorization")
		if key == "" {
			key = c.Query("api_key")
		}
		if key == "" {
			key = c.ClientIP()
		}

		allowed, remaining, resetAt := rl.Allow(key)

		c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", rl.limit))
		c.Header("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
		c.Header("X-RateLimit-Reset", fmt.Sprintf("%d", resetAt.Unix()))

		if !allowed {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error":       "rate limit exceeded",
				"limit":       rl.limit,
				"retry_after": int(rl.window.Seconds()),
			})
			return
		}

		c.Next()
	}
}
