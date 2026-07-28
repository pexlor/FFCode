package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
)

type ProviderError struct {
	Provider   string
	StatusCode int
	ErrorType  string
	RetryAfter time.Duration
	Retryable  bool
	Err        error
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "provider error"
	}
	prefix := strings.TrimSpace(e.Provider)
	if prefix == "" {
		prefix = "provider"
	}
	if e.StatusCode != 0 {
		prefix = fmt.Sprintf("%s HTTP %d", prefix, e.StatusCode)
	}
	if e.Err == nil {
		return prefix
	}
	return fmt.Sprintf("%s: %v", prefix, e.Err)
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewHTTPProviderError(provider string, statusCode int, header http.Header, message string) *ProviderError {
	return &ProviderError{
		Provider: provider, StatusCode: statusCode, ErrorType: http.StatusText(statusCode),
		RetryAfter: parseRetryAfter(header), Retryable: retryableStatus(statusCode), Err: errors.New(strings.TrimSpace(message)),
	}
}

func NormalizeProviderError(provider string, err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var existing *ProviderError
	if errors.As(err, &existing) {
		copy := *existing
		if copy.Provider == "" {
			copy.Provider = provider
		}
		return &copy
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		header := http.Header{}
		if apiErr.Response != nil {
			header = apiErr.Response.Header
		}
		result := NewHTTPProviderError(provider, apiErr.StatusCode, header, apiErr.Error())
		result.ErrorType = apiErr.Type
		result.Err = err
		return result
	}
	retryable := errors.Is(err, ErrMalformedToolInput) || errors.Is(err, ErrStreamIdleTimeout) || errors.Is(err, ErrIncompleteStream) || errors.Is(err, io.ErrUnexpectedEOF)
	var networkErr net.Error
	if errors.As(err, &networkErr) && (networkErr.Timeout() || networkErr.Temporary()) {
		retryable = true
	}
	if !retryable {
		return err
	}
	errorType := "transport_error"
	if errors.Is(err, ErrMalformedToolInput) {
		errorType = "malformed_tool_input"
	} else if errors.Is(err, ErrStreamIdleTimeout) {
		errorType = "stream_idle_timeout"
	} else if errors.Is(err, ErrIncompleteStream) {
		errorType = "incomplete_stream"
	}
	return &ProviderError{Provider: provider, ErrorType: errorType, Retryable: true, Err: err}
}

func retryableStatus(statusCode int) bool {
	return statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooManyRequests || statusCode == 529 || statusCode >= 500
}

func parseRetryAfter(header http.Header) time.Duration {
	if header == nil {
		return 0
	}
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		return max(0, time.Duration(seconds*float64(time.Second)))
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	return max(0, time.Until(when))
}

type RetryPolicy struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	Jitter     func(time.Duration) time.Duration
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxRetries: 2, BaseDelay: 500 * time.Millisecond, MaxDelay: 8 * time.Second}
}

func (p RetryPolicy) ShouldRetry(err error, retriesUsed int) bool {
	var providerErr *ProviderError
	return retriesUsed < p.MaxRetries && errors.As(err, &providerErr) && providerErr.Retryable
}

func (p RetryPolicy) Delay(retriesUsed int, err error) time.Duration {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) && providerErr.RetryAfter > 0 {
		return providerErr.RetryAfter
	}
	baseDelay := p.BaseDelay
	if baseDelay <= 0 {
		baseDelay = 500 * time.Millisecond
	}
	maxDelay := p.MaxDelay
	if maxDelay <= 0 {
		maxDelay = 8 * time.Second
	}
	exponent := min(max(retriesUsed, 0), 30)
	delay := time.Duration(math.Min(float64(maxDelay), float64(baseDelay)*math.Pow(2, float64(exponent))))
	jitter := p.Jitter
	if jitter == nil {
		jitter = func(maximum time.Duration) time.Duration {
			if maximum <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(maximum) + 1))
		}
	}
	return max(0, delay-jitter(delay/4))
}

func (p RetryPolicy) Wait(ctx context.Context, retriesUsed int, err error) error {
	return WaitForRetry(ctx, p.Delay(retriesUsed, err))
}

func WaitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
