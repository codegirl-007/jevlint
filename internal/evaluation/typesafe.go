package evaluation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"jevlint/internal/config"
	"jevlint/internal/parsing"
)

const (
	defaultBaseURL    = "https://api.typesafe.ai"
	defaultModel      = "jev-latest"
	defaultTimeout    = 10 * time.Second
	defaultMaxRetries = 2
	maxResponseBytes  = 1 << 20
)

type TypeSafeOptions struct {
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client
	MaxRetries int
	Sleep      func(context.Context, time.Duration) error
}

type TypeSafe struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
	maxRetries int
	sleep      func(context.Context, time.Duration) error
}

type systemOneRequest struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]question `json:"questions"`
}

type question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type systemOneResponse struct {
	Answers map[string]choiceAnswer `json:"answers"`
}

type choiceAnswer struct {
	Type       string   `json:"type"`
	Choice     string   `json:"choice"`
	Confidence *float64 `json:"confidence"`
}

func NewTypeSafeFromEnv() (*TypeSafe, error) {
	return NewTypeSafe(TypeSafeOptions{
		APIKey:  os.Getenv("TYPESAFE_API_KEY"),
		BaseURL: os.Getenv("TYPESAFE_BASE_URL"),
		Model:   os.Getenv("TYPESAFE_DEFAULT_MODEL"),
	})
}

func NewTypeSafe(options TypeSafeOptions) (*TypeSafe, error) {
	apiKey := strings.TrimSpace(options.APIKey)
	if apiKey == "" {
		return nil, errors.New("TYPESAFE_API_KEY is required")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(options.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return nil, fmt.Errorf("invalid TypeSafe base URL %q", baseURL)
	}

	model := strings.TrimSpace(options.Model)
	if model == "" {
		model = defaultModel
	}

	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}

	maxRetries := options.MaxRetries
	if maxRetries == 0 {
		maxRetries = defaultMaxRetries
	}
	if maxRetries < 0 {
		return nil, errors.New("maximum retries cannot be negative")
	}

	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	return &TypeSafe{
		apiKey:     apiKey,
		baseURL:    baseURL,
		model:      model,
		httpClient: httpClient,
		maxRetries: maxRetries,
		sleep:      sleep,
	}, nil
}

func (client *TypeSafe) Evaluate(ctx context.Context, batch Batch) (map[string]Result, error) {
	if len(batch.Rules) == 0 {
		return nil, errors.New("at least one rule is required")
	}

	questions := make(map[string]question, len(batch.Rules))
	for _, rule := range batch.Rules {
		if _, exists := questions[rule.ID]; exists {
			return nil, fmt.Errorf("duplicate rule id %q in evaluation batch", rule.ID)
		}
		questions[rule.ID] = question{
			Type: "choice",
			Instructions: instructionsFor(
				rule,
				batch.CodeUnit.Kind,
			),
			Criteria: map[string]string{
				"pass": "The code complies with the rule, or an explicit exception applies.",
				"fail": "The code violates the rule, and no explicit exception applies.",
			},
		}
	}

	body, err := json.Marshal(systemOneRequest{
		Model:     client.model,
		State:     batch.CodeUnit,
		Questions: questions,
	})
	if err != nil {
		return nil, fmt.Errorf("encode TypeSafe request: %w", err)
	}

	responseBody, err := client.perform(ctx, body)
	if err != nil {
		return nil, err
	}

	var response systemOneResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, fmt.Errorf("decode TypeSafe response: %w", err)
	}
	if response.Answers == nil {
		return nil, errors.New("decode TypeSafe response: answers are missing")
	}

	results := make(map[string]Result, len(batch.Rules))
	for _, rule := range batch.Rules {
		answer, ok := response.Answers[rule.ID]
		if !ok {
			return nil, fmt.Errorf("decode TypeSafe response: answer for rule %q is missing", rule.ID)
		}
		if answer.Type != "choice" {
			return nil, fmt.Errorf(
				"decode TypeSafe response: answer for rule %q has type %q, want choice",
				rule.ID,
				answer.Type,
			)
		}
		if answer.Confidence == nil {
			return nil, fmt.Errorf(
				"decode TypeSafe response: confidence for rule %q is missing",
				rule.ID,
			)
		}

		result := Result{
			Status:     Status(answer.Choice),
			Confidence: *answer.Confidence,
		}
		if err := result.Validate(); err != nil {
			return nil, fmt.Errorf("decode TypeSafe response: rule %q: %w", rule.ID, err)
		}
		results[rule.ID] = result
	}
	return results, nil
}

func (client *TypeSafe) perform(ctx context.Context, body []byte) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			client.baseURL+"/v1/systemone",
			bytes.NewReader(body),
		)
		if err != nil {
			return nil, fmt.Errorf("create TypeSafe request: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+client.apiKey)
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", "jevlint/0.1.0")
		request.Header.Set("X-TypeSafe-SDK", "jevlint/0.1.0")
		request.Header.Set("X-TypeSafe-Runtime", runtime.Version())
		if attempt > 0 {
			request.Header.Set("X-TypeSafe-Retry-Count", strconv.Itoa(attempt))
		}

		response, requestErr := client.httpClient.Do(request)
		if requestErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt >= client.maxRetries {
				return nil, fmt.Errorf("TypeSafe request failed: %w", requestErr)
			}
			if err := client.sleep(ctx, retryDelay(attempt, nil)); err != nil {
				return nil, err
			}
			continue
		}

		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
		closeErr := response.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read TypeSafe response: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close TypeSafe response: %w", closeErr)
		}

		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return responseBody, nil
		}
		if retryableStatus(response.StatusCode) && attempt < client.maxRetries {
			if err := client.sleep(ctx, retryDelay(attempt, response.Header)); err != nil {
				return nil, err
			}
			continue
		}
		return nil, responseError(response.StatusCode, response.Header, responseBody)
	}
}

func instructionsFor(
	rule config.Rule,
	kind parsing.CodeKind,
) string {
	var builder strings.Builder
	if kind == parsing.CodeKindRegion {
		builder.WriteString(
			"Determine whether state.source violates this rule. " +
				"Use state.parentSource only as surrounding context:\n",
		)
	} else {
		builder.WriteString("Determine whether the supplied code complies with this rule:\n")
	}
	builder.WriteString(rule.Description)
	if len(rule.Exceptions) > 0 {
		builder.WriteString("\n\nExplicit exceptions:")
		for _, exception := range rule.Exceptions {
			builder.WriteString("\n- ")
			builder.WriteString(exception)
		}
	}
	return builder.String()
}

func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusTooManyRequests ||
		status >= 500
}

func retryDelay(attempt int, headers http.Header) time.Duration {
	if headers != nil {
		if raw := headers.Get("retry-after-ms"); raw != "" {
			if milliseconds, err := strconv.ParseFloat(raw, 64); err == nil && milliseconds >= 0 {
				delay := time.Duration(milliseconds * float64(time.Millisecond))
				if delay <= time.Minute {
					return delay
				}
			}
		}
		if raw := headers.Get("Retry-After"); raw != "" {
			if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds >= 0 {
				delay := time.Duration(seconds * float64(time.Second))
				if delay <= time.Minute {
					return delay
				}
			}
		}
	}

	delay := 500 * time.Millisecond * time.Duration(1<<attempt)
	if delay > 5*time.Second {
		return 5 * time.Second
	}
	return delay
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func responseError(status int, headers http.Header, body []byte) error {
	message := strings.TrimSpace(string(body))
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error != "" {
		message = payload.Error
	}
	if message == "" {
		message = http.StatusText(status)
	}

	requestID := headers.Get("x-typesafe-request-id")
	if requestID != "" {
		return fmt.Errorf("TypeSafe API returned %d: %s (request %s)", status, message, requestID)
	}
	return fmt.Errorf("TypeSafe API returned %d: %s", status, message)
}
