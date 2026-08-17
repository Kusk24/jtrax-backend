package httpx

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CORS allows the local dev frontends; adjust via ALLOWED_ORIGINS when deployed.
func CORS(allowedOrigins []string, next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range allowedOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP identifies the caller for rate limiting. Deployed behind a proxy
// every RemoteAddr is the proxy's, which would put all callers in one bucket,
// so the left-most X-Forwarded-For entry wins when present.
//
// That header is caller-supplied and trivially spoofed, so it may only relax
// a limit, never tighten one — it is used for rate limiting alone and never
// for an authorization decision.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if first, _, found := strings.Cut(fwd, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(fwd)
	}
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return ip
	}
	return r.RemoteAddr
}

// RateLimit is a fixed-window per-IP limiter for unauthenticated endpoints.
func RateLimit(perMinute int, next http.HandlerFunc) http.HandlerFunc {
	limiter := NewLimiter(perMinute)
	return func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow(clientIP(r)) {
			Error(w, http.StatusTooManyRequests, "too many attempts, try again in a minute", nil)
			return
		}
		next(w, r)
	}
}

// Limiter is a fixed-window counter over an arbitrary key.
//
// Split out from RateLimit because the client IP is the wrong key for sign-in.
// The portals call this API from **their own servers** — a Next.js server
// action on Vercel, not the browser — so every member of staff arrives from the
// same address. An IP budget of ten a minute is therefore not "ten tries for
// you"; it is ten tries for the entire academy, and the eleventh person to try
// their password that minute is locked out by the first ten.
//
// Sign-in is limited per account instead: it stops someone guessing at one
// person's password, which is the actual threat, and cannot take anyone else
// down with it.
type Limiter struct {
	perMinute int
	mu        sync.Mutex
	seen      map[string]*window
}

type window struct {
	start time.Time
	count int
}

func NewLimiter(perMinute int) *Limiter {
	return &Limiter{perMinute: perMinute, seen: map[string]*window{}}
}

// Allow records an attempt against key and reports whether it is within budget.
func (l *Limiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	// Fixed windows are kept only while they are live, so the map cannot grow
	// without bound on a long-running process.
	for k, wd := range l.seen {
		if now.Sub(wd.start) > time.Minute {
			delete(l.seen, k)
		}
	}
	wd := l.seen[key]
	if wd == nil {
		wd = &window{start: now}
		l.seen[key] = wd
	}
	wd.count++
	return wd.count <= l.perMinute
}

// Forget clears a key's window — called when an attempt succeeds, so a member
// of staff who mistypes their password four times and then gets it right is not
// still carrying those four for the rest of the minute.
func (l *Limiter) Forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.seen, key)
}
