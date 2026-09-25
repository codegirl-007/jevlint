package evaluation

import (
	"context"
	"fmt"

	"jevlint/internal/config"
	"jevlint/internal/parsing"
)

type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
)

type Batch struct {
	Rules    []config.Rule    `json:"rules"`
	CodeUnit parsing.CodeUnit `json:"codeUnit"`
}

type Result struct {
	Status     Status  `json:"status"`
	Confidence float64 `json:"confidence"`
}

type Evaluator interface {
	Evaluate(context.Context, Batch) (map[string]Result, error)
}

type CacheStats struct {
	Hits   uint64 `json:"hits"`
	Misses uint64 `json:"misses"`
	Writes uint64 `json:"writes"`
}

type CacheStatsProvider interface {
	CacheStats() CacheStats
}

func (result Result) Validate() error {
	switch result.Status {
	case StatusPass, StatusFail:
	default:
		return fmt.Errorf("invalid evaluation status %q", result.Status)
	}
	if result.Confidence < 0 || result.Confidence > 1 {
		return fmt.Errorf("confidence must be between 0 and 1")
	}
	return nil
}
