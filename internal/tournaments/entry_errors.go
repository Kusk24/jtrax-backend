// Registration refusals carry a message the family can see. Database errors stay in the log.
package tournaments

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

type registrationRefusal struct {
	status  int
	message string
}

func (e registrationRefusal) Error() string { return e.message }

func writeRegistrationError(ctx *gin.Context, err error) {
	var refusal registrationRefusal
	if errors.As(err, &refusal) {
		httpx.Error(ctx, refusal.status, refusal.message, nil)
		return
	}
	httpx.Error(ctx, http.StatusInternalServerError, "could not register", err)
}
