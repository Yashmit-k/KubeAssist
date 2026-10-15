package api

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimiter is a per-key token bucket (key = user ID, or client IP for
// anonymous callers). It is in-process: with several backend replicas
// each enforces its own budget, which is fine for "basic" abuse
// protection; put a shared limiter at the edge for anything stricter.
type RateLimiter struct {
	limit rate.Limit
	burst int
	idle  time.Duration // a bucket untouched this long is full again and can be dropped

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter allows `events` per `per`, with bursts of up to
// `events`. events <= 0 returns nil, which every caller treats as
// "unlimited".
func NewRateLimiter(events int, per time.Duration) *RateLimiter {
	if events <= 0 || per <= 0 {
		return nil
	}
	return &RateLimiter{
		limit:   rate.Limit(float64(events) / per.Seconds()),
		burst:   events,
		idle:    per,
		buckets: make(map[string]*bucket),
	}
}

// ParseRateLimit parses "N/unit" (unit: s, m, h, or a Go duration such
// as 10m), e.g. "10/h" or "30/m". "0" or "off" disables the limit.
func ParseRateLimit(spec string) (*RateLimiter, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "0" || spec == "off" {
		return nil, nil
	}
	n, unit, ok := strings.Cut(spec, "/")
	events, err := strconv.Atoi(n)
	if !ok || err != nil || events < 0 {
		return nil, fmt.Errorf("rate limit %q: want N/unit, e.g. 10/h", spec)
	}
	var per time.Duration
	switch unit {
	case "s":
		per = time.Second
	case "m":
		per = time.Minute
	case "h":
		per = time.Hour
	default:
		if per, err = time.ParseDuration(unit); err != nil || per <= 0 {
			return nil, fmt.Errorf("rate limit %q: unknown unit %q", spec, unit)
		}
	}
	return NewRateLimiter(events, per), nil
}

// Allow consumes one token for key. When the bucket is empty it returns
// false and how long until the next token.
func (l *RateLimiter) Allow(key string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) > l.idle {
		for k, b := range l.buckets {
			if now.Sub(b.lastSeen) > l.idle {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(l.limit, l.burst)}
		l.buckets[key] = b
	}
	b.lastSeen = now
	r := b.lim.ReserveN(now, 1)
	if d := r.DelayFrom(now); d > 0 {
		r.CancelAt(now)
		return false, d
	}
	return true, 0
}

func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests; try again later")
}

// limitKey identifies the caller for rate limiting: the signed-in user
// if the request was authorized as one, else the client IP.
func (s *Server) limitKey(r *http.Request) string {
	if u := userFrom(r.Context()); u != nil {
		return "user:" + u.ID
	}
	return "ip:" + s.clientIP(r)
}

// clientIP is the TCP peer, or — when TRUST_PROXY_HEADERS is on because
// a reverse proxy we control sits in front — the address that proxy
// appended to X-Forwarded-For (the right-most entry; anything to its left
// is client-supplied and spoofable).
func (s *Server) clientIP(r *http.Request) string {
	if s.TrustProxyHeaders {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limitScoped rate-limits a cluster-scoped handler.
func (s *Server) limitScoped(l *RateLimiter, h scopedHandler) scopedHandler {
	if l == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request, src clusterSource, clusterKey string) {
		if ok, wait := l.Allow(s.limitKey(r)); !ok {
			writeRateLimited(w, wait)
			return
		}
		h(w, r, src, clusterKey)
	}
}

// limitByIP rate-limits a plain handler by client IP.
func (s *Server) limitByIP(l *RateLimiter) func(http.HandlerFunc) http.HandlerFunc {
	return func(h http.HandlerFunc) http.HandlerFunc {
		if l == nil {
			return h
		}
		return func(w http.ResponseWriter, r *http.Request) {
			if ok, wait := l.Allow("ip:" + s.clientIP(r)); !ok {
				writeRateLimited(w, wait)
				return
			}
			h(w, r)
		}
	}
}
