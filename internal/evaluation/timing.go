package evaluation

import "time"

// RunTiming summarizes wall time for comparing decision backends.
type RunTiming struct {
	TotalSeconds float64 `json:"totalSeconds"`
	Provider     string  `json:"provider,omitempty"`
	Model        string  `json:"model,omitempty"`
	Endpoint     string  `json:"endpoint,omitempty"`
}

// BackendInfo describes the active decision service.
type BackendInfo interface {
	ProviderName() string
	Endpoint() string
	Model() string
}

// NewRunTiming builds a timing summary from a run.
func NewRunTiming(total time.Duration, backend BackendInfo) RunTiming {
	timing := RunTiming{TotalSeconds: total.Seconds()}
	if backend != nil {
		timing.Provider = backend.ProviderName()
		timing.Model = backend.Model()
		timing.Endpoint = backend.Endpoint()
	}
	return timing
}
