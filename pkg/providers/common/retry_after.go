package common

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryAfter parses delay seconds, milliseconds or an HTTP date.
// Advice beyond one day remains above the application recovery budget.
func RetryAfter(header http.Header, now time.Time) time.Duration {
	if milliseconds, err := strconv.ParseFloat(header.Get("Retry-After-Ms"), 64); err == nil && milliseconds >= 0 {
		return time.Duration(min(milliseconds, 86400000) * float64(time.Millisecond))
	}
	value := strings.TrimSpace(header.Get("Retry-After"))
	if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds >= 0 {
		return time.Duration(min(seconds, 86400) * float64(time.Second))
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}
