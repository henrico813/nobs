package main

import "fmt"

// NOBSErrorCode keeps CLI, broker, vault, and sync errors aligned.
type NOBSErrorCode int

const (
	NOBSErrUnexpected NOBSErrorCode = iota + 1
	NOBSErrInvalidArgs
	NOBSErrInvalidPath
	NOBSErrNotFound
	NOBSErrForbidden
	NOBSErrBrokerUnavailable
	NOBSErrSyncUnavailable
)

// NOBSError is the shared typed error surface for nobs.
type NOBSError struct {
	Code    NOBSErrorCode `json:"code"`
	Message string        `json:"message"`
}

func (e *NOBSError) Error() string {
	return e.Message
}

func newNOBSError(code NOBSErrorCode, format string, args ...any) *NOBSError {
	return &NOBSError{Code: code, Message: fmt.Sprintf(format, args...)}
}
