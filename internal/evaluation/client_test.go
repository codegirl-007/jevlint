package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codegirl-007/jevlint/internal/config"
	"github.com/codegirl-007/jevlint/internal/evidence"
	"github.com/codegirl-007/jevlint/internal/parsing"
)

type unavailableCache struct{}

func (unavailableCache) Get(string) (map[string]Result, bool) {
	return nil, false
}

func (unavailableCache) Put(string, map[string]Result) bool {
	return false
}

func (unavailableCache) Clear() error {
	return nil
}

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
			evidenceItems, ok := state["evidence"].([]any)
			if !ok || len(evidenceItems) != 2 {
				t.Errorf("state evidence = %#v", state["evidence"])
			}
			for _, key := range []string{
				"startLine", "endLine", "startColumn", "endColumn", "startByte", "endByte",
			} {
				if _, exists := state[key]; exists {
					t.Errorf("state includes %s = %#v", key, state[key])
				}
			}
		}
		if !strings.Contains(payload.Questions["database-joins"].Instructions, "different databases") {
			t.Errorf("instructions = %q", payload.Questions["database-joins"].Instructions)
		}
		if payload.Questions["semicolons"].Criteria.Fail == "" {
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

func TestTypeSafeEvaluateExplainsRegionContext(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload systemOneRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		state, _ := payload.State.(map[string]any)
		if state["source"] != "Enabled bool" {
			t.Errorf("region source = %#v", state["source"])
		}
		if !strings.Contains(fmt.Sprint(state["parentSource"]), "type FeatureFlags") {
			t.Errorf("parent source = %#v", state["parentSource"])
		}
		instructions := payload.Questions["database-joins"].Instructions
		if !strings.Contains(instructions, "state.source") ||
			!strings.Contains(instructions, "state.parentSource") {
			t.Errorf("instructions = %q", instructions)
		}
		fmt.Fprint(writer, `{
			"answers": {
				"database-joins": {"type": "choice", "choice": "fail", "confidence": 1}
			}
		}`)
	}))
	defer server.Close()

	client := newTestClient(t, server, nil)
	batch := testBatch()
	batch.Rules = batch.Rules[:1]
	batch.CodeUnit = parsing.CodeUnit{
		Kind:         parsing.CodeKindField,
		Name:         "FeatureFlags:field_declaration",
		Language:     parsing.SourceLanguageGo,
		Path:         "flags.go",
		Source:       "Enabled bool",
		ParentSource: "type FeatureFlags struct {\n\tEnabled bool\n}",
		RegionKind:   "field_declaration",
		StartLine:    4,
		EndLine:      4,
	}
	results, err := client.Evaluate(context.Background(), batch)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if results["database-joins"].Status != StatusFail {
		t.Fatalf("result = %#v", results["database-joins"])
	}
}

func TestTypeSafeCacheKeyTracksExactEvaluationInput(t *testing.T) {
	t.Parallel()

	client, err := NewClient(Options{
		APIKey:  "sk-one",
		BaseURL: "https://one.example",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	client.model = "jev-one"
	body, err := client.requestBody(testBatch())
	if err != nil {
		t.Fatalf("requestBody() error = %v", err)
	}
	key := client.cacheKey(body)
	if len(key) != sha256.Size*2 {
		t.Fatalf("cacheKey() length = %d, want %d", len(key), sha256.Size*2)
	}

	sameBody, err := client.requestBody(testBatch())
	if err != nil {
		t.Fatalf("requestBody() error = %v", err)
	}
	if sameKey := client.cacheKey(sameBody); sameKey != key {
		t.Fatalf("identical cache key = %q, want %q", sameKey, key)
	}

	severityOnly := testBatch()
	severityOnly.Rules[0].Severity = config.SeverityError
	severityBody, err := client.requestBody(severityOnly)
	if err != nil {
		t.Fatalf("requestBody() error = %v", err)
	}
	if severityKey := client.cacheKey(severityBody); severityKey != key {
		t.Fatalf("severity cache key = %q, want %q", severityKey, key)
	}

	changed := testBatch()
	changed.CodeUnit.Source += "\n"
	changedBody, err := client.requestBody(changed)
	if err != nil {
		t.Fatalf("requestBody() error = %v", err)
	}
	if changedKey := client.cacheKey(changedBody); changedKey == key {
		t.Fatal("source change did not change cache key")
	}

	locationOnly := testBatch()
	locationOnly.CodeUnit.StartLine++
	locationOnly.CodeUnit.EndLine++
	locationOnly.CodeUnit.StartColumn++
	locationOnly.CodeUnit.StartByte++
	locationBody, err := client.requestBody(locationOnly)
	if err != nil {
		t.Fatalf("requestBody() error = %v", err)
	}
	if locationKey := client.cacheKey(locationBody); locationKey != key {
		t.Fatal("span-only change changed cache key")
	}

	mutations := map[string]func(*Batch){
		"path": func(batch *Batch) {
			batch.CodeUnit.Path = "other.go"
		},
		"related type": func(batch *Batch) {
			batch.Evidence[0].Source = "type User struct{ ID int }"
		},
		"rule description": func(batch *Batch) {
			batch.Rules[0].Description = "A different rule."
		},
		"rule exception": func(batch *Batch) {
			batch.Rules[0].Exceptions = []string{"A different exception."}
		},
		"localization state": func(batch *Batch) {
			batch.CodeUnit.Kind = parsing.CodeKindRegion
			batch.CodeUnit.ParentSource = "different parent"
		},
		"batch membership": func(batch *Batch) {
			batch.Rules = batch.Rules[:1]
		},
		"allowSkip": func(batch *Batch) {
			batch.Rules[0].AllowSkip = true
		},
		"allowAbstain": func(batch *Batch) {
			batch.Rules[0].AllowAbstain = true
		},
		"callee context": func(batch *Batch) {
			batch.Evidence = append(batch.Evidence, evidence.Evidence{
				Kind:      evidence.KindCallee,
				Path:      "users.go",
				StartLine: 20,
				EndLine:   20,
				Symbol:    "loadUsers",
				Source:    "func loadUsers() {}",
			})
		},
	}
	for name, mutate := range mutations {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			changed := testBatch()
			mutate(&changed)
			changedBody, err := client.requestBody(changed)
			if err != nil {
				t.Fatalf("requestBody() error = %v", err)
			}
			if changedKey := client.cacheKey(changedBody); changedKey == key {
				t.Fatalf("%s change did not change cache key", name)
			}
		})
	}

	for name, test := range map[string]struct {
		options Options
		model   string
	}{
		"endpoint": {
			options: Options{APIKey: "sk-one", BaseURL: "https://two.example"},
			model:   "jev-one",
		},
		"model": {
			options: Options{APIKey: "sk-one", BaseURL: "https://one.example"},
			model:   "jev-two",
		},
		"credential": {
			options: Options{APIKey: "sk-two", BaseURL: "https://one.example"},
			model:   "jev-one",
		},
	} {
		name, test := name, test
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			other, err := NewClient(test.options)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			other.model = test.model
			if otherKey := other.cacheKey(body); otherKey == key {
				t.Fatalf("%s change did not change cache key", name)
			}
		})
	}
}

func TestTypeSafeEvaluateCachesValidatedResults(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(writer, `{
			"answers": {
				"database-joins": {"type": "choice", "choice": "fail", "confidence": 0.9},
				"semicolons": {"type": "choice", "choice": "pass", "confidence": 0.8}
			}
		}`)
	}))
	defer server.Close()

	cache, err := newFileCacheAt(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatalf("newFileCacheAt() error = %v", err)
	}
	client := newCachedTestClient(t, server, cache, false)
	for range 2 {
		results, err := client.Evaluate(context.Background(), testBatch())
		if err != nil {
			t.Fatalf("Evaluate() error = %v", err)
		}
		if results["database-joins"].Status != StatusFail {
			t.Fatalf("Evaluate() results = %#v", results)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP requests = %d, want 1", got)
	}
	if stats := client.CacheStats(); stats != (CacheStats{
		Hits:   1,
		Misses: 1,
		Writes: 1,
	}) {
		t.Fatalf("CacheStats() = %#v", stats)
	}
}

func TestTypeSafeEvaluateBypassesUnavailableOrDisabledCache(t *testing.T) {
	t.Parallel()

	tests := map[string]ResultCache{
		"disabled":    nil,
		"unavailable": unavailableCache{},
	}
	for name, cache := range tests {
		name, cache := name, cache
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(
				func(writer http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					fmt.Fprint(writer, `{
						"answers": {
							"database-joins": {
								"type": "choice",
								"choice": "pass",
								"confidence": 1
							},
							"semicolons": {
								"type": "choice",
								"choice": "pass",
								"confidence": 1
							}
						}
					}`)
				},
			))
			defer server.Close()

			client := newCachedTestClient(t, server, cache, false)
			for range 2 {
				if _, err := client.Evaluate(context.Background(), testBatch()); err != nil {
					t.Fatalf("Evaluate() error = %v", err)
				}
			}
			if got := requests.Load(); got != 2 {
				t.Fatalf("HTTP requests = %d, want 2", got)
			}
			if cache == nil && client.CacheStats() != (CacheStats{}) {
				t.Fatalf("CacheStats() = %#v", client.CacheStats())
			}
		})
	}
}

func TestTypeSafeEvaluateDeduplicatesConcurrentMisses(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
		}
		<-release
		fmt.Fprint(writer, `{
			"answers": {
				"database-joins": {"type": "choice", "choice": "pass", "confidence": 1},
				"semicolons": {"type": "choice", "choice": "pass", "confidence": 1}
			}
		}`)
	}))
	defer server.Close()

	cache, err := newFileCacheAt(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatalf("newFileCacheAt() error = %v", err)
	}
	client := newCachedTestClient(t, server, cache, false)

	const callers = 8
	errors := make(chan error, callers)
	var workers sync.WaitGroup
	workers.Add(callers)
	for range callers {
		go func() {
			defer workers.Done()
			_, err := client.Evaluate(context.Background(), testBatch())
			errors <- err
		}()
	}
	<-started
	close(release)
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("Evaluate() error = %v", err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP requests = %d, want 1", got)
	}
}

func TestTypeSafeEvaluateRefreshesCachedResult(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		status := "pass"
		if requests.Add(1) == 2 {
			status = "fail"
		}
		fmt.Fprintf(writer, `{
			"answers": {
				"database-joins": {"type": "choice", "choice": %q, "confidence": 1},
				"semicolons": {"type": "choice", "choice": "pass", "confidence": 1}
			}
		}`, status)
	}))
	defer server.Close()

	cache, err := newFileCacheAt(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatalf("newFileCacheAt() error = %v", err)
	}
	initial := newCachedTestClient(t, server, cache, false)
	if _, err := initial.Evaluate(context.Background(), testBatch()); err != nil {
		t.Fatalf("initial Evaluate() error = %v", err)
	}

	refresh := newCachedTestClient(t, server, cache, true)
	results, err := refresh.Evaluate(context.Background(), testBatch())
	if err != nil {
		t.Fatalf("refresh Evaluate() error = %v", err)
	}
	if results["database-joins"].Status != StatusFail {
		t.Fatalf("refresh results = %#v", results)
	}

	cached := newCachedTestClient(t, server, cache, false)
	results, err = cached.Evaluate(context.Background(), testBatch())
	if err != nil {
		t.Fatalf("cached Evaluate() error = %v", err)
	}
	if results["database-joins"].Status != StatusFail {
		t.Fatalf("cached results = %#v", results)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("HTTP requests = %d, want 2", got)
	}
}

func TestTypeSafeEvaluateDoesNotCacheMalformedResponse(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			fmt.Fprint(writer, `{"answers": {}}`)
			return
		}
		fmt.Fprint(writer, `{
			"answers": {
				"database-joins": {"type": "choice", "choice": "pass", "confidence": 1},
				"semicolons": {"type": "choice", "choice": "pass", "confidence": 1}
			}
		}`)
	}))
	defer server.Close()

	cache, err := newFileCacheAt(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatalf("newFileCacheAt() error = %v", err)
	}
	client := newCachedTestClient(t, server, cache, false)
	if _, err := client.Evaluate(context.Background(), testBatch()); err == nil {
		t.Fatal("first Evaluate() error = nil")
	}
	if _, err := client.Evaluate(context.Background(), testBatch()); err != nil {
		t.Fatalf("second Evaluate() error = %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("HTTP requests = %d, want 2", got)
	}
}

func TestTypeSafeEvaluateReplacesIncompleteCachedBatch(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(writer, `{
			"answers": {
				"database-joins": {"type": "choice", "choice": "pass", "confidence": 1},
				"semicolons": {"type": "choice", "choice": "pass", "confidence": 1}
			}
		}`)
	}))
	defer server.Close()

	cache, err := newFileCacheAt(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatalf("newFileCacheAt() error = %v", err)
	}
	client := newCachedTestClient(t, server, cache, false)
	body, err := client.requestBody(testBatch())
	if err != nil {
		t.Fatalf("requestBody() error = %v", err)
	}
	if !cache.Put(client.cacheKey(body), map[string]Result{
		"database-joins": {Status: StatusFail, Confidence: 1},
	}) {
		t.Fatal("Put() = false")
	}

	results, err := client.Evaluate(context.Background(), testBatch())
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if results["database-joins"].Status != StatusPass || len(results) != 2 {
		t.Fatalf("Evaluate() results = %#v", results)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP requests = %d, want 1", got)
	}
}

func TestTypeSafeSkipAndAbstainCriteria(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload systemOneRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		joins := payload.Questions["database-joins"].Criteria
		if joins.Skip != criterionSkip || joins.Abstain != "" {
			t.Errorf("database-joins criteria = %#v", joins)
		}
		semicolons := payload.Questions["semicolons"].Criteria
		if semicolons.Skip != "" || semicolons.Abstain != criterionAbstain {
			t.Errorf("semicolons criteria = %#v", semicolons)
		}
		fmt.Fprint(writer, `{
			"answers": {
				"database-joins": {"type": "choice", "choice": "skip", "confidence": 0.9},
				"semicolons": {"type": "choice", "choice": "abstain", "confidence": 0.8}
			}
		}`)
	}))
	defer server.Close()

	batch := testBatch()
	batch.Rules[0].AllowSkip = true
	batch.Rules[1].AllowAbstain = true
	client := newTestClient(t, server, nil)
	results, err := client.Evaluate(context.Background(), batch)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if results["database-joins"].Status != StatusSkip {
		t.Fatalf("database-joins = %#v", results["database-joins"])
	}
	if results["semicolons"].Status != StatusAbstain {
		t.Fatalf("semicolons = %#v", results["semicolons"])
	}
}

func TestTypeSafeRejectsDisallowedSkipAndAbstain(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"skip":    "skip",
		"abstain": "abstain",
	}
	for name, choice := range tests {
		name, choice := name, choice
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(writer, `{
					"answers": {
						"database-joins": {"type": "choice", "choice": %q, "confidence": 0.9},
						"semicolons": {"type": "choice", "choice": "pass", "confidence": 0.9}
					}
				}`, choice)
			}))
			defer server.Close()
			client := newTestClient(t, server, nil)
			if _, err := client.Evaluate(context.Background(), testBatch()); err == nil {
				t.Fatal("Evaluate() error = nil")
			}
		})
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
		if got := request.Header.Get("X-Jevlint-Retry-Count"); got != "1" {
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

func TestTypeSafeEndpointOverrideUsesFullURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/custom/clef" {
			t.Errorf("path = %q, want /custom/clef", request.URL.Path)
		}
		fmt.Fprint(writer, `{
			"answers": {
				"database-joins": {"type": "choice", "choice": "pass", "confidence": 1},
				"semicolons": {"type": "choice", "choice": "pass", "confidence": 1}
			}
		}`)
	}))
	defer server.Close()

	client, err := NewClient(Options{
		APIKey:     "sk-test",
		Endpoint:   server.URL + "/custom/clef",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	client.model = "clef"
	if _, err := client.Evaluate(context.Background(), testBatch()); err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
}

func TestTypeSafeDebugLogRedactsCredential(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `{
			"answers": {
				"database-joins": {"type": "choice", "choice": "pass", "confidence": 1},
				"semicolons": {"type": "choice", "choice": "pass", "confidence": 1}
			}
		}`)
	}))
	defer server.Close()

	var logs strings.Builder
	client, err := NewClient(Options{
		APIKey:     "sk-super-secret",
		BaseURL:    ServiceURL(server.URL),
		HTTPClient: server.Client(),
		Logf: func(format string, args ...any) {
			fmt.Fprintf(&logs, format+"\n", args...)
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	client.model = "clef"
	if _, err := client.Evaluate(context.Background(), testBatch()); err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	out := logs.String()
	for _, want := range []string{
		"jevlint: request POST ",
		"jevlint: payload to jev:",
		"jevlint: response 200",
		"Authorization=Bearer <redacted>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sk-super-secret") {
		t.Fatalf("log leaked the API key:\n%s", out)
	}
}

func TestDebugLogCapsLargeRequestPayload(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `{"answers":{"database-joins":{"type":"choice","choice":"pass","confidence":1}}}`)
	}))
	defer server.Close()

	var logs strings.Builder
	client, err := NewClient(Options{
		APIKey:     "sk-test",
		BaseURL:    ServiceURL(server.URL),
		HTTPClient: server.Client(),
		Logf: func(format string, args ...any) {
			fmt.Fprintf(&logs, format+"\n", args...)
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	batch := testBatch()
	batch.Rules = batch.Rules[:1]
	batch.CodeUnit.Source = strings.Repeat("x", maxDebugBodyBytes*2)
	if _, err := client.Evaluate(context.Background(), batch); err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	out := logs.String()
	if !strings.Contains(out, "truncated") {
		t.Fatalf("large request payload was not capped")
	}
	if strings.Contains(out, strings.Repeat("x", maxDebugBodyBytes+1)) {
		t.Fatalf("log contained the uncapped payload")
	}
}

func newTestClient(
	t *testing.T,
	server *httptest.Server,
	sleep func(context.Context, time.Duration) error,
) *Client {
	t.Helper()

	client, err := NewClient(Options{
		APIKey:     "sk-test",
		BaseURL:    ServiceURL(server.URL),
		HTTPClient: server.Client(),
		Sleep:      sleep,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	client.model = "jev-test"
	return client
}

func newCachedTestClient(
	t *testing.T,
	server *httptest.Server,
	cache ResultCache,
	refresh bool,
) *Client {
	t.Helper()

	client, err := NewClient(Options{
		APIKey:     "sk-test",
		BaseURL:    ServiceURL(server.URL),
		HTTPClient: server.Client(),
		Cache:      cache,
		Refresh:    refresh,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	client.model = "jev-test"
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
			Language:  parsing.SourceLanguageGo,
			Path:      "store.go",
			Source:    "// Joins users.\nfunc JoinUsers(user User) {}",
			StartLine: 3,
			EndLine:   4,
		},
		Evidence: []evidence.Evidence{
			{
				Kind:      evidence.KindRelatedType,
				Path:      "store.go",
				StartLine: 1,
				EndLine:   1,
				Symbol:    "User",
				Source:    "type User struct{}",
			},
			{
				Kind:      evidence.KindCallee,
				Path:      "users.go",
				StartLine: 20,
				EndLine:   20,
				Symbol:    "loadUsers",
				Source:    "func loadUsers() {}",
			},
		},
	}
}
