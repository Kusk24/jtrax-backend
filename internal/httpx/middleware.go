package httpx

import (
	"net"
	"net/http"
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

// RateLimit is a fixed-window per-IP limiter for unauthenticated endpoints.
func RateLimit(perMinute int, next http.HandlerFunc) http.HandlerFunc {
	type window struct {
		start time.Time
		count int
	}
	var mu sync.Mutex
	seen := map[string]*window{}
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
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
