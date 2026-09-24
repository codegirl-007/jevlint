package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type Config struct {
	Rules []Rule `json:"rules"`
}

type Rule struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	Severity    Severity  `json:"severity"`
	Include     []string  `json:"include,omitempty"`
	Exclude     []string  `json:"exclude,omitempty"`
	Exceptions  []string  `json:"exceptions,omitempty"`
	Examples    *Examples `json:"examples,omitempty"`
}

type Examples struct {
	Good []string `json:"good,omitempty"`
	Bad  []string `json:"bad,omitempty"`
}

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	return Decode(file)
}

func Decode(reader io.Reader) (Config, error) {
	var cfg Config
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("decode config: multiple JSON values")
		}
		return Config{}, fmt.Errorf("decode config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg Config) Validate() error {
	if len(cfg.Rules) == 0 {
		return errors.New("config must contain at least one rule")
	}

	ids := make(map[string]struct{}, len(cfg.Rules))
	for index, rule := range cfg.Rules {
		prefix := fmt.Sprintf("rules[%d]", index)
		if strings.TrimSpace(rule.ID) == "" {
			return fmt.Errorf("%s.id is required", prefix)
		}
		if _, exists := ids[rule.ID]; exists {
			return fmt.Errorf("duplicate rule id %q", rule.ID)
		}
		ids[rule.ID] = struct{}{}

		if strings.TrimSpace(rule.Description) == "" {
			return fmt.Errorf("%s.description is required", prefix)
		}
		switch rule.Severity {
		case SeverityInfo, SeverityWarning, SeverityError:
		default:
			return fmt.Errorf("%s.severity must be info, warning, or error", prefix)
		}
		for _, pattern := range append(append([]string{}, rule.Include...), rule.Exclude...) {
			if strings.TrimSpace(pattern) == "" {
				return fmt.Errorf("%s contains an empty file pattern", prefix)
			}
		}
	}
	return nil
}
