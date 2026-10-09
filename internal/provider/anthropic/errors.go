package anthropic

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// billingText spots the 400s that mean the account, not the request, is the
// problem (provider-gateway.md D20: classified by message pattern).
var billingText = regexp.MustCompile(`credit balance|spend(ing)? limit|usage limit|billing`)

// classify maps err onto a *provider.Error. It keeps only the status class,
// retry-after and request id: the SDK's own message can echo the request.
// u is what the attempt consumed before it failed. Our own ctx ending is
// not a Provider failure and passes through; a transport timeout is one.
func classify(ctx context.Context, err error, u provider.Usage, requestID string) error {
	if ctx.Err() != nil {
		return err
	}
	pe := &provider.Error{Kind: provider.KindProviderDown, RequestID: requestID, Usage: u}
	var apiErr *sdk.Error
	switch {
	case errors.As(err, &apiErr):
		if apiErr.RequestID != "" {
			pe.RequestID = apiErr.RequestID
		}
		pe.HTTPStatus = apiErr.StatusCode
		pe.RetryAfter = provider.RetryAfter(apiErr.Response)
		pe.Kind = provider.WaitKind(kindOf(apiErr, pe.RetryAfter), pe.RetryAfter)
	case errors.Is(err, provider.ErrStreamIdle):
		pe.Err = provider.ErrStreamIdle
	}
	return pe
}

// kindOf trusts the error type in the body first, which is all a mid-stream
// error (HTTP 200) has, then the status.
func kindOf(e *sdk.Error, wait time.Duration) provider.ErrorKind {
	switch e.Type() {
	case "authentication_error", "permission_error":
		return provider.KindKeyInvalid
	case "billing_error":
		return provider.KindBilling
	case "not_found_error":
		return provider.KindModelUnavailable
	case "request_too_large":
		return provider.KindTooLarge
	case "overloaded_error", "api_error", "timeout_error":
		return provider.KindProviderDown
	}
	switch status := e.StatusCode; {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return provider.KindKeyInvalid
	case status == http.StatusPaymentRequired:
		return provider.KindBilling
	case status == http.StatusNotFound:
		return provider.KindModelUnavailable
	case status == http.StatusRequestEntityTooLarge:
		return provider.KindTooLarge
	case status == http.StatusTooManyRequests:
		return rateLimitKind(wait)
	case status == http.StatusBadRequest && billingText.MatchString(strings.ToLower(e.RawJSON())):
		return provider.KindBilling
	case status >= 500:
		return provider.KindProviderDown
	case status == http.StatusOK && e.Type() == "rate_limit_error":
		return provider.KindRateLimited
	}
	// Every other 400, including the prefix-binding ones (block_binding,
	// thinking, tool_choice), means we sent a malformed request.
	return provider.KindBug
}

// rateLimitKind: a 429 with no retry-after is a spend cap, not a burst.
func rateLimitKind(wait time.Duration) provider.ErrorKind {
	switch {
	case wait == 0:
		return provider.KindBilling
	case wait <= provider.ShortWait:
		return provider.KindRateLimited
	}
	return provider.KindLongWait
}
