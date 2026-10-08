package openai

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// codeError is a failure OpenAI reported inside the stream, by its code.
type codeError string

func (e codeError) Error() string { return "openai stream error: " + string(e) }

// errCode is the error code OpenAI gave for err, in an HTTP error body or
// in the stream, or "".
func errCode(err error) string {
	var ce codeError
	var apiErr *sdk.Error
	var se *ssestream.StreamError
	switch {
	case errors.As(err, &ce):
		return string(ce)
	case errors.As(err, &apiErr):
		return apiErr.Code
	case errors.As(err, &se):
		var body struct {
			Code  string `json:"code"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(se.Event.Data, &body)
		return cmp.Or(body.Error.Code, body.Code)
	}
	return ""
}

// classify maps err onto a *provider.Error. It keeps only the class,
// retry-after and request id: OpenAI's own message can echo the request.
// u is what the attempt consumed before it failed. Our own ctx ending is not
// a Provider failure and passes through; a transport timeout is one.
func classify(ctx context.Context, err error, u provider.Usage, requestID string) error {
	if ctx.Err() != nil {
		return err
	}
	pe := &provider.Error{Kind: provider.KindProviderDown, RequestID: requestID, Usage: u}
	var apiErr *sdk.Error
	switch {
	case errors.As(err, &apiErr):
		if apiErr.Response != nil && pe.RequestID == "" {
			pe.RequestID = apiErr.Response.Header.Get("x-request-id")
		}
		pe.RetryAfter = provider.RetryAfter(apiErr.Response)
		pe.Kind = kindOf(apiErr.StatusCode, apiErr.Code)
	case errors.Is(err, provider.ErrStreamIdle):
		pe.Err = provider.ErrStreamIdle
	default:
		if code := errCode(err); code != "" {
			pe.Kind = kindOf(http.StatusOK, code)
		}
	}
	pe.Kind = provider.WaitKind(pe.Kind, pe.RetryAfter)
	return pe
}

// kindOf trusts the error code first, which is all a mid-stream error
// (HTTP 200) has, then the status. OpenAI flags a spent quota by code, so a
// bare 429 is a rate limit.
func kindOf(status int, code string) provider.ErrorKind {
	switch code {
	case "invalid_api_key":
		return provider.KindKeyInvalid
	case "insufficient_quota", "billing_hard_limit_reached", "billing_not_active":
		return provider.KindBilling
	case "model_not_found":
		return provider.KindModelUnavailable
	case "rate_limit_exceeded":
		return provider.KindRateLimited
	case "server_error":
		return provider.KindProviderDown
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return provider.KindKeyInvalid
	case status == http.StatusPaymentRequired:
		return provider.KindBilling
	case status == http.StatusNotFound:
		return provider.KindModelUnavailable
	case status == http.StatusRequestEntityTooLarge:
		return provider.KindTooLarge
	case status == http.StatusTooManyRequests:
		return provider.KindRateLimited
	case status >= 500:
		return provider.KindProviderDown
	}
	return provider.KindBug
}
