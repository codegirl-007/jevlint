package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func openAIEnv(extra map[string]string) func(string) string {
	env := map[string]string{
		"JEVLINT_PROVIDER": "openai",
		"OPENAI_API_KEY":   "sk-test",
	}
	for key, value := range extra {
		env[key] = value
	}
	return func(key string) string { return env[key] }
}

func TestOpenAIProviderDefaults(t *testing.T) {
	t.Parallel()

	client, err := NewClientFromEnv(Options{}, openAIEnv(nil))
	if err != nil {
		t.Fatalf("NewClientFromEnv() error = %v", err)
	}
	if client.endpoint != openAIDecisionsURL {
		t.Errorf("endpoint = %q, want %q", client.endpoint, openAIDecisionsURL)
	}
	if client.model != openAIModel {
		t.Errorf("model = %q, want %q", client.model, openAIModel)
	}
	if kind := client.CredentialKind(); !strings.HasPrefix(kind, "openai-key") {
		t.Errorf("credential kind = %q", kind)
	}
}

func TestOpenAIProviderAutoSelect(t *testing.T) {
	t.Parallel()

	client, err := NewClientFromEnv(Options{}, func(key string) string {
		switch key {
		case "OPENAI_API_KEY":
			return "sk-test"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatalf("NewClientFromEnv() error = %v", err)
	}
	if client.provider.Name() != "openai" {
		t.Errorf("provider = %q, want openai", client.provider.Name())
	}
}

func TestOpenAIProviderRequiresKey(t *testing.T) {
	t.Parallel()

	_, err := NewClientFromEnv(Options{}, func(key string) string {
		if key == "JEVLINT_PROVIDER" {
			return "openai"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("error = %v, want a missing-key error", err)
	}
}

func TestOpenAIProviderAnswers(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"answers": [
			{"type": "choice", "name": "r", "choice": "pass", "confidence": 1}
		]
	}`)
	answers, err := (OpenAIProvider{}).Answers(body)
	if err != nil || answers["r"].Choice != "pass" {
		t.Fatalf("Answers() = %#v, %v", answers, err)
	}
}

func TestOpenAIProviderAnswersRefusal(t *testing.T) {
	t.Parallel()

	body := []byte(`{"answers":[{"type":"refusal","name":"r"}]}`)
	_, err := (OpenAIProvider{}).Answers(body)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("error = %v, want refusal error", err)
	}
}

func TestOpenAIEvaluateUsesDecisionsShape(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/decisions" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q", got)
		}
		var payload openAIDecisionRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Model != openAIModel {
			t.Errorf("model = %q", payload.Model)
		}
		if payload.Input == "" {
			t.Error("input is empty")
		}
		if len(payload.Questions) != 2 {
			t.Fatalf("questions = %d, want 2", len(payload.Questions))
		}
		for _, question := range payload.Questions {
			if question.Type != "choice" || len(question.Choices) < 2 {
				t.Errorf("question %#v", question)
			}
		}
		fmt.Fprint(writer, `{
			"answers": [
				{"type": "choice", "name": "database-joins", "choice": "pass", "confidence": 1},
				{"type": "choice", "name": "semicolons", "choice": "pass", "confidence": 1}
			]
		}`)
	}))
	defer server.Close()

	client, err := NewClientFromEnv(Options{
		Endpoint:   server.URL + "/v1/decisions",
		HTTPClient: server.Client(),
	}, openAIEnv(nil))
	if err != nil {
		t.Fatalf("NewClientFromEnv() error = %v", err)
	}
	results, err := client.Evaluate(context.Background(), testBatch())
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if results["database-joins"].Status != StatusPass {
		t.Fatalf("results = %#v", results)
	}
}
