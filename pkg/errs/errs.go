// Package errs defines the error codes shared by every Tessera package.
//
// The codes map one to one onto Twirp error codes (admin API), signaling
// RequestResponse reasons and HTTP statuses. This package is a frozen shared
// contract (see docs/adr/0011): only additive changes, in a dedicated commit.
package errs

import (
	"errors"
	"fmt"
)

// Code is a machine-readable error class. Values equal Twirp error codes.
type Code string

// Error codes.
const (
	CodeInternal           Code = "internal"
	CodeInvalidArgument    Code = "invalid_argument"
	CodeMalformed          Code = "malformed"
	CodeNotFound           Code = "not_found"
	CodeAlreadyExists      Code = "already_exists"
	CodePermissionDenied   Code = "permission_denied"
	CodeUnauthenticated    Code = "unauthenticated"
	CodeResourceExhausted  Code = "resource_exhausted"
	CodeUnimplemented      Code = "unimplemented"
	CodeUnavailable        Code = "unavailable"
	CodeFailedPrecondition Code = "failed_precondition"
)

// Error is an error with a Code. Use errors.Is(err, errs.ErrNotFound) to test
// the class of any error produced with New or Wrap.
type Error struct {
	Code Code
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	switch {
	case e.Err != nil && e.Msg != "":
		return e.Msg + ": " + e.Err.Error()
	case e.Err != nil:
		return e.Err.Error()
	case e.Msg != "":
		return e.Msg
	default:
		return string(e.Code)
	}
}

// Unwrap returns the wrapped error.
func (e *Error) Unwrap() error { return e.Err }

// Is reports whether target is an *Error with the same Code. This lets
// errors.Is match any error against the sentinels below by class.
func (e *Error) Is(target error) bool {
	var t *Error
	if errors.As(target, &t) {
		return t.Code == e.Code
	}
	return false
}

// Sentinels, one per code. Compare with errors.Is.
var (
	ErrInternal           = &Error{Code: CodeInternal, Msg: "internal error"}
	ErrInvalidArgument    = &Error{Code: CodeInvalidArgument, Msg: "invalid argument"}
	ErrMalformed          = &Error{Code: CodeMalformed, Msg: "malformed request"}
	ErrNotFound           = &Error{Code: CodeNotFound, Msg: "not found"}
	ErrAlreadyExists      = &Error{Code: CodeAlreadyExists, Msg: "already exists"}
	ErrPermissionDenied   = &Error{Code: CodePermissionDenied, Msg: "permission denied"}
	ErrUnauthenticated    = &Error{Code: CodeUnauthenticated, Msg: "unauthenticated"}
	ErrResourceExhausted  = &Error{Code: CodeResourceExhausted, Msg: "resource exhausted"}
	ErrUnimplemented      = &Error{Code: CodeUnimplemented, Msg: "not implemented"}
	ErrUnavailable        = &Error{Code: CodeUnavailable, Msg: "unavailable"}
	ErrFailedPrecondition = &Error{Code: CodeFailedPrecondition, Msg: "failed precondition"}
)

// New returns an *Error with the given code and formatted message.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Wrap returns an *Error with the given code wrapping err.
func Wrap(code Code, err error, msg string) *Error {
	return &Error{Code: code, Msg: msg, Err: err}
}

// NotImplemented returns an ErrUnimplemented-class error naming the feature.
// Phase 0 stubs use it so callers can see which piece is missing.
func NotImplemented(what string) *Error {
	return &Error{Code: CodeUnimplemented, Msg: what + ": not implemented"}
}

// CodeOf returns the Code of err, CodeInternal for foreign errors and "" for nil.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// HTTPStatus maps a Code to the HTTP status used by the Twirp protocol.
func HTTPStatus(c Code) int {
	switch c {
	case CodeInvalidArgument, CodeMalformed:
		return 400
	case CodeUnauthenticated:
		return 401
	case CodePermissionDenied:
		return 403
	case CodeNotFound:
		return 404
	case CodeAlreadyExists:
		return 409
	case CodeFailedPrecondition:
		return 412
	case CodeResourceExhausted:
		return 429
	case CodeUnimplemented:
		return 501
	case CodeUnavailable:
		return 503
	default:
		return 500
	}
}
