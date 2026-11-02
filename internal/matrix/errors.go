package matrix

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ErrorCode is a Matrix error code as defined by the client-server API.
type ErrorCode string

// Error codes from the "Standards" section of the spec.
const (
	ErrCodeUnknown              ErrorCode = "M_UNKNOWN"
	ErrCodeForbidden            ErrorCode = "M_FORBIDDEN"
	ErrCodeUnauthorized         ErrorCode = "M_UNAUTHORIZED"
	ErrCodeUnknownToken         ErrorCode = "M_UNKNOWN_TOKEN"
	ErrCodeMissingToken         ErrorCode = "M_MISSING_TOKEN"
	ErrCodeBadJSON              ErrorCode = "M_BAD_JSON"
	ErrCodeNotJSON              ErrorCode = "M_NOT_JSON"
	ErrCodeNotFound             ErrorCode = "M_NOT_FOUND"
	ErrCodeLimit                ErrorCode = "M_LIMIT_EXCEEDED"
	ErrCodeTooLarge             ErrorCode = "M_TOO_LARGE"
	ErrCodeCannotLeaveServer    ErrorCode = "M_CANNOT_LEAVE_SERVER"
	ErrCodeInvalidUsername      ErrorCode = "M_INVALID_USERNAME"
	ErrCodeUserInUse            ErrorCode = "M_USER_IN_USE"
	ErrCodeGuestAccessForbidden ErrorCode = "M_GUEST_ACCESS_FORBIDDEN"
	ErrCodeUnrecognized         ErrorCode = "M_UNRECOGNIZED"
	ErrCodeInvalidParam         ErrorCode = "M_INVALID_PARAM"
	ErrCodeWeakPassword         ErrorCode = "M_WEAK_PASSWORD"
	ErrCodeTokenExpired         ErrorCode = "M_TOKEN_EXPIRED"
	ErrCodeExclusive            ErrorCode = "M_EXCLUSIVE"
	ErrCodeAliasExists          ErrorCode = "M_ALIAS_EXISTS"
	ErrCodeBadAlias             ErrorCode = "M_BAD_ALIAS"
	ErrCodeCannotOverwrite      ErrorCode = "M_CANNOT_OVERWRITE_MEDIA"
	ErrCodeNotYetUploaded       ErrorCode = "M_NOT_YET_UPLOADED"
	ErrCodeReserved             ErrorCode = "M_RESERVED"
	ErrCodeSessionExpired       ErrorCode = "M_SESSION_EXPIRED"
	ErrCodeFlowNotFound         ErrorCode = "M_FLOW_NOT_FOUND"
	ErrCodeIncompleteStages     ErrorCode = "M_INCOMPLETE_STAGES"
	ErrCodeWrongRoomServer      ErrorCode = "M_WRONG_ROOM_SERVER"
	ErrCodeUnsupportedRoomVer   ErrorCode = "M_UNSUPPORTED_ROOM_VERSION"
	ErrCodeIncompatibleRoomVer  ErrorCode = "M_INCOMPATIBLE_ROOM_VERSION"
	ErrCodeThreepidNotFound     ErrorCode = "M_THREEPID_NOT_FOUND"
	ErrCodeThreepidInUse        ErrorCode = "M_THREEPID_IN_USE"
	ErrCodeWrongKeyVersion      ErrorCode = "M_WRONG_KEY_VERSION"
)

// Error is a Matrix API error. It carries the HTTP status that should be used
// when responding to a client.
type Error struct {
	Code    ErrorCode `json:"errcode"`
	Message string    `json:"error"`
	// Status is the HTTP status code to respond with. It is not serialised.
	Status int `json:"-"`
	// RetryAfterMS is optionally included for rate limited responses.
	RetryAfterMS int64 `json:"retry_after_ms,omitempty"`
	// Extra holds additional top level keys the endpoint adds to the body, such
	// as the flows of a User-Interactive Auth challenge. It is serialised by
	// MarshalJSON rather than by the struct encoder.
	Extra Object `json:"-"`
	// cause keeps the underlying implementation error for logs and errors.Is.
	cause error
}

// MarshalJSON writes the errcode and error keys plus every Extra field, so an
// error can carry endpoint specific payload (User-Interactive Auth flows) while
// staying a single type.
func (e *Error) MarshalJSON() ([]byte, error) {
	body := make(Object, len(e.Extra)+3)
	for key, value := range e.Extra {
		body[key] = value
	}
	body["errcode"] = e.Code
	body["error"] = e.Message
	if e.RetryAfterMS != 0 {
		body["retry_after_ms"] = e.RetryAfterMS
	}
	return json.Marshal(body)
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the wrapped implementation error, if any.
func (e *Error) Unwrap() error { return e.cause }

// HTTPStatus returns the HTTP status code for the error, defaulting to 400.
func (e *Error) HTTPStatus() int {
	if e.Status == 0 {
		return http.StatusBadRequest
	}
	return e.Status
}

// WithCause attaches an underlying cause to a Matrix error.
func (e *Error) WithCause(err error) *Error {
	out := *e
	out.cause = err
	return &out
}

// WithExtra returns a copy of the error whose response body carries the given
// top level fields in addition to errcode and error.
func (e *Error) WithExtra(extra Object) *Error {
	out := *e
	out.Extra = extra
	return &out
}

// newError builds an error with the given code, message and status.
func newError(code ErrorCode, status int, format string, args ...any) *Error {
	return &Error{Code: code, Status: status, Message: fmt.Sprintf(format, args...)}
}

// NewError builds an error with an explicit HTTP status.
func NewError(code ErrorCode, status int, message string) *Error {
	return &Error{Code: code, Status: status, Message: message}
}

// ErrInvalidParam returns a 400 M_INVALID_PARAM error.
func ErrInvalidParam(format string, args ...any) *Error {
	return newError(ErrCodeInvalidParam, http.StatusBadRequest, format, args...)
}

// ErrInvalidUsername returns a 400 M_INVALID_USERNAME error.
func ErrInvalidUsername(format string, args ...any) *Error {
	return newError(ErrCodeInvalidUsername, http.StatusBadRequest, format, args...)
}

// ErrNotFound returns a 404 M_NOT_FOUND error.
func ErrNotFound(format string, args ...any) *Error {
	return newError(ErrCodeNotFound, http.StatusNotFound, format, args...)
}

// ErrForbidden returns a 403 M_FORBIDDEN error.
func ErrForbidden(format string, args ...any) *Error {
	return newError(ErrCodeForbidden, http.StatusForbidden, format, args...)
}

// ErrUnauthorized returns a 401 M_UNAUTHORIZED error, which is how a server
// asks a client to complete a User-Interactive Auth stage.
func ErrUnauthorized(format string, args ...any) *Error {
	return newError(ErrCodeUnauthorized, http.StatusUnauthorized, format, args...)
}

// ErrInvalidRequest returns a 400 M_UNKNOWN error.
func ErrInvalidRequest(format string, args ...any) *Error {
	return newError(ErrCodeUnknown, http.StatusBadRequest, format, args...)
}

// ErrUnrecognized returns a 404 M_UNRECOGNIZED error for unknown endpoints and
// unimplemented functionality.
func ErrUnrecognized(format string, args ...any) *Error {
	return newError(ErrCodeUnrecognized, http.StatusNotFound, format, args...)
}

// ErrNotImplemented returns a 501 M_UNRECOGNIZED error.
func ErrNotImplemented(feature string) *Error {
	return newError(ErrCodeUnrecognized, http.StatusNotImplemented,
		"endpoint or feature %s is not implemented by this homeserver", feature)
}

// ErrUnknownToken returns a 401 M_UNKNOWN_TOKEN error. When softLogout is true
// clients are told not to clear their session.
func ErrUnknownToken(softLogout bool) *Error {
	e := newError(ErrCodeUnknownToken, http.StatusUnauthorized,
		"session has been expired or closed")
	e.Message = "Unknown token or token expired"
	if softLogout {
		e.Message += " (soft logout)"
	}
	return e
}

// ErrMissingToken returns a 401 M_MISSING_TOKEN error.
func ErrMissingToken() *Error {
	return newError(ErrCodeMissingToken, http.StatusUnauthorized,
		"an access token was required but none was provided")
}

// ErrBadJSON returns a 400 M_BAD_JSON error for payloads with the wrong shape.
func ErrBadJSON(err error) *Error {
	return newError(ErrCodeBadJSON, http.StatusBadRequest, "request body had the wrong shape: %v", err).
		WithCause(err)
}

// ErrBadAlias returns a 400 M_BAD_ALIAS error for a canonical alias that does
// not point at the room it is written into. The specification names this code
// for m.room.canonical_alias alone, and it is deliberately not M_NOT_FOUND: a
// client that reads the error cannot tell an unknown alias from a mistake in its
// own room graph otherwise.
func ErrBadAlias(format string, args ...any) *Error {
	return newError(ErrCodeBadAlias, http.StatusBadRequest, format, args...)
}

// ErrNotYetUploaded returns a 504 M_NOT_YET_UPLOADED error for a media id that
// was reserved but has no content yet. The reservation is real, so the answer is
// "not now" rather than "does not exist": a client uploading a room avatar
// downloads it straight after the PUT and needs to retry rather than give up.
func ErrNotYetUploaded(format string, args ...any) *Error {
	return newError(ErrCodeNotYetUploaded, http.StatusGatewayTimeout, format, args...)
}

// ErrUserInUse returns a 400 M_USER_IN_USE error for a registration that asks
// for an account name that is taken. It is a client error rather than a conflict:
// the name is not in a temporary state the client can wait out, it belongs to
// somebody else.
func ErrUserInUse(format string, args ...any) *Error {
	return newError(ErrCodeUserInUse, http.StatusBadRequest, format, args...)
}

// ErrNotJSON returns a 400 M_NOT_JSON error for unparsable payloads.
func ErrNotJSON(format string, args ...any) *Error {
	return newError(ErrCodeNotJSON, http.StatusBadRequest, format, args...)
}

// ErrLimitExceeded returns a 429 M_LIMIT_EXCEEDED error.
func ErrLimitExceeded(retryAfterMS int64) *Error {
	e := newError(ErrCodeLimit, http.StatusTooManyRequests, "Too many requests")
	e.RetryAfterMS = retryAfterMS
	return e
}

// ErrTooLarge returns a 413 M_TOO_LARGE error for oversized uploads.
func ErrTooLarge(limit int64) *Error {
	return newError(ErrCodeTooLarge, http.StatusRequestEntityTooLarge,
		"upload exceeds the maximum size of %d bytes", limit)
}

// ErrAs converts any error into a *matrix.Error so handlers can respond. notFound
// is consulted for implementation level "record not found" errors so this
// package stays free of storage layer imports.
func ErrAs(err error, notFound func(error) bool) *Error {
	var merr *Error
	if errors.As(err, &merr) {
		return merr
	}
	if notFound != nil && notFound(err) {
		return ErrNotFound("%v", err)
	}
	return ErrInternal(err)
}

// HasCode reports whether err is, or wraps, a spec error carrying code. Service
// methods turn storage misses into spec errors, so a caller that has to tell "this
// room is unknown here" from "something is broken" asks about the code rather than
// about a storage error that has already been converted. Comparing messages would
// work until someone reworded an error, which is the sort of thing a rename does.
func HasCode(err error, code ErrorCode) bool {
	var merr *Error
	if !errors.As(err, &merr) {
		return false
	}
	return merr.Code == code
}

// ErrInternal returns a 500 M_UNKNOWN error wrapping the cause.
func ErrInternal(err error) *Error {
	e := newError(ErrCodeUnknown, http.StatusInternalServerError, "internal server error")
	return e.WithCause(err)
}

// ErrConflict returns a 409 error with the given code and message.
func ErrConflict(code ErrorCode, format string, args ...any) *Error {
	return newError(code, http.StatusConflict, format, args...)
}
