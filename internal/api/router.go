// Gin serves all feature handlers. Keep the HTTP details existing school clients rely on.
package api

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

type router struct{ engine *gin.Engine }

type originalRequestKey struct{}

// A marker lets the compatibility check distinguish a registered route from a
// redirect or a method error. Only Gin can dispatch the actual endpoint.
type registeredRoute struct{}

func (registeredRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }

func newRouter(register func(gin.IRoutes), middleware ...gin.HandlerFunc) *router {
	engine := gin.New()
	engine.RedirectTrailingSlash = false
	engine.UseEscapedPath = true
	// Query unescaping would turn a literal '+' in a path parameter into a space.
	engine.UnescapePathValues = false

	// ServeMux supplies Go's canonical redirects and its 404/405 bodies and
	// Allow headers. It has no feature handlers.
	httpErrors := http.NewServeMux()
	engine.Use(func(c *gin.Context) {
		if original, ok := c.Request.Context().Value(originalRequestKey{}).(*http.Request); ok {
			c.Request = original
		}
		for i, param := range c.Params {
			c.Params[i].Value, _ = url.PathUnescape(param.Value)
		}
	})
	engine.Use(middleware...)
	engine.Use(func(c *gin.Context) {
		u := c.Request.URL
		if u.RawPath == "" && path.Clean(u.Path) == u.Path {
			return
		}
		// ServeMux decides whether an unusual path names an endpoint. For
		// example, a%2Fb is one valid ID, but %2F alone is a slash.
		h, _ := httpErrors.Handler(c.Request)
		if _, registered := h.(registeredRoute); !registered {
			c.Abort()
			h.ServeHTTP(c.Writer, c.Request)
		}
	})
	engine.NoRoute(func(c *gin.Context) { httpErrors.ServeHTTP(c.Writer, c.Request) })
	register(engine)
	for _, route := range engine.Routes() {
		parts := strings.Split(route.Path, "/")
		for i, part := range parts {
			if name, ok := strings.CutPrefix(part, ":"); ok {
				parts[i] = "{" + name + "}"
			}
		}
		httpErrors.Handle(route.Method+" "+strings.Join(parts, "/"), registeredRoute{})
	}
	return &router{engine: engine}
}

func (r *router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodHead && req.URL.RawPath == "" {
		r.engine.ServeHTTP(w, req)
		return
	}
	// HEAD selects the GET route. Canonicalize escaping one segment at a
	// time so %68ealth matches health and an encoded slash stays in its ID.
	// Middleware restores the untouched request before a feature sees it.
	lookup := req.WithContext(context.WithValue(req.Context(), originalRequestKey{}, req))
	if req.Method == http.MethodHead {
		lookup.Method = http.MethodGet
	}
	if req.URL.RawPath != "" {
		parts := strings.Split(req.URL.EscapedPath(), "/")
		for i, part := range parts {
			value, _ := url.PathUnescape(part)
			parts[i] = url.PathEscape(value)
		}
		u := *req.URL
		u.RawPath = strings.Join(parts, "/")
		lookup.URL = &u
	}
	r.engine.ServeHTTP(w, lookup)
}
