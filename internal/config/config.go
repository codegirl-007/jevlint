package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type Config struct {
	Languages map[string]Language `json:"languages"`
	Rules     []Rule              `json:"rules"`
}

type Language struct {
	Extensions         []string            `json:"extensions,omitempty"`
	FunctionQueries    []string            `json:"functionQueries,omitempty"`
	TypeQueries        []string            `json:"typeQueries,omitempty"`
	TypeContextQueries []string            `json:"typeContextQueries,omitempty"`
	Regions            map[string][]string `json:"regions,omitempty"`
}

type Rule struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Severity    Severity `json:"severity"`
	Include     []string `json:"include,omitempty"`
	Exclude     []string `json:"exclude,omitempty"`
	Exceptions  []string `json:"exceptions,omitempty"`
	Kinds       []string `json:"kinds,omitempty"`
	Localize    []string `json:"localize,omitempty"`
}

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

var languagePresets = map[string]struct{}{
	"c":          {},
	"cpp":        {},
	"csharp":     {},
	"go":         {},
	"java":       {},
	"javascript": {},
	"kotlin":     {},
	"php":        {},
	"python":     {},
	"ruby":       {},
	"rust":       {},
	"tsx":        {},
	"typescript": {},
}

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
	if err := validateLanguages(cfg.Languages); err != nil {
		return err
	}
	if len(cfg.Rules) == 0 {
		return errors.New("config must contain at least one rule")
	}

	ids := make(map[string]struct{}, len(cfg.Rules))
	for index, rule := range cfg.Rules {
		prefix := fmt.Sprintf("rules[%d]", index)
		if err := validateRuleID(rule, prefix); err != nil {
			return err
		}
		if _, exists := ids[rule.ID]; exists {
			return fmt.Errorf("duplicate rule id %q", rule.ID)
		}
		ids[rule.ID] = struct{}{}

		if err := validateRuleDescription(rule, prefix); err != nil {
			return err
		}
		if err := validateRuleSeverity(rule, prefix); err != nil {
			return err
		}
		if err := validateRulePatterns(rule, prefix); err != nil {
			return err
		}
		if err := validateRuleLocalization(rule, prefix); err != nil {
			return err
		}
		if err := validateRuleKinds(rule, prefix); err != nil {
			return err
		}
	}
	return nil
}

func validateLanguages(languages map[string]Language) error {
	if len(languages) == 0 {
		return errors.New("config must enable at least one language")
	}

	ids := make([]string, 0, len(languages))
	for id := range languages {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	extensions := make(map[string]string)
	for _, id := range ids {
		language := languages[id]
		if _, ok := languagePresets[id]; !ok {
			return fmt.Errorf("languages contains unknown preset %q", id)
		}
		if err := validateLanguageExtensions(id, language.Extensions, extensions); err != nil {
			return err
		}
		if err := validateQueryOverride(id, "functionQueries", language.FunctionQueries); err != nil {
			return err
		}
		if err := validateQueryOverride(id, "typeQueries", language.TypeQueries); err != nil {
			return err
		}
		if err := validateQueryOverride(
			id,
			"typeContextQueries",
			language.TypeContextQueries,
		); err != nil {
			return err
		}
		if err := validateRegionOverrides(id, language.Regions); err != nil {
			return err
		}
	}
	return nil
}

func validateLanguageExtensions(
	id string,
	values []string,
	seen map[string]string,
) error {
	if values != nil && len(values) == 0 {
		return fmt.Errorf("languages.%s.extensions cannot be empty", id)
	}
	for _, extension := range values {
		if extension == "" ||
			!strings.HasPrefix(extension, ".") ||
			extension != strings.ToLower(extension) ||
			strings.ContainsAny(extension, `/\`) {
			return fmt.Errorf(
				"languages.%s.extensions contains invalid extension %q",
				id,
				extension,
			)
		}
		if owner, exists := seen[extension]; exists {
			return fmt.Errorf(
				"language extension %q is assigned to both %q and %q",
				extension,
				owner,
				id,
			)
		}
		seen[extension] = id
	}
	return nil
}

func validateQueryOverride(id string, field string, values []string) error {
	if values == nil {
		return nil
	}
	if len(values) == 0 {
		return fmt.Errorf("languages.%s.%s cannot be empty", id, field)
	}
	for _, query := range values {
		if strings.TrimSpace(query) == "" {
			return fmt.Errorf("languages.%s.%s contains an empty query", id, field)
		}
	}
	return nil
}

func validateRegionOverrides(id string, regions map[string][]string) error {
	if regions == nil {
		return nil
	}
	for category, kinds := range regions {
		switch category {
		case "comment", "field", "statement":
		default:
			return fmt.Errorf(
				"languages.%s.regions contains invalid category %q",
				id,
				category,
			)
		}
		if len(kinds) == 0 {
			return fmt.Errorf(
				"languages.%s.regions.%s cannot be empty",
				id,
				category,
			)
		}
		for _, kind := range kinds {
			if strings.TrimSpace(kind) == "" {
				return fmt.Errorf(
					"languages.%s.regions.%s contains an empty node kind",
					id,
					category,
				)
			}
		}
	}
	return nil
}

func validateRuleID(rule Rule, prefix string) error {
	if strings.TrimSpace(rule.ID) == "" {
		return fmt.Errorf("%s.id is required", prefix)
	}
	return nil
}

func validateRuleDescription(rule Rule, prefix string) error {
	if strings.TrimSpace(rule.Description) == "" {
		return fmt.Errorf("%s.description is required", prefix)
	}
	return nil
}

func validateRuleSeverity(rule Rule, prefix string) error {
	switch rule.Severity {
	case SeverityInfo, SeverityWarning, SeverityError:
		return nil
	default:
		return fmt.Errorf("%s.severity must be info, warning, or error", prefix)
	}
}

func validateRulePatterns(rule Rule, prefix string) error {
	patterns := append(append([]string{}, rule.Include...), rule.Exclude...)
	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("%s contains an empty file pattern", prefix)
		}
	}
	return nil
}

func validateRuleLocalization(rule Rule, prefix string) error {
	for _, category := range rule.Localize {
		switch category {
		case "comment", "field", "statement":
		default:
			return fmt.Errorf(
				"%s.localize contains invalid category %q; want comment, field, or statement",
				prefix,
				category,
			)
		}
	}
	return nil
}

func validateRuleKinds(rule Rule, prefix string) error {
	for _, kind := range rule.Kinds {
		switch kind {
		case "function", "type":
		default:
			return fmt.Errorf(
				"%s.kinds contains invalid kind %q; want function or type",
				prefix,
				kind,
			)
		}
	}
	return nil
}
