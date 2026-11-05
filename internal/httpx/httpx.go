// Package httpx contains HTTP helpers shared by the Matrix API handlers: JSON
// reading/writing with specification error codes, and access token extraction.
package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/enimatek/lattice/internal/matrixerr"
)

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already flushed; the only thing left is to stop writing.
		return
	}
}

// DecodeJSON reads a JSON request body into target, applying the size limit and
// mapping failures to the specification's error codes:
//
//	M_TOO_LARGE  body over the limit
//	M_NOT_JSON   body is not well-formed JSON
//	M_BAD_JSON   well-formed JSON with the wrong shape or invalid numbers
func DecodeJSON(r *http.Request, maxBytes int64, target any) error {
	if r.Body == nil {
		return setEmptyIfSet(target)
	}
	if r.ContentLength == 0 {
		return setEmptyIfSet(target)
	}
	// The body is read through a hard limit rather than MaxBytesReader, because a
	// JSON decoder turns the reader's overflow error into a syntax error and the
	// distinction between "too large" and "not JSON" is protocol visible.
	limit := maxBytes
	if limit <= 0 {
		limit = int64(1) << 40
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return matrixerr.New(matrixerr.CodeTooLarge, "Request body is too large.")
		}
		return matrixerr.New(matrixerr.CodeNotJSON, "Request body could not be read.")
	}
	if int64(len(data)) > limit {
		return matrixerr.New(matrixerr.CodeTooLarge, "Request body is too large.")
	}
	if len(data) == 0 {
		return setEmptyIfSet(target)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(target); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			return matrixerr.New(matrixerr.CodeTooLarge, "Request body is too large.")
		case errors.Is(err, io.EOF):
			return matrixerr.New(matrixerr.CodeNotJSON, "Request body is empty.")
		default:
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &typeErr) {
				return matrixerr.New(matrixerr.CodeBadJSON, fmt.Sprintf("Improper JSON: %s", typeErr.Field))
			}
			if isSyntaxError(err) {
				return matrixerr.New(matrixerr.CodeNotJSON, "Request body is not valid JSON.")
			}
			return matrixerr.New(matrixerr.CodeBadJSON, "Improper JSON in request body.")
		}
	}
	// Reject trailing content after the JSON value.
	if dec.More() {
		return matrixerr.New(matrixerr.CodeNotJSON, "Request body contains trailing data.")
	}
	return nil
}

// DecodeJSONOrError decodes a request body and, on failure, writes the
// protocol-defined error response. It reports whether the handler may continue.
func DecodeJSONOrError(w http.ResponseWriter, r *http.Request, maxBytes int64, target any) bool {
	err := DecodeJSON(r, maxBytes, target)
	if err == nil {
		return true
	}
	if me, ok := matrixerr.As(err); ok {
		if me.Code == matrixerr.CodeTooLarge {
			matrixerr.Write(w, http.StatusRequestEntityTooLarge, me)
			return false
		}
		matrixerr.Write(w, http.StatusBadRequest, me)
		return false
	}
	matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeNotJSON,
		"Request body is not valid JSON."))
	return false
}

// setEmptyIfSet lets a missing body decode into zero-valued handler structs.
func setEmptyIfSet(target any) error {
	if target == nil {
		return nil
	}
	return setEmpty(target)
}

func isSyntaxError(err error) bool {
	var syn *json.SyntaxError
	return errors.As(err, &syn)
}

func setEmpty(target any) error {
	return json.Unmarshal([]byte("{}"), target)
}

// DecodeRawBody reads the whole body, limited by maxBytes.
func DecodeRawBody(r *http.Request, maxBytes int64) ([]byte, error) {
	if maxBytes > 0 {
		r.Body = http.MaxBytesReader(nil, r.Body, maxBytes)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, matrixerr.New(matrixerr.CodeTooLarge, "Request body is too large.")
		}
		return nil, matrixerr.New(matrixerr.CodeNotJSON, "Cannot read request body.")
	}
	return body, nil
}

// AccessTokenFromRequest extracts the access token from the Authorization header
// ("Bearer <token>") or, for compatibility with older clients, the
// "access_token" query parameter.
func AccessTokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
			return strings.TrimSpace(h[7:])
		}
		return strings.TrimSpace(h)
	}
	return r.URL.Query().Get("access_token")
}
