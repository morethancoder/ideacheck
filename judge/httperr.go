package judge

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// HTTPError classifies a failed HTTP response: 408, 429 and 5xx are retryable and
// carry the server's Retry-After; everything else (400, 401, 404…) is final.
func HTTPError(status int, header http.Header, body string) error {
	err := fmt.Errorf("HTTP %d: %s", status, truncate(body, 300))
	if status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 {
		return &RetryableError{Err: err, After: retryDelay(header)}
	}
	return err
}

// retryDelay prefers the millisecond-precision retry-after-ms header (sent by
// TypeSafe and Anthropic) over the standard Retry-After.
func retryDelay(header http.Header) time.Duration {
	if ms, err := strconv.ParseFloat(header.Get("retry-after-ms"), 64); err == nil && ms > 0 {
		return time.Duration(ms * float64(time.Millisecond))
	}
	return ParseRetryAfter(header.Get("Retry-After"), time.Now())
}

// ParseRetryAfter reads either form of Retry-After: delay seconds or an HTTP date.
func ParseRetryAfter(v string, now time.Time) time.Duration {
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if at, err := http.ParseTime(v); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
