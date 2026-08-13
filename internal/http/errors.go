package http

import (
	"errors"
	"net/http"

	"github.com/Jolls/enshu/internal/auth"
)

// classifyFormError maps a service-layer error to a renderable (status, message, Retry-After)
// triple. domain, if non-nil, is checked first so a handler can splice in its own errors.Is
// cases (e.g. auth.ErrEmailTaken, auth.ErrInvalidCredentials) ahead of the two error types every
// auth.Service method can return. ok is false for anything unclassified, which the caller must
// render as a bare 500 rather than leaking the underlying error to the client.
func classifyFormError(err error, domain func(error) (status int, msg string, matched bool)) (status int, msg, retryAfter string, ok bool) {
	if domain != nil {
		if status, msg, matched := domain(err); matched {
			return status, msg, "", true
		}
	}
	var ve *auth.ValidationError
	var rle *auth.RateLimitError
	switch {
	case errors.As(err, &ve):
		return http.StatusBadRequest, ve.Msg, "", true
	case errors.As(err, &rle):
		return http.StatusTooManyRequests, "Too many attempts. Try again later.", formatRetryAfter(rle.RetryAfter), true
	default:
		return 0, "", "", false
	}
}
