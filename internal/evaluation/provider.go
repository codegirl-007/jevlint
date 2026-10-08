package evaluation

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Provider describes one SystemOne-compatible decision backend.
type Provider interface {
	// Name identifies the provider for diagnostics.
	Name() string
	// BaseURL is the default service base when Options.BaseURL is empty.
	BaseURL() string
	// DefaultModel is the model used when Options.Model is empty.
	DefaultModel() string
	// Configure fills the options from the environment and validates them.
	Configure(options *Options, getenv func(string) string) error
	// ValidateQuestions checks a batch against the provider's limits.
	ValidateQuestions(questions map[string]question) error
	// MaxBodyBytes caps the request body size, or 0 for no explicit cap.
	MaxBodyBytes() int
	// Answers extracts the answers from a response body.
	Answers(body []byte) (map[string]choiceAnswer, error)
	// ResponseError turns a failed response into an error.
	ResponseError(status int, headers http.Header, body []byte) error
	// DescribeCredential names the credential without revealing it.
	DescribeCredential(key string) string
	// Headers returns extra request headers for this provider, or nil.
	Headers() map[string]string
}

// NewClientFromEnv selects a provider from the environment and builds a client.
//
// The default provider talks to a Jev/SystemOne service (TypeSafe by default)
// using TYPESAFE_API_KEY, TYPESAFE_BASE_URL, and TYPESAFE_DEFAULT_MODEL. Set
// JEVLINT_PROVIDER=cloudflare to talk to the Clef models hosted on Cloudflare
// Workers AI using CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_AUTH_TOKEN (or
// CLOUDFLARE_API_TOKEN), with CLEF_MODEL selecting "clef" or "clef-flash".
// JEVLINT_PROVIDER=openai uses OPENAI_API_KEY and POST /v1/decisions.
// TYPESAFE_ENDPOINT overrides the request URL for any provider.
func NewClientFromEnv(options Options, getenv func(string) string) (*Client, error) {
	if getenv == nil {
		return NewClientWithProvider(TypeSafeProvider{}, options)
	}
	name := strings.ToLower(strings.TrimSpace(getenv("JEVLINT_PROVIDER")))
	if name == "" &&
		cleanCredential(getenv("OPENAI_API_KEY")) != "" &&
		strings.TrimSpace(getenv("TYPESAFE_API_KEY")) == "" {
		name = "openai"
	}
	provider, err := providerByName(name)
	if err != nil {
		return nil, err
	}
	if err := provider.Configure(&options, getenv); err != nil {
		return nil, err
	}
	client, err := NewClientWithProvider(provider, options)
	if err != nil {
		return nil, err
	}
	if name == "" {
		if cloudflareVariablesSet(getenv) {
			client.warn(
				"jevlint: Cloudflare variables are set but JEVLINT_PROVIDER " +
					"is not cloudflare; using the default provider",
			)
		}
		if openRouterVariablesSet(getenv) {
			client.warn(
				"jevlint: OPENROUTER_* variables are set but JEVLINT_PROVIDER " +
					"is not openrouter; using the default provider",
			)
		}
	}
	if client.logf != nil {
		client.debugf(
			"jevlint: using provider=%s endpoint=%s model=%s credential=%s",
			provider.Name(),
			client.endpoint,
			client.model,
			provider.DescribeCredential(client.apiKey),
		)
	}
	return client, nil
}

// providerByName maps a JEVLINT_PROVIDER value to a provider.
func providerByName(name string) (Provider, error) {
	switch name {
	case "", "typesafe", "jev":
		return TypeSafeProvider{}, nil
	case "cloudflare", "clef":
		return CloudflareProvider{}, nil
	case "openrouter":
		return &OpenRouterProvider{}, nil
	case "openai":
		return OpenAIProvider{}, nil
	default:
		return nil, fmt.Errorf("unknown JEVLINT_PROVIDER %q", name)
	}
}

// firstNonEmpty returns the first non-empty value, or "".
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// cleanCredential trims whitespace and stray quote characters that a shell or
// clipboard can leave around a value.
func cleanCredential(value string) string {
	return strings.Trim(strings.TrimSpace(value), `"'`)
}

// serviceErrorBody covers the error shapes of the services we talk to:
// Jev's {"error": "..."}, Cloudflare's {"errors": [{"code", "message"}]}, and
// OpenRouter's {"error": {"message", "code"}}.
type serviceErrorBody struct {
	Error  json.RawMessage `json:"error"`
	Errors []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// parseServiceError extracts a message and optional code from an error body.
func parseServiceError(body []byte) (string, int) {
	var payload serviceErrorBody
	if json.Unmarshal(body, &payload) == nil {
		if message, code, ok := parseErrorField(payload.Error); ok {
			return message, code
		}
		if len(payload.Errors) > 0 {
			return payload.Errors[0].Message, payload.Errors[0].Code
		}
	}
	return strings.TrimSpace(string(body)), 0
}

// parseErrorField reads an `error` value that is either a string or an object
// with `message` and `code`.
func parseErrorField(raw json.RawMessage) (string, int, bool) {
	if len(raw) == 0 {
		return "", 0, false
	}
	var message string
	if json.Unmarshal(raw, &message) == nil && message != "" {
		return message, 0, true
	}
	var object struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	}
	if json.Unmarshal(raw, &object) == nil && (object.Message != "" || object.Code != 0) {
		return object.Message, object.Code, true
	}
	return "", 0, false
}

// serviceError builds the shared "decision service returned" error.
func serviceError(status int, requestID string, message string) error {
	if message == "" {
		message = http.StatusText(status)
	}
	if requestID != "" {
		return fmt.Errorf(
			"decision service returned %d: %s (request %s)",
			status,
			message,
			requestID,
		)
	}
	return fmt.Errorf("decision service returned %d: %s", status, message)
}
