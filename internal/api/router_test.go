// Exercise the school's route table against the net/http behavior existing clients use.
package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/mail"
)

// publishedPattern uses the pre-migration notation of the checked-in API inventory.
func publishedPattern(route gin.RouteInfo) string {
	parts := strings.Split(route.Path, "/")
	for i, part := range parts {
		if name, ok := strings.CutPrefix(part, ":"); ok {
			// The student resource and its history share one Gin wildcard.
			if route.Path == "/api/v1/students/:id/history" {
				name = "studentId"
			}
			parts[i] = "{" + name + "}"
		}
	}
	return route.Method + " " + strings.Join(parts, "/")
}

func featureRoutes() gin.RoutesInfo {
	engine := gin.New()
	registerRoutes(engine, nil, mail.Config{}, nil, nil)
	return engine.Routes()
}

func TestGinRouteContracts(t *testing.T) {
	routes := featureRoutes()
	old := http.NewServeMux()
	current := newRouter(func(g gin.IRoutes) {
		for _, route := range routes {
			pattern := publishedPattern(route)
			old.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				fmt.Fprint(w, r.Method, " ", r.Pattern)
				for _, part := range strings.Split(pattern, "/") {
					if name, ok := strings.CutPrefix(part, "{"); ok {
						fmt.Fprint(w, " ", r.PathValue(strings.TrimSuffix(name, "}")))
					}
				}
			})
			g.Handle(route.Method, route.Path, func(c *gin.Context) {
				c.Header("Content-Type", "text/plain")
				fmt.Fprint(c.Writer, c.Request.Method, " ", pattern)
				for _, part := range strings.Split(route.Path, "/") {
					if name, ok := strings.CutPrefix(part, ":"); ok {
						fmt.Fprint(c.Writer, " ", c.Param(name))
					}
				}
			})
		}
	})
	check := func(method, path string) {
		t.Helper()
		request := func(h http.Handler) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			return w
		}
		want, got := request(old), request(current)
		if got.Code != want.Code || got.Body.String() != want.Body.String() ||
			!reflect.DeepEqual(got.Result().Header, want.Result().Header) {
			t.Fatalf("%s %s: want %d %v %q, got %d %v %q", method, path,
				want.Code, want.Result().Header, want.Body, got.Code, got.Result().Header, got.Body)
		}
	}
	for _, route := range routes {
		method, path, _ := strings.Cut(publishedPattern(route), " ")
		parts := strings.Split(path, "/")
		for i, part := range parts {
			if strings.HasPrefix(part, "{") {
				parts[i] = fmt.Sprintf("sample-%d", i)
			}
		}
		path = strings.Join(parts, "/")
		check(method, path)
		check("OPTIONS", path)
		check("HEAD", path)
	}
	for _, path := range []string{"/unknown", "/health/", "//health", "/api/../health",
		"/api/v1/students/a%2Fb/history", "/api/v1/students/a+b/history", "/api/v1/students/a%2Bb/history", "/api/v1/students/%2f/history", "/api/v1/students/%2F", "/%68ealth", "/api/v1/students/%2E/history", "/api/v1/students/%252F/history", "/api/v1/students/../history", "/api/v1/students//history", "/api/v1/students/stu%20one/history"} {
		check("GET", path)
	}
	t.Logf("compared %d registered routes", len(routes))
}

// The route comparison above exercises each registered handler. This independent
// inventory also detects a whole feature being omitted from registerRoutes.
func TestPublishedRoutesRemainAvailable(t *testing.T) {
	raw, err := os.ReadFile("testdata/published_routes.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, stripeEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("stripe=%t", stripeEnabled), func(t *testing.T) {
			t.Setenv("STRIPE_SECRET_KEY", "")
			t.Setenv("STRIPE_WEBHOOK_SECRET", "")
			if stripeEnabled {
				t.Setenv("STRIPE_SECRET_KEY", "test-key")
				t.Setenv("STRIPE_WEBHOOK_SECRET", "test-secret")
			}
			var got, want []string
			for _, route := range featureRoutes() {
				got = append(got, publishedPattern(route))
			}
			for _, route := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				if stripeEnabled || route != "POST /api/v1/stripe/webhook" {
					want = append(want, route)
				}
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("route inventory differs: missing %v, additional %v", difference(want, got), difference(got, want))
			}
		})
	}
}

func difference(first, second []string) []string {
	var result []string
	for _, route := range first {
		if !slices.Contains(second, route) {
			result = append(result, route)
		}
	}
	return result
}

// Exercise the middleware chain through the same entry point as the server.
// Preflight, redirects and method errors must neither charge a route's budget
// nor skip the CORS headers. HEAD and GET share the underlying endpoint.
func TestGinMiddlewareAcrossRequestForms(t *testing.T) {
	const origin = "https://school.example"
	calls := 0
	router := newRouter(func(routes gin.IRoutes) {
		routes.GET("/pupils/:id", httpx.RateLimit(1), func(c *gin.Context) {
			calls++
			c.String(http.StatusOK, "%s %s", c.Request.Method, c.Param("id"))
		})
	}, httpx.CORS([]string{origin}))
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"OPTIONS", "/pupils//penny", http.StatusNoContent},
		{"GET", "/pupils//penny", http.StatusTemporaryRedirect},
		{"POST", "/pupils/penny", http.StatusMethodNotAllowed},
		{"GET", "/missing", http.StatusNotFound},
		{"HEAD", "/%70upils/a%2Fb", http.StatusOK},
		{"GET", "/pupils/penny", http.StatusTooManyRequests},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != tc.status || rec.Header().Get("Access-Control-Allow-Origin") != origin {
			t.Fatalf("%s %s: got %d %v", tc.method, tc.path, rec.Code, rec.Header())
		}
		if tc.method == "HEAD" && rec.Body.String() != "HEAD a/b" {
			t.Errorf("handler saw %q", rec.Body.String())
		}
	}
	if calls != 1 {
		t.Errorf("feature handler ran %d times, want one", calls)
	}
}
