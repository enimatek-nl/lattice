package unit

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enimatek/lattice/internal/httpserver"
	"github.com/enimatek/lattice/internal/matrixerr"
)

func TestMatrixErrorWriteShape(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	matrixerr.Write(rec, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden, "nope"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("content type = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["errcode"] != "M_FORBIDDEN" || body["error"] != "nope" {
		t.Errorf("unexpected body: %v", body)
	}
}

func TestMatrixErrorExtrasAreMerged(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	err := matrixerr.New(matrixerr.CodeNoAuth, "Missing privileges.").
		WithExtra(map[string]any{"session": "abc", "flows": []string{"m.login.dummy"}, "completed": []string{}})
	matrixerr.Write(rec, http.StatusUnauthorized, err)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["session"] != "abc" {
		t.Errorf("extra member not merged: %v", body)
	}
	if body["flows"] == nil {
		t.Errorf("expected flows in extra body, got %v", body)
	}
}

func TestMatrixErrorFromHidesInternalDetails(t *testing.T) {
	t.Parallel()
	internal := fmt.Errorf("connection string user=postgres password=s3cret: %w", errors.New("dial failed"))
	got := matrixerr.From(internal, matrixerr.CodeUnknown, "The server encountered an internal error.")
	if got.Code != matrixerr.CodeUnknown {
		t.Errorf("code = %s", got.Code)
	}
	if got.Message != "The server encountered an internal error." {
		t.Errorf("message leaked internals: %q", got.Message)
	}
	// A Matrix error passes through unchanged.
	known := matrixerr.New(matrixerr.CodeUserInUse, "taken")
	if matrixerr.From(known, matrixerr.CodeUnknown, "fallback") != known {
		t.Errorf("Matrix errors must not be replaced")
	}
}

// TestRouterUnknownEndpointSemantics is the unit-level counterpart of the
// Complement TestUnknownEndpoints expectations.
func TestRouterUnknownEndpointSemantics(t *testing.T) {
	t.Parallel()

	router := httpserver.NewRouter()
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	router.Handle("GET", "/_matrix/client/v3/login", ok)
	router.Handle("POST", "/_matrix/client/v3/login", ok)
	router.Handle("GET", "/_matrix/federation/v1/version", ok)
	router.Handle("POST", "/_matrix/key/v2/query", ok)
	router.Handle("POST", "/_matrix/media/v3/upload", ok)
	router.Handle("GET", "/_matrix/client/v3/rooms/{roomID}/state", ok)

	handler := router.Handler()

	cases := []struct {
		name        string
		method      string
		path        string
		wantStatus  int
		wantErrcode string
		wantAllow   string
	}{
		{"unknown prefix", "GET", "/_matrix/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{"unknown client endpoint", "GET", "/_matrix/client/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{"unknown under v1", "GET", "/_matrix/client/v1/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{"unknown under v3", "GET", "/_matrix/client/v3/room/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{"wrong method on login", "PUT", "/_matrix/client/v3/login", http.StatusMethodNotAllowed, "M_UNRECOGNIZED", "GET, POST"},
		{"unknown federation endpoint", "GET", "/_matrix/federation/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{"unknown under federation v1", "GET", "/_matrix/federation/v1/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{"wrong method on federation version", "PUT", "/_matrix/federation/v1/version", http.StatusMethodNotAllowed, "M_UNRECOGNIZED", "GET"},
		{"unknown key endpoint", "GET", "/_matrix/key/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{"wrong method on key query", "PUT", "/_matrix/key/v2/query", http.StatusMethodNotAllowed, "M_UNRECOGNIZED", "POST"},
		{"unknown media endpoint", "PATCH", "/_matrix/media/unknown", http.StatusNotFound, "M_UNRECOGNIZED", ""},
		{"wrong method on media upload", "PATCH", "/_matrix/media/v3/upload", http.StatusMethodNotAllowed, "M_UNRECOGNIZED", "POST"},
		{"matched path variable", "GET", "/_matrix/client/v3/rooms/!room:hs/state", http.StatusOK, "", ""},
		{"HEAD follows GET", "HEAD", "/_matrix/client/v3/login", http.StatusOK, "", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantErrcode == "" {
				return
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("error body is not JSON: %v (%s)", err, rec.Body.String())
			}
			if body["errcode"] != tc.wantErrcode {
				t.Errorf("errcode = %v, want %s", body["errcode"], tc.wantErrcode)
			}
			if body["error"] == nil || body["error"] == "" {
				t.Errorf("error member missing: %v", body)
			}
			if tc.wantAllow != "" {
				if got := rec.Header().Get("Allow"); got != tc.wantAllow {
					t.Errorf("Allow = %q, want %q", got, tc.wantAllow)
				}
			}
		})
	}
}

func TestRouterRejectsDuplicateRegistration(t *testing.T) {
	t.Parallel()
	defer func() {
		if rec := recover(); rec == nil {
			t.Errorf("expected a panic for duplicate registration")
		}
	}()
	router := httpserver.NewRouter()
	router.Handle("GET", "/x", func(http.ResponseWriter, *http.Request) {})
	router.Handle("GET", "/x", func(http.ResponseWriter, *http.Request) {})
}

func TestRouterPatternsAndMethods(t *testing.T) {
	t.Parallel()
	router := httpserver.NewRouter()
	router.Handle("POST", "/_matrix/client/v3/login", func(http.ResponseWriter, *http.Request) {})
	router.Handle("GET", "/_matrix/client/v3/login", func(http.ResponseWriter, *http.Request) {})

	patterns := router.Patterns()
	if len(patterns) != 1 || patterns[0] != "/_matrix/client/v3/login" {
		t.Errorf("patterns = %v", patterns)
	}
	methods := router.AllowedMethods("/_matrix/client/v3/login")
	if len(methods) != 2 || methods[0] != "GET" || methods[1] != "POST" {
		t.Errorf("methods = %v", methods)
	}
}
