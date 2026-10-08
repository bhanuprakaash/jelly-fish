package gemini

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// classify maps err onto a *provider.Error. It keeps only the class,
// retry-after and request id: Gemini's own message can echo the request. Our
// own ctx ending is not a Provider failure and passes through; a transport
// failure is one.
func classify(ctx context.Context, err error, requestID string) error {
	if ctx.Err() != nil {
		return err
	}
	pe := &provider.Error{Kind: provider.KindProviderDown, RequestID: requestID}
	var apiErr genai.APIError
	switch {
	case errors.As(err, &apiErr):
		pe.RetryAfter = retryDelay(apiErr)
		pe.Kind = provider.WaitKind(kindOf(apiErr), pe.RetryAfter)
	case errors.Is(err, provider.ErrStreamIdle):
		pe.Err = provider.ErrStreamIdle
	}
	return pe
}

// kindOf trusts the status the body names over the HTTP code where Gemini
// reuses one code for different faults.
func kindOf(e genai.APIError) provider.ErrorKind {
	switch {
	case keyRejected(e):
		return provider.KindKeyInvalid
	case e.Code == http.StatusPaymentRequired || e.Status == "FAILED_PRECONDITION":
		return provider.KindBilling
	case e.Code == http.StatusNotFound:
		return provider.KindModelUnavailable
	case e.Code == http.StatusRequestEntityTooLarge:
		return provider.KindTooLarge
	case e.Code == http.StatusTooManyRequests:
		return provider.KindRateLimited
	case e.Code >= 500:
		return provider.KindProviderDown
	}
	return provider.KindBug
}

// keyRejected is a refused key. Gemini answers a malformed key with a 400
// that names API_KEY_INVALID, not a 401.
func keyRejected(e genai.APIError) bool {
	return e.Code == http.StatusUnauthorized || e.Code == http.StatusForbidden || detail(e, "ErrorInfo", "reason") == "API_KEY_INVALID"
}

// tooLong is the 400 for a prompt over the model's input limit.
func tooLong(e genai.APIError) bool {
	return e.Status == "INVALID_ARGUMENT" && strings.Contains(e.Message, "exceeds the maximum")
}

// retryDelay is the wait Gemini's RetryInfo asks for, or zero.
func retryDelay(e genai.APIError) time.Duration {
	d, err := time.ParseDuration(detail(e, "RetryInfo", "retryDelay"))
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// detail is the string field of the first error detail whose type ends in
// typ, or "".
func detail(e genai.APIError, typ, field string) string {
	for _, d := range e.Details {
		if t, _ := d["@type"].(string); strings.HasSuffix(t, "."+typ) {
			s, _ := d[field].(string)
			return s
		}
	}
	return ""
}
