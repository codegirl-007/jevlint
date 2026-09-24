package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jevlint/internal/config"
	"jevlint/internal/parsing"
)

func TestTypeSafeEvaluateBatchesRules(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/systemone" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q", got)
		}

		var payload systemOneRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload.Model != "jev-test" {
			t.Errorf("model = %q", payload.Model)
		}
		if len(payload.Questions) != 2 {
			t.Errorf("questions = %#v", payload.Questions)
		}
		state, ok := payload.State.(map[string]any)
		if !ok {
			t.Errorf("state = %#v", payload.State)
		} else {
			if state["kind"] != "function" {
				t.Errorf("state kind = %#v", state["kind"])
			}
			source, _ := state["source"].(string)
			if !strings.HasPrefix(source, "// Joins users.") {
				t.Errorf("state source = %q", source)
			}
			types, ok := state["types"].([]any)
			if !ok || len(types) != 1 {
				t.Errorf("state types = %#v", state["types"])
			}
		}
		if !strings.Contains(payload.Questions["database-joins"].Instructions, "different databases") {
			t.Errorf("instructions = %q", payload.Questions["database-joins"].Instructions)
		}
		if payload.Questions["semicolons"].Criteria["fail"] == "" {
			t.Errorf("fail criterion is missing")
		}

		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{
			"model": "jev-test",
			"answers": {
				"database-joins": {"type": "choice", "choice": "fail", "confidence": 0.91},
				"semicolons": {"type": "choice", "choice": "pass", "confidence": 0.87}
			}
		}`)
	}))
	defer server.Close()

	client := newTestClient(t, server, nil)
	results, err := client.Evaluate(context.Background(), testBatch())
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if results["database-joins"].Status != StatusFail {
		t.Fatalf("database-joins = %#v", results["database-joins"])
	}
	if results["semicolons"].Status != StatusPass {
		t.Fatalf("semicolons = %#v", results["semicolons"])
	}
}

func TestTypeSafeEvaluateRejectsMalformedAnswers(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"missing answer": `{
			"answers": {
				"database-joins": {"type": "choice", "choice": "pass", "confidence": 0.9}
			}
		}`,
		"invalid choice": `{
			"answers": {
				"database-joins": {"type": "choice", "choice": "maybe", "confidence": 0.9},
				"semicolons": {"type": "choice", "choice": "pass", "confidence": 0.9}
			}
		}`,
		"missing confidence": `{
			"answers": {
				"database-joins": {"type": "choice", "choice": "pass"},
				"semicolons": {"type": "choice", "choice": "pass", "confidence": 0.9}
			}
		}`,
	}

	for name, response := range tests {
		name, response := name, response
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(writer, response)
			}))
			defer server.Close()

			client := newTestClient(t, server, nil)
			if _, err := client.Evaluate(context.Background(), testBatch()); err == nil {
				t.Fatal("Evaluate() error = nil")
			}
		})
	}
}

func TestTypeSafeEvaluateRetriesRateLimit(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			writer.Header().Set("retry-after-ms", "0")
			http.Error(writer, `{"error":"slow down"}`, http.StatusTooManyRequests)
			return
		}
		if got := request.Header.Get("X-TypeSafe-Retry-Count"); got != "1" {
			t.Errorf("retry count = %q", got)
		}
		fmt.Fprint(writer, `{
			"answers": {
				"database-joins": {"type": "choice", "choice": "pass", "confidence": 1},
				"semicolons": {"type": "choice", "choice": "pass", "confidence": 1}
			}
		}`)
	}))
	defer server.Close()

	sleepCalls := 0
	client := newTestClient(t, server, func(context.Context, time.Duration) error {
		sleepCalls++
		return nil
	})
	if _, err := client.Evaluate(context.Background(), testBatch()); err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if requests != 2 || sleepCalls != 1 {
		t.Fatalf("requests = %d, sleeps = %d", requests, sleepCalls)
	}
}

func TestTypeSafeErrorIncludesRequestID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("x-typesafe-request-id", "req_123")
		writer.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(writer, `{"error":"invalid key"}`)
	}))
	defer server.Close()

	client := newTestClient(t, server, nil)
	_, err := client.Evaluate(context.Background(), testBatch())
	if err == nil || !strings.Contains(err.Error(), "req_123") || !strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if strings.Contains(err.Error(), "sk-test") {
		t.Fatalf("Evaluate() exposed API key: %v", err)
	}
}

func TestNewTypeSafeRequiresAPIKey(t *testing.T) {
	t.Parallel()

	if _, err := NewTypeSafe(TypeSafeOptions{}); err == nil {
		t.Fatal("NewTypeSafe() error = nil")
	}
}

func newTestClient(
	t *testing.T,
	server *httptest.Server,
	sleep func(context.Context, time.Duration) error,
) *TypeSafe {
	t.Helper()

	client, err := NewTypeSafe(TypeSafeOptions{
		APIKey:     "sk-test",
		BaseURL:    server.URL,
		Model:      "jev-test",
		HTTPClient: server.Client(),
		Sleep:      sleep,
	})
	if err != nil {
		t.Fatalf("NewTypeSafe() error = %v", err)
	}
	return client
}

func testBatch() Batch {
	return Batch{
		Rules: []config.Rule{
			{
				ID:          "database-joins",
				Description: "Join records in the database.",
				Exceptions:  []string{"The records come from different databases."},
			},
			{
				ID:          "semicolons",
				Description: "Every line ends with a semicolon.",
			},
		},
		CodeUnit: parsing.CodeUnit{
			Kind:      parsing.CodeKindFunction,
			Name:      "JoinUsers",
			Language:  "go",
			Path:      "store.go",
			Source:    "// Joins users.\nfunc JoinUsers(user User) {}",
			StartLine: 3,
			EndLine:   4,
			RelatedTypes: []parsing.TypeDeclaration{{
				Name:      "User",
				Source:    "type User struct{}",
				StartLine: 1,
				EndLine:   1,
			}},
		},
	}
}
