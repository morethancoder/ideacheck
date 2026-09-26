package judge

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestHTTPErrorClassification(t *testing.T) {
	for status, retryable := range map[int]bool{400: false, 401: false, 404: false, 408: true, 429: true, 499: false, 500: true, 503: true} {
		var re *RetryableError
		if got := errors.As(HTTPError(status, http.Header{}, "x"), &re); got != retryable {
			t.Errorf("HTTP %d retryable = %v, want %v", status, got, retryable)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"3": 3 * time.Second, "0.5": 500 * time.Millisecond, "": 0, "0": 0, "-4": 0, "soon": 0,
		"Sat, 19 Sep 2026 12:00:10 GMT": 10 * time.Second,
		"Sat, 19 Sep 2026 11:00:00 GMT": 0, // in the past
	}
	for in, want := range cases {
		if got := ParseRetryAfter(in, now); got != want {
			t.Errorf("ParseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
