// Package ratelimit provides a simple in-memory token-bucket limiter.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a size-bounded map of per-key token buckets.
type Limiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     float64 // tokens per second
	capacity float64
	maxKeys  int
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New creates a limiter allowing `burst` tokens, refilled at `perMinute` tokens per minute.
func New(perMinute, burst int) *Limiter {
	if perMinute <= 0 {
		perMinute = 5
	}
	if burst <= 0 {
		burst = perMinute
	}
	return &Limiter{
		buckets:  map[string]*bucket{},
		rate:     float64(perMinute) / 60.0,
		capacity: float64(burst),
		maxKeys:  10_000,
	}
}

// Allow reports whether key may proceed, consuming one token when true.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.maxKeys {
			l.evictOne()
		}
		l.buckets[key] = &bucket{tokens: l.capacity - 1, last: now}
		return true
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens = min(l.capacity, b.tokens+elapsed*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *Limiter) evictOne() {
	for k := range l.buckets {
		delete(l.buckets, k)
		return
	}
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
