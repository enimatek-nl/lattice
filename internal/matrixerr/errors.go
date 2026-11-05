// Package matrixerr implements the Matrix standard error response format
// (spec.matrix.org/v1.19/client-server-api/#standard-error-response).
package matrixerr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Code is a Matrix error code. Codes are opaque strings from the specification
// plus the reserved vendor namespace.
type Code string

// Standard error codes from the v1.19 specification.
const (
	CodeUnknown               Code = "M_UNKNOWN"
	CodeUnrecognized          Code = "M_UNRECOGNIZED"
	CodeNotFound              Code = "M_NOT_FOUND"
	CodeUnknownToken          Code = "M_UNKNOWN_TOKEN"
	CodeMissingToken          Code = "M_MISSING_TOKEN"
	CodeBadJSON               Code = "M_BAD_JSON"
	CodeNotJSON               Code = "M_NOT_JSON"
	CodeForbidden             Code = "M_FORBIDDEN"
	CodeUnauthorized          Code = "M_UNAUTHORIZED"
	CodeUserInUse             Code = "M_USER_IN_USE"
	CodeInvalidUsername       Code = "M_INVALID_USERNAME"
	CodeWeakPassword          Code = "M_WEAK_PASSWORD"
	CodeUserDeactivated       Code = "M_USER_DEACTIVATED"
	CodeNoAuth                Code = "M_NO_AUTH"
	CodeMissingParams         Code = "M_MISSING_PARAMS"
	CodeInvalidParam          Code = "M_INVALID_PARAM"
	CodeNotYetRegistered      Code = "M_NOT_YET_REGISTERED"
	CodeGuestAccessForbidden  Code = "M_GUEST_ACCESS_FORBIDDEN"
	CodeLimitExceeded         Code = "M_LIMIT_EXCEEDED"
	CodeResourceLimitExceeded Code = "M_RESOURCE_LIMIT_EXCEEDED"
	CodeTooLarge              Code = "M_TOO_LARGE"
	CodeInvalidToken          Code = "M_INVALID_TOKEN"
	CodeInvalidSTS            Code = "M_INVALID_STK" //nolint:gocritic // Matrix code name
)

// Error is a Matrix error: an errcode, a human readable message and optional
// extra top-level members (for example the User-Interactive Auth body).
type Error struct {
	Code    Code           `json:"errcode"`
	Message string         `json:"error"`
	Extra   map[string]any `json:"-"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// New creates a new *Error.
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Newf creates a new *Error with a formatted message.
func Newf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithExtra attaches additional top-level members to the response body.
func (e *Error) WithExtra(extra map[string]any) *Error {
	e.Extra = extra
	return e
}

// As converts err to a *Error when possible. ok reports whether err was a *Error.
func As(err error) (*Error, bool) {
	var me *Error
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// From maps an arbitrary error to a *Error. Errors that are not *Error are
// reported as fallbackCode with a generic message so internal details are never
// leaked to clients (AGENTS.md §11).
func From(err error, fallbackCode Code, fallbackMessage string) *Error {
	if err == nil {
		return New(fallbackCode, fallbackMessage)
	}
	if me, ok := As(err); ok {
		return me
	}
	return New(fallbackCode, fallbackMessage)
}

// Write sends the error as a Matrix error response.
func Write(w http.ResponseWriter, status int, e *Error) {
	if e == nil {
		e = New(CodeUnknown, "The server encountered an internal error.")
	}
	body := map[string]any{
		"errcode": string(e.Code),
		"error":   e.Message,
	}
	for k, v := range e.Extra {
		body[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already sent; nothing useful can be done here.
		return
	}
}

// WriteError maps err to a Matrix error and writes it with status.
func WriteError(w http.ResponseWriter, status int, err error) {
	if e, ok := As(err); ok {
		Write(w, status, e)
		return
	}
	Write(w, status, New(CodeUnknown, "The server encountered an internal error."))
}
