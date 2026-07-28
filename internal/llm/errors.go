package llm

import "errors"

var (
	ErrInvalidConfig      = errors.New("llm: invalid configuration")
	ErrInvalidRequest     = errors.New("llm: invalid request")
	ErrStreamIdleTimeout  = errors.New("llm: stream idle timeout")
	ErrMalformedToolInput = errors.New("llm: malformed tool input")
	ErrIncompleteStream   = errors.New("llm: incomplete stream")
)
