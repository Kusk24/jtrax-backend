// The API uses Gin for routing and keeps net/http handlers at the transport boundary.
package api

import (
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type router struct {
	engine   *gin.Engine
	standard *http.ServeMux
}

func newRouter() *router {
	engine := gin.New()
	engine.RedirectTrailingSlash = false
	r := &router{engine: engine, standard: http.NewServeMux()}
	// Preserve method discovery (including Allow), redirects and the original
	// plain-text errors for requests that do not match a Gin route.
	engine.NoRoute(func(c *gin.Context) { r.standard.ServeHTTP(c.Writer, c.Request) })
	return r
}

func (r *router) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	r.standard.HandleFunc(pattern, handler)
	method, route, _ := strings.Cut(pattern, " ")
	parts := strings.Split(route, "/")
	var names []string
	for i, segment := range parts {
		if strings.HasPrefix(segment, "{") {
			names = append(names, strings.TrimSuffix(segment[1:], "}"))
			// Gin shares wildcard nodes. Two features may call the same
			// segment "id" and "studentId" in their public handler code.
			parts[i] = ":param" + strconv.Itoa(len(names))
		}
	}
	r.engine.Handle(method, strings.Join(parts, "/"), func(c *gin.Context) {
		c.Request.Pattern = pattern
		for i, name := range names {
			c.Request.SetPathValue(name, c.Params[i].Value)
		}
		handler(c.Writer, c.Request)
	})
}

func (r *router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// ServeMux owns the existing HEAD semantics and canonicalization of
	// escaped segments, duplicate slashes and dot segments.
	if req.Method == http.MethodHead || req.Method == http.MethodConnect ||
		req.URL.RawPath != "" || path.Clean(req.URL.Path) != req.URL.Path {
		r.standard.ServeHTTP(w, req)
		return
	}
	r.engine.ServeHTTP(w, req)
}
