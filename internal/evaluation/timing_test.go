package evaluation

import (
	"testing"
	"time"
)

type stubBackend struct{}

func (stubBackend) ProviderName() string { return "test" }
func (stubBackend) Endpoint() string     { return "https://example.test/v1" }
func (stubBackend) Model() string        { return "test-model" }

func TestNewRunTimingIncludesBackend(t *testing.T) {
	t.Parallel()

	timing := NewRunTiming(2*time.Second, stubBackend{})
	if timing.Provider != "test" || timing.Model != "test-model" {
		t.Fatalf("timing = %#v", timing)
	}
	if timing.TotalSeconds < 1.9 {
		t.Fatalf("total = %v", timing.TotalSeconds)
	}
}
