package providers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go/v3"

	"github.com/sipeed/picoclaw/pkg/providers/common"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// Common patterns in Go HTTP error messages
var httpStatusPatterns = []*regexp.Regexp{
	regexp.MustCompile(`status[:\s]+(\d{3})`),
	regexp.MustCompile(`http[/\s]+\d*\.?\d*\s+(\d{3})`),
	regexp.MustCompile(`\b([3-5]\d{2})\b`),
}

// errorPattern defines a single pattern (string or regex) for error classification.
type errorPattern struct {
	substring string
	regex     *regexp.Regexp
}

func substr(s string) errorPattern { return errorPattern{substring: s} }
func rxp(r string) errorPattern    { return errorPattern{regex: regexp.MustCompile("(?i)" + r)} }

// Error patterns organized by FailoverReason, matching OpenClaw production (~40 patterns).
var (
	rateLimitPatterns = []errorPattern{
		rxp(`rate[_ ]limit`),
		substr("too many requests"),
		substr("429"),
		substr("exceeded your current quota"),
		rxp(`exceeded.*quota`),
		rxp(`resource has been exhausted`),
		rxp(`resource.*exhausted`),
		substr("resource_exhausted"),
		substr("quota exceeded"),
		substr("usage limit"),
	}

	overloadedPatterns = []errorPattern{
		rxp(`overloaded_error`),
		rxp(`"type"\s*:\s*"overloaded_error"`),
		substr("overloaded"),
	}

	timeoutPatterns = []errorPattern{
		substr("timeout"),
		substr("timed out"),
		substr("deadline exceeded"),
		substr("context deadline exceeded"),
	}

	billingPatterns = []errorPattern{
		rxp(`\b402\b`),
		substr("payment required"),
		substr("insufficient credits"),
		substr("credit balance"),
		substr("plans & billing"),
		substr("insufficient balance"),
	}

	authPatterns = []errorPattern{
		rxp(`invalid[_ ]?api[_ ]?key`),
		substr("incorrect api key"),
		substr("invalid token"),
		substr("authentication"),
		substr("re-authenticate"),
		substr("oauth token refresh failed"),
		substr("unauthorized"),
		substr("forbidden"),
		substr("access denied"),
		substr("expired"),
		substr("token has expired"),
		rxp(`\b401\b`),
		rxp(`\b403\b`),
		substr("no credentials found"),
		substr("no api key found"),
	}

	formatPatterns = []errorPattern{
		substr("string should match pattern"),
		substr("tool_use.id"),
		substr("tool_use_id"),
		substr("messages.1.content.1.tool_use.id"),
		substr("invalid request format"),
	}

	imageDimensionPatterns = []errorPattern{
		rxp(`image dimensions exceed max`),
	}

	imageSizePatterns = []errorPattern{
		rxp(`image exceeds.*mb`),
	}

	// Transient HTTP status codes that map to timeout (server-side failures).
	transientStatusCodes = map[int]bool{
		500: true, 502: true, 503: true,
		521: true, 522: true, 523: true, 524: true,
		529: true,
	}
)

// ClassifyError classifies an error into a FailoverError with reason.
// Returns nil if the error is not classifiable (unknown errors should not trigger fallback).
func ClassifyError(err error, provider, model string) *FailoverError {
	if err == nil {
		return nil
	}

	// Context cancellation: user abort, never fallback.
	if errors.Is(err, context.Canceled) {
		return nil
	}

	// Context deadline exceeded: treat as timeout, always fallback.
	if errors.Is(err, context.DeadlineExceeded) {
		return &FailoverError{
			Reason:   FailoverTimeout,
			Provider: provider,
			Model:    model,
			Wrapped:  err,
		}
	}

	var classified *FailoverError
	if errors.As(err, &classified) {
		return classified
	}
	msg := strings.ToLower(err.Error())
	status, delay, code := errorHTTPMetadata(err)
	msg += " " + strings.ToLower(code)
	// Hard quota exhaustion often arrives as HTTP 429; it cannot recover with backoff.
	if strings.Contains(msg, "insufficient_quota") || strings.Contains(msg, "exceeded your current quota") ||
		strings.Contains(msg, "billing_hard_limit") ||
		strings.Contains(msg, "quota exhausted") ||
		strings.Contains(msg, "quota_exceeded") ||
		strings.Contains(msg, "quota exceeded") ||
		strings.Contains(msg, "insufficient credit") ||
		strings.Contains(msg, "credits exhausted") {
		return &FailoverError{
			Reason:     FailoverBilling,
			Provider:   provider,
			Model:      model,
			Status:     status,
			RetryAfter: delay,
			Wrapped:    err,
		}
	}
	networkErr, isNetwork := modelNetworkError(err)
	if isNetwork || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		reason := FailoverNetwork
		if networkErr != nil && networkErr.Timeout() {
			reason = FailoverTimeout
		}
		return &FailoverError{Reason: reason, Provider: provider, Model: model, Wrapped: err}
	}
	if status > 0 {
		if reason := classifyByStatus(status); reason != "" {
			return &FailoverError{
				Reason:     reason,
				Provider:   provider,
				Model:      model,
				Status:     status,
				RetryAfter: delay,
				Wrapped:    err,
			}
		}
	}

	// Image dimension/size errors: non-retriable, non-fallback.
	if IsImageDimensionError(msg) || IsImageSizeError(msg) {
		return &FailoverError{
			Reason:   FailoverFormat,
			Provider: provider,
			Model:    model,
			Wrapped:  err,
		}
	}

	// Try HTTP status code extraction first.
	if status := extractHTTPStatus(msg); status > 0 {
		if reason := classifyByStatus(status); reason != "" {
			return &FailoverError{
				Reason:   reason,
				Provider: provider,
				Model:    model,
				Status:   status,
				Wrapped:  err,
			}
		}
	}

	// Message pattern matching (priority order from OpenClaw).
	if reason := classifyByMessage(msg); reason != "" {
		return &FailoverError{
			Reason:   reason,
			Provider: provider,
			Model:    model,
			Wrapped:  err,
		}
	}

	return nil
}

// classifyByStatus maps HTTP status codes to FailoverReason.
func classifyByStatus(status int) FailoverReason {
	switch {
	case status == 401 || status == 403:
		return FailoverAuth
	case status == 402:
		return FailoverBilling
	case status == 408:
		return FailoverTimeout
	case status == 429:
		return FailoverRateLimit
	case status == 400:
		return FailoverFormat
	case transientStatusCodes[status] || status >= 500 && status <= 599:
		return FailoverTimeout
	}
	return ""
}

// classifyByMessage matches error messages against patterns.
// Priority order matters (from OpenClaw classifyFailoverReason).
func classifyByMessage(msg string) FailoverReason {
	if matchesAny(msg, rateLimitPatterns) {
		return FailoverRateLimit
	}
	if matchesAny(msg, overloadedPatterns) {
		return FailoverRateLimit // Overloaded treated as rate_limit
	}
	if matchesAny(msg, billingPatterns) {
		return FailoverBilling
	}
	if matchesAny(msg, timeoutPatterns) {
		return FailoverTimeout
	}
	if matchesAny(msg, authPatterns) {
		return FailoverAuth
	}
	if matchesAny(msg, formatPatterns) {
		return FailoverFormat
	}
	return ""
}

// extractHTTPStatus extracts an HTTP status code from an error message.
// Looks for patterns like "status: 429", "status 429", "http/1.1 429", "http 429", or standalone "429".
func extractHTTPStatus(msg string) int {
	for _, p := range httpStatusPatterns {
		if m := p.FindStringSubmatch(msg); len(m) > 1 {
			return parseDigits(m[1])
		}
	}
	return 0
}

// IsImageDimensionError returns true if the message indicates an image dimension error.
func IsImageDimensionError(msg string) bool {
	return matchesAny(msg, imageDimensionPatterns)
}

// IsImageSizeError returns true if the message indicates an image file size error.
func IsImageSizeError(msg string) bool {
	return matchesAny(msg, imageSizePatterns)
}

// matchesAny checks if msg matches any of the patterns.
func matchesAny(msg string, patterns []errorPattern) bool {
	for _, p := range patterns {
		if p.regex != nil {
			if p.regex.MatchString(msg) {
				return true
			}
		} else if p.substring != "" {
			if strings.Contains(msg, p.substring) {
				return true
			}
		}
	}
	return false
}

// parseDigits converts a string of digits to an int.
func parseDigits(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

func errorHTTPMetadata(err error) (int, time.Duration, string) {
	var transport *protocoltypes.HTTPError
	if errors.As(err, &transport) {
		return transport.StatusCode, transport.RetryAfter, transport.Code
	}
	var openaiErr *openai.Error
	if errors.As(err, &openaiErr) {
		if openaiErr.Response != nil {
			return openaiErr.StatusCode, common.RetryAfter(openaiErr.Response.Header, time.Now()), openaiErr.Code
		}
		return openaiErr.StatusCode, 0, openaiErr.Code
	}
	var anthropicErr *anthropic.Error
	if errors.As(err, &anthropicErr) {
		if anthropicErr.Response != nil {
			return anthropicErr.StatusCode, common.RetryAfter(anthropicErr.Response.Header, time.Now()), ""
		}
		return anthropicErr.StatusCode, 0, ""
	}
	return 0, 0, ""
}

// url.Error also implements net.Error for configuration and certificate failures.
// Retry only its underlying transport failure, retaining the original diagnostic.
func modelNetworkError(err error) (net.Error, bool) {
	var requestErr *url.Error
	if errors.As(err, &requestErr) {
		err = requestErr.Err
	}
	var addressErr *net.AddrError
	var dnsErr *net.DNSError
	var tlsErr *tls.CertificateVerificationError
	var trustErr x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var certificateErr x509.CertificateInvalidError
	if errors.As(err, &addressErr) || errors.As(err, &dnsErr) && dnsErr.IsNotFound || errors.As(err, &tlsErr) ||
		errors.As(err, &trustErr) ||
		errors.As(err, &hostnameErr) ||
		errors.As(err, &certificateErr) {
		return nil, false
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return networkErr, true
	}
	return nil, false
}
