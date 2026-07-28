package llm

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestHTTPProviderErrorClassifiesRetryableStatuses(t *testing.T) {
	tests := []struct {
		status    int
		retryable bool
	}{
		{status: http.StatusBadRequest},
		{status: http.StatusUnauthorized},
		{status: http.StatusForbidden},
		{status: http.StatusTooManyRequests, retryable: true},
		{status: 529, retryable: true},
		{status: http.StatusInternalServerError, retryable: true},
		{status: http.StatusBadGateway, retryable: true},
		{status: http.StatusServiceUnavailable, retryable: true},
		{status: http.StatusGatewayTimeout, retryable: true},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			err := NewHTTPProviderError("test", test.status, http.Header{}, "request failed")
			if err.Retryable != test.retryable {
				t.Fatalf("status %d retryable = %t", test.status, err.Retryable)
			}
		})
	}
}

func TestHTTPProviderErrorParsesRetryAfter(t *testing.T) {
	header := http.Header{"Retry-After": []string{"1.5"}}
	err := NewHTTPProviderError("test", http.StatusTooManyRequests, header, "busy")
	if err.RetryAfter != 1500*time.Millisecond {
		t.Fatalf("retry after = %s", err.RetryAfter)
	}
}

func TestRetryPolicyUsesExponentialBackoffAndServerDelay(t *testing.T) {
	policy := RetryPolicy{BaseDelay: time.Second, MaxDelay: 8 * time.Second, Jitter: func(time.Duration) time.Duration { return 0 }}
	if got := policy.Delay(2, errors.New("temporary")); got != 4*time.Second {
		t.Fatalf("delay = %s", got)
	}
	providerErr := &ProviderError{RetryAfter: 7 * time.Second, Retryable: true}
	if got := policy.Delay(0, providerErr); got != 7*time.Second {
		t.Fatalf("server delay = %s", got)
	}
}

func TestRetryPolicyWaitHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	policy := RetryPolicy{BaseDelay: time.Second, MaxDelay: time.Second}
	if err := policy.Wait(ctx, 0, errors.New("temporary")); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v", err)
	}
}

func TestNormalizeProviderErrorMarksIncompleteStreamRetryable(t *testing.T) {
	err := NormalizeProviderError("test", ErrIncompleteStream)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable || providerErr.ErrorType != "incomplete_stream" {
		t.Fatalf("provider error = %#v", err)
	}
}
