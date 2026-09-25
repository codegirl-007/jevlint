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

	"jevlint/internal/config"
	"jevlint/internal/parsing"
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
		Language:     "go",
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

	client, err := NewTypeSafe(TypeSafeOptions{
		APIKey:  "sk-one",
		BaseURL: "https://one.example",
		Model:   "jev-one",
	})
	if err != nil {
		t.Fatalf("NewTypeSafe() error = %v", err)
	}
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

	mutations := map[string]func(*Batch){
		"path": func(batch *Batch) {
			batch.CodeUnit.Path = "other.go"
		},
		"location": func(batch *Batch) {
			batch.CodeUnit.StartLine++
		},
		"related type": func(batch *Batch) {
			batch.CodeUnit.RelatedTypes[0].Source = "type User struct{ ID int }"
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

	for name, options := range map[string]TypeSafeOptions{
		"endpoint": {
			APIKey:  "sk-one",
			BaseURL: "https://two.example",
			Model:   "jev-one",
		},
		"model": {
			APIKey:  "sk-one",
			BaseURL: "https://one.example",
			Model:   "jev-two",
		},
		"credential": {
			APIKey:  "sk-two",
			BaseURL: "https://one.example",
			Model:   "jev-one",
		},
	} {
		name, options := name, options
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			other, err := NewTypeSafe(options)
			if err != nil {
				t.Fatalf("NewTypeSafe() error = %v", err)
			}
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

func newCachedTestClient(
	t *testing.T,
	server *httptest.Server,
	cache ResultCache,
	refresh bool,
) *TypeSafe {
	t.Helper()

	client, err := NewTypeSafe(TypeSafeOptions{
		APIKey:     "sk-test",
		BaseURL:    server.URL,
		Model:      "jev-test",
		HTTPClient: server.Client(),
		Cache:      cache,
		Refresh:    refresh,
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
