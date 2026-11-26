// Route registration shared by the school API.
package httpx

import (
	"net/http"
)

// RouteRegistrar is the registration surface used by every feature and registry resource.
type RouteRegistrar interface {
	HandleFunc(string, func(http.ResponseWriter, *http.Request))
}
