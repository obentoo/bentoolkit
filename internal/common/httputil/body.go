package httputil

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrResponseTooLarge is returned when an HTTP response body exceeds the
// MaxBodyBytes cap. It lives here, not in a caller, so the autoupdate and the
// provider paths share one value and errors.Is matches it whichever package
// raised it (internal/common/provider cannot import internal/autoupdate).
var ErrResponseTooLarge = errors.New("response body too large")

// ClassifyBodyReadError maps an error returned while reading an HTTP response
// body to a domain error. When the read tripped an http.MaxBytesReader cap the
// standard library yields an *http.MaxBytesError; this is translated into an
// error wrapping ErrResponseTooLarge that carries the limit. Any other non-nil
// error is returned unchanged, and a nil error yields nil.
func ClassifyBodyReadError(err error) error {
	if err == nil {
		return nil
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return fmt.Errorf("%w: limit %d bytes", ErrResponseTooLarge, maxBytesErr.Limit)
	}
	return err
}
