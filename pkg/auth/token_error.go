package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers/common"
)

// TokenRequestError retains only machine-readable recovery metadata. Provider
// bodies, descriptions and token values never become diagnostic messages.
type TokenRequestError struct {
	StatusCode              int
	Code                    string
	RetryAfter              time.Duration
	ReauthorizationRequired bool
	Wrapped                 error
}

func (e *TokenRequestError) Error() string {
	if e.ReauthorizationRequired {
		return "OpenAI account authorization must be renewed"
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("OpenAI token request failed (HTTP %d)", e.StatusCode)
	}
	return "OpenAI token service is unavailable"
}
func (e *TokenRequestError) Unwrap() error { return e.Wrapped }

func reauthorizationCode(code string) bool {
	switch code {
	case "invalid_grant",
		"invalid_refresh_token",
		"token_expired",
		"refresh_token_expired",
		"refresh_token_invalidated",
		"refresh_token_reused",
		"access_denied",
		"consent_required":
		return true
	}
	return false
}

func safeTokenErrorCode(code string) string {
	if reauthorizationCode(code) {
		return code
	}
	switch code {
	case "temporarily_unavailable",
		"server_error",
		"usage_unavailable",
		"user_unavailable",
		"subscription_sharing_usage_unavailable",
		"subscription_sharing_usage_limit_exceeded",
		"rate_limit_exceeded",
		"invalid_client",
		"invalid_scope",
		"invalid_request",
		"unsupported_grant_type":
		return code
	}
	return "oauth_error"
}

func tokenEndpointError(response *http.Response) error {
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload)
	var code string
	if json.Unmarshal(payload.Error, &code) != nil {
		var nested struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(payload.Error, &nested)
		code = nested.Code
	}
	code = safeTokenErrorCode(code)
	return &TokenRequestError{
		StatusCode:              response.StatusCode,
		Code:                    code,
		RetryAfter:              common.RetryAfter(response.Header, time.Now()),
		ReauthorizationRequired: reauthorizationCode(code),
	}
}
