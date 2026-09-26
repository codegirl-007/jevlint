package evaluation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	cacheKeyVersion      = "typesafe-evaluation-v1"
	answerTypeChoice     = "choice"
	httpSuccessMin       = 200
	httpSuccessLimit     = 300
	retryBackoffBase     = 500 * time.Millisecond
	retryBackoffCap      = 5 * time.Second
	retryAfterHeaderMax  = time.Minute
	criterionPass        = "The code complies with the rule, or an explicit exception applies."
	criterionFail        = "The code violates the rule, and no explicit exception applies."
	minimumBatchRules    = 1
)

type APIKey string
type ServiceURL string

type TypeSafeOptions struct {
	APIKey     APIKey
	BaseURL    ServiceURL
	HTTPClient *http.Client
	MaxRetries int
	Sleep      func(context.Context, time.Duration) error
	Cache      ResultCache
	Refresh    bool
}

type TypeSafe struct {
	apiKey      string
	baseURL     string
	model       string
	httpClient  *http.Client
	maxRetries  int
	sleep       func(context.Context, time.Duration) error
	cache       ResultCache
	refresh     bool
	inflightMu  sync.Mutex
	inflight    map[string]*evaluationCall
	cacheHits   atomic.Uint64
	cacheMisses atomic.Uint64
	cacheWrites atomic.Uint64
}

type systemOneRequest struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]question `json:"questions"`
}

type questionType int

const (
	questionTypeUnknown questionType = iota
	questionTypeChoice
)

func (kind questionType) MarshalJSON() ([]byte, error) {
	switch kind {
	case questionTypeChoice:
		return json.Marshal(answerTypeChoice)
	default:
		return nil, fmt.Errorf("unsupported question type")
	}
}

func (kind *questionType) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value != answerTypeChoice {
		return fmt.Errorf("unsupported question type %q", value)
	}
	*kind = questionTypeChoice
	return nil
}

type questionCriteria struct {
	Pass string `json:"pass"`
	Fail string `json:"fail"`
}

type question struct {
	Type         questionType     `json:"type"`
	Instructions string           `json:"instructions"`
	Criteria     questionCriteria `json:"criteria"`
}

type systemOneResponse struct {
	Answers map[string]choiceAnswer `json:"answers"`
}

type choiceAnswer struct {
	Type       questionType `json:"type"`
	Choice     string       `json:"choice"`
	Confidence *float64     `json:"confidence"`
}

type evaluationCall struct {
	done    chan struct{}
	results map[string]Result
	err     error
}

func NewTypeSafeFromEnvWithOptions(
	options TypeSafeOptions,
	getenv func(string) string,
) (*TypeSafe, error) {
	if getenv != nil {
		if options.APIKey == "" {
			options.APIKey = APIKey(getenv("TYPESAFE_API_KEY"))
		}
		if options.BaseURL == "" {
			options.BaseURL = ServiceURL(getenv("TYPESAFE_BASE_URL"))
		}
	}
	client, err := NewTypeSafe(options)
	if err != nil {
		return nil, err
	}
	if getenv != nil {
		if model := strings.TrimSpace(getenv("TYPESAFE_DEFAULT_MODEL")); model != "" {
			client.model = model
		}
	}
	return client, nil
}

func NewTypeSafe(options TypeSafeOptions) (*TypeSafe, error) {
	apiKey := strings.TrimSpace(string(options.APIKey))
	if apiKey == "" {
		return nil, errors.New("TYPESAFE_API_KEY is required")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(string(options.BaseURL)), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return nil, fmt.Errorf("invalid TypeSafe base URL %q", baseURL)
	}

	model := defaultModel

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
		cache:      options.Cache,
		refresh:    options.Refresh,
		inflight:   make(map[string]*evaluationCall),
	}, nil
}

func (client *TypeSafe) Evaluate(ctx context.Context, batch Batch) (map[string]Result, error) {
	body, err := client.requestBody(batch)
	if err != nil {
		return nil, err
	}
	if client.cache == nil {
		responseBody, err := client.perform(ctx, body)
		if err != nil {
			return nil, err
		}
		return decodeResults(responseBody, batch.Rules)
	}

	key := client.cacheKey(body)
	if results, ok := client.hitCache(key, batch.Rules); ok {
		return results, nil
	}
	return client.evaluateOnce(ctx, key, func() (map[string]Result, error) {
		if results, ok := client.hitCache(key, batch.Rules); ok {
			return results, nil
		}
		client.cacheMisses.Add(1)
		responseBody, err := client.perform(ctx, body)
		if err != nil {
			return nil, err
		}
		results, err := decodeResults(responseBody, batch.Rules)
		if err != nil {
			return nil, err
		}
		if client.cache.Put(key, results) {
			client.cacheWrites.Add(1)
		}
		return results, nil
	})
}

func (client *TypeSafe) hitCache(key string, rules []config.Rule) (map[string]Result, bool) {
	if client.refresh {
		return nil, false
	}
	results, ok := client.cachedResults(key, rules)
	if !ok {
		return nil, false
	}
	client.cacheHits.Add(1)
	return results, true
}

func (client *TypeSafe) cachedResults(
	key string,
	rules []config.Rule,
) (map[string]Result, bool) {
	results, ok := client.cache.Get(key)
	if !ok || len(results) != len(rules) {
		return nil, false
	}
	for _, rule := range rules {
		result, exists := results[rule.ID]
		if !exists || result.Validate() != nil {
			return nil, false
		}
	}
	return results, true
}

func (client *TypeSafe) evaluateOnce(
	ctx context.Context,
	key string,
	evaluate func() (map[string]Result, error),
) (map[string]Result, error) {
	client.inflightMu.Lock()
	if call, ok := client.inflight[key]; ok {
		client.inflightMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-call.done:
			return cloneResults(call.results), call.err
		}
	}
	call := &evaluationCall{done: make(chan struct{})}
	client.inflight[key] = call
	client.inflightMu.Unlock()

	call.results, call.err = evaluate()

	client.inflightMu.Lock()
	delete(client.inflight, key)
	close(call.done)
	client.inflightMu.Unlock()
	return cloneResults(call.results), call.err
}

func (client *TypeSafe) CacheStats() CacheStats {
	return CacheStats{
		Hits:   client.cacheHits.Load(),
		Misses: client.cacheMisses.Load(),
		Writes: client.cacheWrites.Load(),
	}
}

func (client *TypeSafe) requestBody(batch Batch) ([]byte, error) {
	questions, err := questionsForBatch(batch)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(systemOneRequest{
		Model:     client.model,
		State:     batch.CodeUnit,
		Questions: questions,
	})
	if err != nil {
		return nil, fmt.Errorf("encode TypeSafe request: %w", err)
	}
	return body, nil
}

func questionsForBatch(batch Batch) (map[string]question, error) {
	if len(batch.Rules) < minimumBatchRules {
		return nil, errors.New("at least one rule is required")
	}
	questions := make(map[string]question, len(batch.Rules))
	for _, rule := range batch.Rules {
		if _, exists := questions[rule.ID]; exists {
			return nil, fmt.Errorf("duplicate rule id %q in evaluation batch", rule.ID)
		}
		questions[rule.ID] = question{
			Type: questionTypeChoice,
			Instructions: instructionsFor(rule, batch.CodeUnit),
			Criteria: questionCriteria{
				Pass: criterionPass,
				Fail: criterionFail,
			},
		}
	}
	return questions, nil
}

func decodeResults(
	responseBody []byte,
	rules []config.Rule,
) (map[string]Result, error) {
	var response systemOneResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, fmt.Errorf("decode TypeSafe response: %w", err)
	}
	if response.Answers == nil {
		return nil, errors.New("decode TypeSafe response: answers are missing")
	}

	results := make(map[string]Result, len(rules))
	for _, rule := range rules {
		answer, ok := response.Answers[rule.ID]
		if !ok {
			return nil, fmt.Errorf("decode TypeSafe response: answer for rule %q is missing", rule.ID)
		}
		if answer.Type != questionTypeChoice {
			return nil, fmt.Errorf(
				"decode TypeSafe response: answer for rule %q has unsupported type",
				rule.ID,
			)
		}
		if answer.Confidence == nil {
			return nil, fmt.Errorf(
				"decode TypeSafe response: confidence for rule %q is missing",
				rule.ID,
			)
		}

		status, err := ParseStatus(answer.Choice)
		if err != nil {
			return nil, fmt.Errorf("decode TypeSafe response: rule %q: %w", rule.ID, err)
		}
		result := Result{
			Status:     status,
			Confidence: *answer.Confidence,
		}
		if err := result.Validate(); err != nil {
			return nil, fmt.Errorf("decode TypeSafe response: rule %q: %w", rule.ID, err)
		}
		results[rule.ID] = result
	}
	return results, nil
}

func (client *TypeSafe) cacheKey(body []byte) string {
	credential := sha256.Sum256([]byte(client.apiKey))
	hash := sha256.New()
	for _, part := range [][]byte{
		[]byte(cacheKeyVersion),
		[]byte(client.baseURL),
		[]byte(client.model),
		[]byte(fmt.Sprintf("%x", credential)),
		body,
	} {
		hash.Write(part)
		hash.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func (client *TypeSafe) perform(ctx context.Context, body []byte) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		request, err := client.newRequest(ctx, body, attempt)
		if err != nil {
			return nil, err
		}
		response, err := client.httpClient.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt >= client.maxRetries {
				return nil, fmt.Errorf("TypeSafe request failed: %w", err)
			}
			if sleepErr := client.sleep(ctx, retryDelay(attempt, nil)); sleepErr != nil {
				return nil, sleepErr
			}
			continue
		}
		responseBody, err := readResponse(response)
		if err != nil {
			return nil, err
		}
		if response.StatusCode >= httpSuccessMin && response.StatusCode < httpSuccessLimit {
			return responseBody, nil
		}
		if attempt >= client.maxRetries || !retryableStatus(response.StatusCode) {
			return nil, responseError(response.StatusCode, response.Header, responseBody)
		}
		if sleepErr := client.sleep(ctx, retryDelay(attempt, response.Header)); sleepErr != nil {
			return nil, sleepErr
		}
	}
}

func (client *TypeSafe) newRequest(
	ctx context.Context,
	body []byte,
	attempt int,
) (*http.Request, error) {
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
	return request, nil
}

func readResponse(response *http.Response) ([]byte, error) {
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read TypeSafe response: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close TypeSafe response: %w", closeErr)
	}
	return body, nil
}

func instructionsFor(
	rule config.Rule,
	unit parsing.CodeUnit,
) string {
	var builder strings.Builder
	if unit.ParentSource != "" {
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
				if delay <= retryAfterHeaderMax {
					return delay
				}
			}
		}
		if raw := headers.Get("Retry-After"); raw != "" {
			if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds >= 0 {
				delay := time.Duration(seconds * float64(time.Second))
				if delay <= retryAfterHeaderMax {
					return delay
				}
			}
		}
	}

	delay := retryBackoffBase * time.Duration(1<<attempt)
	if delay > retryBackoffCap {
		return retryBackoffCap
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
