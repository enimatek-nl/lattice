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
	CodeUnknown      Code = "M_UNKNOWN"
	CodeUnrecognized Code = "M_UNRECOGNIZED"
	// CodeUnableToGrantJoin is the join-specific "ask somebody else" answer: the joining user
	// is entitled to be in the room, but this resident server cannot stand behind the join
	// because none of its own members could have invited them. It is deliberately not a refusal.
	CodeUnableToGrantJoin Code = "M_UNABLE_TO_GRANT_JOIN"
	CodeNotFound          Code = "M_NOT_FOUND"
	CodeUnknownToken      Code = "M_UNKNOWN_TOKEN"
	CodeMissingToken      Code = "M_MISSING_TOKEN"
	CodeBadJSON           Code = "M_BAD_JSON"
	CodeNotJSON           Code = "M_NOT_JSON"
	// CodeBadRequest reports a request the server could not handle as asked. The
	// Client-Server API lists it; the Server-Server API has no such list, and a
	// transaction missing its origin needs a code that is not M_UNKNOWN.
	CodeBadRequest            Code = "M_BAD_REQUEST"
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
	// CodeNotYetUploaded belongs to the content repository: an `mxc://` URI that was created ahead of its
	// bytes (POST /_matrix/media/v1/create) and asked for before they arrive. It is not M_NOT_FOUND,
	// because the address exists and is waiting, which is a different answer for a client.
	CodeNotYetUploaded Code = "M_NOT_YET_UPLOADED"
	// CodeCannotOverwriteMedia belongs to the content repository's two-step upload: content was already
	// stored at this `mxc://` URI, and a second upload to the same address is refused rather than
	// silently replacing what other people already hold a link to.
	CodeCannotOverwriteMedia Code = "M_CANNOT_OVERWRITE_MEDIA"
	CodeInvalidToken         Code = "M_INVALID_TOKEN"
	CodeInvalidSTS           Code = "M_INVALID_STK" //nolint:gocritic // Matrix code name

	// Room and event errors from the Client-Server API.

	// CodeDuplicateAlias is Synapse's code for an alias already in use. It is not a
	// specification code, but an alias collision needs one and clients recognise this.
	CodeDuplicateAlias          Code = "M_DUPLICATE_ALIAS"
	CodeUnsupportedRoomVersion  Code = "M_UNSUPPORTED_ROOM_VERSION"
	CodeIncompatibleRoomVersion Code = "M_INCOMPATIBLE_ROOM_VERSION"
	CodeBadState                Code = "M_BAD_STATE"

	// CodeBadAlias reports an alias that is well formed but cannot be used: it does
	// not exist on this server, or it points at a different room than the one being
	// edited. Synapse uses this code and Complement asserts it.
	CodeBadAlias                  Code = "M_BAD_ALIAS"
	CodeCannotLeaveServerNotice   Code = "M_CANNOT_LEAVE_SERVER_NOTICE_ROOM"
	CodeResourceLimitExceededRoom Code = "M_RESOURCE_LIMIT_EXCEEDED"
)

// Error is a Matrix error: an errcode, a human readable message and optional
// extra top-level members (for example the User-Interactive Auth body).
type Error struct {
	Code    Code           `json:"errcode"`
	Message string         `json:"error"`
	Extra   map[string]any `json:"-"`
	// Status overrides the status the code alone would carry, and is used for the
	// refusals whose cause is another server's answer. `M_UNKNOWN` on its own would be
	// read as a fault of this homeserver, and an operator would look here when the
	// problem is a peer that refused or did not reply.
	Status int `json:"-"`
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

// WithStatus attaches the HTTP status this error should be answered with.
func (e *Error) WithStatus(status int) *Error {
	e.Status = status
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

// statusByCode maps the error codes whose HTTP status the specification fixes. The
// table exists because a handler that guesses gets these wrong in ways clients act
// on: a client retries on 429, logs out on 401 and stops on 403.
var statusByCode = map[Code]int{
	CodeNotFound:     http.StatusNotFound,
	CodeUnknownToken: http.StatusUnauthorized,
	CodeMissingToken: http.StatusUnauthorized,
	CodeBadJSON:      http.StatusBadRequest,
	CodeNotJSON:      http.StatusBadRequest,
	// A sync token this server did not issue is an authentication-style failure: the
	// Client-Server specification answers it with 401 and M_INVALID_TOKEN.
	CodeInvalidToken:            http.StatusUnauthorized,
	CodeForbidden:               http.StatusForbidden,
	CodeUnauthorized:            http.StatusUnauthorized,
	CodeUserInUse:               http.StatusConflict,
	CodeInvalidUsername:         http.StatusBadRequest,
	CodeWeakPassword:            http.StatusBadRequest,
	CodeUserDeactivated:         http.StatusForbidden,
	CodeNoAuth:                  http.StatusForbidden,
	CodeMissingParams:           http.StatusBadRequest,
	CodeInvalidParam:            http.StatusBadRequest,
	CodeNotYetRegistered:        http.StatusForbidden,
	CodeGuestAccessForbidden:    http.StatusForbidden,
	CodeLimitExceeded:           http.StatusTooManyRequests,
	CodeResourceLimitExceeded:   http.StatusServiceUnavailable,
	CodeTooLarge:                http.StatusRequestEntityTooLarge,
	CodeNotYetUploaded:          http.StatusGatewayTimeout,
	CodeUnrecognized:            http.StatusBadRequest,
	CodeUnableToGrantJoin:       http.StatusBadRequest,
	CodeUnsupportedRoomVersion:  http.StatusBadRequest,
	CodeDuplicateAlias:          http.StatusConflict,
	CodeCannotOverwriteMedia:    http.StatusConflict,
	CodeIncompatibleRoomVersion: http.StatusBadRequest,
	CodeBadState:                http.StatusBadRequest,
	CodeBadAlias:                http.StatusBadRequest,
}

// Status returns the HTTP status the specification associates with an error code. An
// unlisted code means the caller must choose, so 400 is returned: an unspecified
// Matrix error is the caller's problem, not the client's.
func Status(e *Error) int {
	if e == nil {
		return http.StatusInternalServerError
	}
	if e.Status != 0 {
		return e.Status
	}
	if status, ok := statusByCode[e.Code]; ok {
		return status
	}
	return http.StatusBadRequest
}

// WriteServiceError writes a service-layer error with the status the protocol
// prescribes. Anything that is not a Matrix error is an internal fault and becomes a
// 500 M_UNKNOWN with no detail, so internal messages do not reach clients.
func WriteServiceError(w http.ResponseWriter, err error) {
	if e, ok := As(err); ok {
		Write(w, Status(e), e)
		return
	}
	Write(w, http.StatusInternalServerError, New(CodeUnknown, "The server encountered an internal error."))
}

// WriteError maps err to a Matrix error and writes it with status.
func WriteError(w http.ResponseWriter, status int, err error) {
	if e, ok := As(err); ok {
		Write(w, status, e)
		return
	}
	Write(w, status, New(CodeUnknown, "The server encountered an internal error."))
}
