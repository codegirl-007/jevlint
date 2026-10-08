package evaluation

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/codegirl-007/jevlint/internal/config"
)

const (
	openAIDecisionsURL = "https://api.openai.com/v1/decisions"
	openAIModel        = "gpt-6-luna"
)

// OpenAIProvider talks to OpenAI's Decisions API.
type OpenAIProvider struct{}

// Name identifies the provider.
func (OpenAIProvider) Name() string { return "openai" }

// BaseURL is unused when Configure sets a full decisions endpoint.
func (OpenAIProvider) BaseURL() string { return "https://api.openai.com" }

// DefaultModel is the only Decisions model OpenAI documents today.
func (OpenAIProvider) DefaultModel() string { return openAIModel }

// Configure fills options from OPENAI_API_KEY.
func (OpenAIProvider) Configure(options *Options, getenv func(string) string) error {
	if options.Endpoint == "" {
		options.Endpoint = strings.TrimSpace(getenv("TYPESAFE_ENDPOINT"))
	}
	if options.Endpoint == "" {
		options.Endpoint = openAIDecisionsURL
	}
	if options.APIKey == "" {
		options.APIKey = APIKey(cleanCredential(getenv("OPENAI_API_KEY")))
	}
	if options.Model == "" {
		options.Model = openAIModel
	}
	if options.APIKey == "" {
		return errors.New(
			"OPENAI_API_KEY is required when JEVLINT_PROVIDER=openai",
		)
	}
	return nil
}

// ValidateQuestions applies no extra limits.
func (OpenAIProvider) ValidateQuestions(map[string]question) error { return nil }

// MaxBodyBytes applies no explicit body cap.
func (OpenAIProvider) MaxBodyBytes() int { return 0 }

// MarshalRequest builds a POST /v1/decisions body.
func (OpenAIProvider) MarshalRequest(model string, batch Batch) ([]byte, error) {
	stateJSON, err := json.Marshal(requestStateFrom(batch.CodeUnit, batch.Evidence))
	if err != nil {
		return nil, fmt.Errorf("encode input state: %w", err)
	}

	rules := append([]config.Rule(nil), batch.Rules...)
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })

	questions := make([]openAIDecisionQuestion, 0, len(rules))
	for _, rule := range rules {
		criteria := criteriaFor(rule)
		choices := openAIChoicesFromCriteria(criteria)
		questions = append(questions, openAIDecisionQuestion{
			Type:         answerTypeChoice,
			Name:         rule.ID,
			Instructions: instructionsFor(rule, batch.CodeUnit, len(batch.Evidence) > 0),
			Choices:      choices,
		})
	}

	return json.Marshal(openAIDecisionRequest{
		Model:     model,
		Input:     string(stateJSON),
		Questions: questions,
	})
}

// Answers reads the answers array from an OpenAI Decisions response.
func (OpenAIProvider) Answers(body []byte) (map[string]choiceAnswer, error) {
	var response openAIDecisionResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(response.Answers) == 0 {
		return nil, errors.New("decode response: answers are missing")
	}

	answers := make(map[string]choiceAnswer, len(response.Answers))
	for _, item := range response.Answers {
		if item.Name == "" {
			return nil, errors.New("decode response: answer name is missing")
		}
		if item.Type == "refusal" {
			return nil, fmt.Errorf(
				"decode response: answer for rule %q was refused",
				item.Name,
			)
		}
		if item.Type != answerTypeChoice {
			return nil, fmt.Errorf(
				"decode response: answer for rule %q has unsupported type %q",
				item.Name,
				item.Type,
			)
		}
		confidence := item.Confidence
		answers[item.Name] = choiceAnswer{
			Type:       questionTypeChoice,
			Choice:     item.Choice,
			Confidence: &confidence,
		}
	}
	return answers, nil
}

// ResponseError reads OpenAI's error JSON.
func (OpenAIProvider) ResponseError(
	status int,
	headers http.Header,
	body []byte,
) error {
	message, code := parseServiceError(body)
	if code != 0 {
		message = fmt.Sprintf("%s (code %d)", message, code)
	}
	return serviceError(status, headers.Get("x-request-id"), message)
}

// DescribeCredential names the credential without revealing it.
func (OpenAIProvider) DescribeCredential(key string) string {
	if strings.HasPrefix(key, "sk-") {
		return fmt.Sprintf("openai-key (len %d)", len(key))
	}
	return fmt.Sprintf("unknown (len %d)", len(key))
}

// Headers returns no extra request headers.
func (OpenAIProvider) Headers() map[string]string { return nil }

type openAIDecisionRequest struct {
	Model     string                  `json:"model"`
	Input     string                  `json:"input"`
	Questions []openAIDecisionQuestion `json:"questions"`
}

type openAIDecisionQuestion struct {
	Type         string              `json:"type"`
	Name         string              `json:"name"`
	Instructions string              `json:"instructions"`
	Choices      []openAIDecisionChoice `json:"choices"`
}

type openAIDecisionChoice struct {
	ChoiceValue string `json:"value"`
	Description string `json:"description"`
}

type openAIDecisionResponse struct {
	Answers []openAIDecisionAnswerItem `json:"answers"`
}

type openAIDecisionAnswerItem struct {
	Type       string  `json:"type"`
	Name       string  `json:"name"`
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
}

func openAIChoicesFromCriteria(criteria questionCriteria) []openAIDecisionChoice {
	choices := make([]openAIDecisionChoice, 0, 4)
	appendChoice := func(choiceValue, description string) {
		if description == "" {
			return
		}
		choices = append(choices, openAIDecisionChoice{
			ChoiceValue: choiceValue,
			Description: description,
		})
	}
	appendChoice("pass", criteria.Pass)
	appendChoice("fail", criteria.Fail)
	appendChoice("skip", criteria.Skip)
	appendChoice("abstain", criteria.Abstain)
	return choices
}
