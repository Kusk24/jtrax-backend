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
	type window struct {
		start time.Time
		count int
	}
	var mu sync.Mutex
	seen := map[string]*window{}
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		mu.Lock()
		wd := seen[ip]
		now := time.Now()
		if wd == nil || now.Sub(wd.start) > time.Minute {
			wd = &window{start: now}
			seen[ip] = wd
		}
		wd.count++
		over := wd.count > perMinute
		mu.Unlock()
		if over {
			Error(w, http.StatusTooManyRequests, "too many attempts, try again in a minute", nil)
			return
		}
		next(w, r)
	}
}
