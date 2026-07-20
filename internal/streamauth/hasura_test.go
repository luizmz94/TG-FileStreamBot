package streamauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHasuraAccessCheckerUserCanStream(t *testing.T) {
	tests := []struct {
		name     string
		response string
		allowed  bool
	}{
		{
			name:     "active user",
			response: `{"data":{"users":[{"firebase_uid":"firebase-user-1","is_blocked":false,"is_deleted":false}]}}`,
			allowed:  true,
		},
		{
			name:     "blocked user",
			response: `{"data":{"users":[{"firebase_uid":"firebase-user-1","is_blocked":true,"is_deleted":false}]}}`,
		},
		{
			name:     "deleted user",
			response: `{"data":{"users":[{"firebase_uid":"firebase-user-1","is_blocked":false,"is_deleted":true}]}}`,
		},
		{
			name:     "missing user",
			response: `{"data":{"users":[]}}`,
		},
		{
			name:     "unexpected user",
			response: `{"data":{"users":[{"firebase_uid":"another-user","is_blocked":false,"is_deleted":false}]}}`,
		},
		{
			name:     "null status",
			response: `{"data":{"users":[{"firebase_uid":"firebase-user-1","is_blocked":null,"is_deleted":false}]}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("method = %s, want POST", r.Method)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer firebase-token" {
					t.Errorf("Authorization = %q", got)
				}
				if got := r.Header.Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q", got)
				}

				var request hasuraGraphQLRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode request: %v", err)
				}
				if got := request.Variables["firebaseUid"]; got != "firebase-user-1" {
					t.Errorf("firebaseUid = %v", got)
				}
				if !strings.Contains(request.Query, "is_blocked") || !strings.Contains(request.Query, "is_deleted") {
					t.Errorf("query does not request user status: %s", request.Query)
				}

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			checker, err := newHasuraAccessChecker(server.URL)
			if err != nil {
				t.Fatalf("new checker: %v", err)
			}
			allowed, err := checker.UserCanStream(t.Context(), "firebase-token", "firebase-user-1")
			if err != nil {
				t.Fatalf("UserCanStream: %v", err)
			}
			if allowed != tt.allowed {
				t.Errorf("allowed = %v, want %v", allowed, tt.allowed)
			}
		})
	}
}

func TestHasuraAccessCheckerFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   string
	}{
		{name: "HTTP error", statusCode: http.StatusInternalServerError, response: `server error`},
		{name: "GraphQL error", statusCode: http.StatusOK, response: `{"errors":[{"message":"denied"}],"data":{"users":[]}}`},
		{name: "malformed JSON", statusCode: http.StatusOK, response: `{`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			checker, err := newHasuraAccessChecker(server.URL)
			if err != nil {
				t.Fatalf("new checker: %v", err)
			}
			allowed, err := checker.UserCanStream(t.Context(), "firebase-token", "firebase-user-1")
			if err == nil {
				t.Fatal("expected an error")
			}
			if allowed {
				t.Fatal("access was allowed on validation error")
			}
		})
	}
}

func TestNewHasuraAccessCheckerRejectsInvalidEndpoint(t *testing.T) {
	for _, endpoint := range []string{"", "hasura.example/v1/graphql", "ftp://hasura.example/v1/graphql"} {
		if _, err := newHasuraAccessChecker(endpoint); err == nil {
			t.Errorf("endpoint %q was accepted", endpoint)
		}
	}
}
