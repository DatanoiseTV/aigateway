package middleware

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"ai-gateway/internal/models"

	"github.com/patrickmn/go-cache"
)

type RateLimiter struct {
	cache     *cache.Cache
	rateLimit sync.Map
}

type clientLimits struct {
	minute *tokenBucket
	hour   *tokenBucket
	day    *tokenBucket
}

type tokenBucket struct {
	capacity   int
	tokens     int
	lastRefill time.Time
	mu         sync.Mutex
}

func newTokenBucket(capacity int) *tokenBucket {
	return &tokenBucket{
		capacity:   capacity,
		tokens:     capacity,
		lastRefill: time.Now(),
	}
}

func (tb *tokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill)

	if elapsed >= time.Minute {
		tb.tokens = tb.capacity
		tb.lastRefill = now
	}
}

func (tb *tokenBucket) tryConsume() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.refill()

	if tb.tokens > 0 {
		tb.tokens--
		return true
	}
	return false
}

func (tb *tokenBucket) remaining() int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.refill()
	return tb.tokens
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		cache: cache.New(1*time.Hour, 24*time.Hour),
	}
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := GetClientFromContext(r.Context())
		if client == nil {
			next.ServeHTTP(w, r)
			return
		}

		limits, err := rl.getOrCreateLimits(client)
		if err != nil {
			http.Error(w, `{"error": "Internal server error"}`, http.StatusInternalServerError)
			return
		}

		if !limits.minute.tryConsume() {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(1*time.Minute).Unix()))
			http.Error(w, `{"error": "Rate limit exceeded (minute)"}`, http.StatusTooManyRequests)
			return
		}

		if !limits.hour.tryConsume() {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(1*time.Hour).Unix()))
			http.Error(w, `{"error": "Rate limit exceeded (hour)"}`, http.StatusTooManyRequests)
			return
		}

		if !limits.day.tryConsume() {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(24*time.Hour).Unix()))
			http.Error(w, `{"error": "Rate limit exceeded (day)"}`, http.StatusTooManyRequests)
			return
		}

		w.Header().Set("X-RateLimit-Remaining-Minute", fmt.Sprintf("%d", limits.minute.remaining()))
		w.Header().Set("X-RateLimit-Remaining-Hour", fmt.Sprintf("%d", limits.hour.remaining()))
		w.Header().Set("X-RateLimit-Remaining-Day", fmt.Sprintf("%d", limits.day.remaining()))

		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) getOrCreateLimits(client *models.Client) (*clientLimits, error) {
	key := "limits:" + client.ID

	if cached, found := rl.cache.Get(key); found {
		return cached.(*clientLimits), nil
	}

	limits := &clientLimits{
		minute: newTokenBucket(client.RateLimitMinute),
		hour:   newTokenBucket(client.RateLimitHour),
		day:    newTokenBucket(client.RateLimitDay),
	}

	rl.cache.Set(key, limits, 24*time.Hour)
	return limits, nil
}

func (rl *RateLimiter) ResetClient(clientID string) {
	rl.cache.Delete("limits:" + clientID)
}
