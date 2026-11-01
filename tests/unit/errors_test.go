package unit

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// TestErrorStatuses pins which status code and errcode each constructor answers
// with. The pairing comes from the "Standards" section of the specification, and
// clients branch on it: an M_UNKNOWN_TOKEN must be a 401 or a client stops
// refreshing its session, and M_UNRECOGNIZED must be a 400 or a client cannot
// tell an unsupported request body from a missing room.
func TestErrorStatuses(t *testing.T) {
	tests := []struct {
		err     *matrix.Error
		code    matrix.ErrorCode
		status  int
		comment string
	}{
		{err: matrix.ErrInvalidParam("bad %s", "thing"), code: matrix.ErrCodeInvalidParam, status: http.StatusBadRequest},
		{err: matrix.ErrInvalidUsername("no"), code: matrix.ErrCodeInvalidUsername, status: http.StatusBadRequest},
		{err: matrix.ErrNotFound("nothing here"), code: matrix.ErrCodeNotFound, status: http.StatusNotFound},
		{err: matrix.ErrForbidden("cannot"), code: matrix.ErrCodeForbidden, status: http.StatusForbidden},
		{err: matrix.ErrUnauthorized("prove it"), code: matrix.ErrCodeUnauthorized, status: http.StatusUnauthorized},
		{err: matrix.ErrInvalidRequest("shape"), code: matrix.ErrCodeUnknown, status: http.StatusBadRequest,
			comment: "a malformed request is the request's fault, so it is a 400"},
		{err: matrix.ErrUnrecognized("no such thing"), code: matrix.ErrCodeUnrecognized, status: http.StatusNotFound,
			comment: "an unknown endpoint is a 404; an unsupported body is the same code at 400"},
		{err: matrix.ErrNotImplemented("/federation"), code: matrix.ErrCodeUnrecognized, status: http.StatusNotImplemented,
			comment: "a known endpoint the server refuses to serve is a 501"},
		{err: matrix.ErrUnknownToken(false), code: matrix.ErrCodeUnknownToken, status: http.StatusUnauthorized},
		{err: matrix.ErrUnknownToken(true), code: matrix.ErrCodeUnknownToken, status: http.StatusUnauthorized,
			comment: "a soft logout keeps the same code so clients do not discard the session"},
		{err: matrix.ErrMissingToken(), code: matrix.ErrCodeMissingToken, status: http.StatusUnauthorized},
		{err: matrix.ErrBadJSON(errors.New("bad json")), code: matrix.ErrCodeBadJSON, status: http.StatusBadRequest},
		{err: matrix.ErrNotJSON("not json"), code: matrix.ErrCodeNotJSON, status: http.StatusBadRequest},
		{err: matrix.ErrLimitExceeded(500), code: matrix.ErrCodeLimit, status: http.StatusTooManyRequests},
		{err: matrix.ErrTooLarge(1024), code: matrix.ErrCodeTooLarge, status: http.StatusRequestEntityTooLarge},
		{err: matrix.ErrConflict(matrix.ErrCodeUserInUse, "taken"), code: matrix.ErrCodeUserInUse, status: http.StatusConflict},
		{err: matrix.ErrInternal(errors.New("disk on fire")), code: matrix.ErrCodeUnknown, status: http.StatusInternalServerError,
			comment: "the message must never repeat the cause: it can carry file names and SQL"},
	}
	for _, tt := range tests {
		if tt.err.Code != tt.code {
			t.Errorf("%v: errcode = %s, want %s", tt.err, tt.err.Code, tt.code)
		}
		if got := tt.err.HTTPStatus(); got != tt.status {
			t.Errorf("%v: status = %d, want %d", tt.err, got, tt.status)
		}
	}
	// A hand built error without a status still answers something sensible.
	if got := (&matrix.Error{Code: matrix.ErrCodeUnknown, Message: "x"}).HTTPStatus(); got != http.StatusBadRequest {
		t.Errorf("an error with no status answered %d, want 400", got)
	}
}

// TestErrorBodies pins the JSON a client reads. Only errcode, error and the two
// optional keys may appear: a client that sees an unexpected key may stop
// parsing, and the Status and cause fields must not leak into the body.
func TestErrorBodies(t *testing.T) {
	body, err := json.Marshal(matrix.ErrForbidden("you may not"))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	assertJSONFields(t, string(body), map[string]any{
		"errcode": "M_FORBIDDEN",
		"error":   "you may not",
	}, []string{"Status", "cause", "retry_after_ms", "Extra"})

	body, err = json.Marshal(matrix.ErrLimitExceeded(1200))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	assertJSONFields(t, string(body), map[string]any{
		"errcode":        "M_LIMIT_EXCEEDED",
		"retry_after_ms": float64(1200),
	}, nil)

	// User-Interactive Auth challenges carry extra top level keys.
	challenge := matrix.ErrUnauthorized("pick a stage").WithExtra(matrix.Object{
		"flows":   matrix.Array{matrix.Object{"stages": matrix.Array{"m.login.password"}}},
		"session": "abc",
	})
	body, err = json.Marshal(challenge)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	assertJSONFields(t, string(body), map[string]any{
		"errcode": "M_UNAUTHORIZED",
		"session": "abc",
	}, nil)
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if _, ok := decoded["flows"]; !ok {
		t.Errorf("the flows of a UIA challenge are missing: %s", body)
	}

	// An internal error keeps its cause for logs and errors.Is, but never shows it
	// to the client.
	cause := errors.New("no such table: events")
	internal := matrix.ErrInternal(cause)
	if !errors.Is(internal, cause) {
		t.Errorf("the cause is no longer reachable through errors.Is")
	}
	body, err = json.Marshal(internal)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if want := "no such table"; strings.Contains(string(body), want) {
		t.Errorf("the response body repeats the internal cause (%q): %s", want, body)
	}
}

// TestErrorConversion pins how a handler turns any error into a response: an
// error that is already a Matrix error is passed through, a storage "no such
// record" becomes M_NOT_FOUND, and anything else is M_UNKNOWN.
func TestErrorConversion(t *testing.T) {
	forbidden := matrix.ErrForbidden("cannot")
	if got := matrix.ErrAs(forbidden, persist.IsNotFound); got != forbidden {
		t.Errorf("a Matrix error was replaced by %v", got)
	}
	notFound := matrix.ErrAs(gorm.ErrRecordNotFound, persist.IsNotFound)
	if notFound.Code != matrix.ErrCodeNotFound || notFound.HTTPStatus() != http.StatusNotFound {
		t.Errorf("a missing record answered %v, want 404 M_NOT_FOUND", notFound)
	}
	unknown := matrix.ErrAs(errors.New("boom"), persist.IsNotFound)
	if unknown.Code != matrix.ErrCodeUnknown || unknown.HTTPStatus() != http.StatusInternalServerError {
		t.Errorf("an unexplained error answered %v, want 500 M_UNKNOWN", unknown)
	}
	// A wrapped Matrix error is still found: services wrap with %w on purpose.
	wrapped := fmt.Errorf("while sending: %w", matrix.ErrNotFound("no event"))
	if got := matrix.ErrAs(wrapped, persist.IsNotFound); got.Code != matrix.ErrCodeNotFound {
		t.Errorf("a wrapped M_NOT_FOUND answered %v, want 404", got)
	}
	// A nil predicate may not panic: not every caller has a storage layer to ask.
	if got := matrix.ErrAs(gorm.ErrRecordNotFound, nil); got.Code != matrix.ErrCodeUnknown {
		t.Errorf("with no not-found predicate the answer was %v, want M_UNKNOWN", got)
	}
}

// TestWriteError covers the one place every handler answers an error through.
func TestWriteError(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{err: matrix.ErrNotFound("no room"), status: http.StatusNotFound, code: "M_NOT_FOUND"},
		{err: matrix.ErrForbidden("cannot"), status: http.StatusForbidden, code: "M_FORBIDDEN"},
		{err: matrix.ErrLimitExceeded(250), status: http.StatusTooManyRequests, code: "M_LIMIT_EXCEEDED"},
		{err: gorm.ErrRecordNotFound, status: http.StatusNotFound, code: "M_NOT_FOUND"},
		{err: errors.New("disk on fire"), status: http.StatusInternalServerError, code: "M_UNKNOWN"},
	}
	for _, tt := range tests {
		recorder := httptest.NewRecorder()
		auth.WriteError(recorder, tt.err)
		if recorder.Code != tt.status {
			t.Errorf("%v: status = %d, want %d", tt.err, recorder.Code, tt.status)
		}
		if got := recorder.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("%v: Content-Type = %q", tt.err, got)
		}
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Errorf("%v: body is not JSON: %s", tt.err, recorder.Body)
			continue
		}
		if body["errcode"] != tt.code {
			t.Errorf("%v: errcode = %v, want %s (%s)", tt.err, body["errcode"], tt.code, recorder.Body)
		}
		if _, ok := body["error"].(string); !ok {
			t.Errorf("%v: the body has no error message: %s", tt.err, recorder.Body)
		}
	}
}

// assertJSONFields checks that a JSON object carries the wanted keys with the
// wanted values and none of the forbidden ones.
func assertJSONFields(t *testing.T, body string, want map[string]any, forbidden []string) {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("body is not JSON: %s", body)
	}
	for key, value := range want {
		if decoded[key] != value {
			t.Errorf("%s = %v, want %v (%s)", key, decoded[key], value, body)
		}
	}
	for _, key := range forbidden {
		if _, ok := decoded[key]; ok {
			t.Errorf("the body carries %q, which must stay internal: %s", key, body)
		}
	}
}
