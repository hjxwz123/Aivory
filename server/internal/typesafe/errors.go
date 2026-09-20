package typesafe

import (
	"errors"
	"fmt"
)

type ErrorKind string

const (
	ErrDisabled       ErrorKind = "disabled"
	ErrConfiguration  ErrorKind = "configuration"
	ErrValidation     ErrorKind = "validation"
	ErrAuthentication ErrorKind = "authentication"
	ErrRateLimit      ErrorKind = "rate_limit"
	ErrOverloaded     ErrorKind = "overloaded"
	ErrHTTP           ErrorKind = "http"
	ErrTransport      ErrorKind = "transport"
	ErrTimeout        ErrorKind = "timeout"
	ErrCanceled       ErrorKind = "canceled"
	ErrResponse       ErrorKind = "invalid_response"
	ErrModelVersion   ErrorKind = "model_version"
	ErrRecording      ErrorKind = "recording"
)

// Error deliberately excludes response bodies, input values and URLs: API
// validation errors can echo private state or credentials. Unwrap preserves
// errors.Is(err, context.DeadlineExceeded/Canceled) and recorder failures.
type Error struct {
	Kind       ErrorKind
	StatusCode int
	RequestID  string
	Message    string
	Cause      error
}

func (e *Error) Error() string {
	return fmt.Sprintf("typesafe: %s (status=%d): %s", e.Kind, e.StatusCode, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

func KindOf(err error) ErrorKind {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Kind
	}
	return ""
}

func failure(kind ErrorKind, message string) *Error {
	return &Error{Kind: kind, Message: message}
}
