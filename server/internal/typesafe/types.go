// Package typesafe implements TypeSafe's non-streaming System One HTTP API.
// It returns judgments; thresholds, permissions and actions belong to callers.
package typesafe

import (
	"encoding/json"
	"time"
)

const (
	DefaultBaseURL = "https://api.typesafe.ai/v1"
	// Pin the release used to validate this integration. Aliases remain opt-in.
	DefaultModel = "jev-1.13.0"
)

type QuestionType string

const (
	Choice QuestionType = "choice"
	Score  QuestionType = "score"
	Noul   QuestionType = "noul"
)

// Question accepts structured instructions and criteria as documented by the
// API. Use NewChoice/NewScore/NewNoul for convenient construction, or unmarshal
// JSON into Request for dynamically configured rubrics. Evaluate validates both.
type Question struct {
	Type         QuestionType `json:"type"`
	Instructions any          `json:"instructions"`
	Criteria     any          `json:"criteria,omitempty"`
}

func NewChoice(instructions any, options map[string]any) Question {
	return Question{Type: Choice, Instructions: instructions, Criteria: options}
}

// Levels are zero-indexed; the API supports between 2 and 10 levels.
func NewScore(instructions any, levels []any) Question {
	return Question{Type: Score, Instructions: instructions, Criteria: levels}
}

// criteria is optional; when supplied its keys are "true" and "false".
func NewNoul(instructions any, criteria map[string]any) Question {
	if criteria == nil {
		return Question{Type: Noul, Instructions: instructions}
	}
	return Question{Type: Noul, Instructions: instructions, Criteria: criteria}
}

type Request struct {
	State     any                 `json:"state"`
	Model     string              `json:"model,omitempty"`
	Questions map[string]Question `json:"questions"`
}

// Answer is a validated tagged union. Pointer fields distinguish a legitimate
// zero from an absent field. Noul answers never acquire synthetic confidence.
type Answer struct {
	Type          QuestionType               `json:"type"`
	Choice        *string                    `json:"choice,omitempty"`
	Score         *float64                   `json:"score,omitempty"`
	Noul          *float64                   `json:"noul,omitempty"`
	Confidence    *float64                   `json:"confidence,omitempty"`
	Probabilities map[string]float64         `json:"probabilities,omitempty"`
	Legend        map[string]json.RawMessage `json:"legend,omitempty"`
}

// UnmarshalJSON rejects null probabilities (encoding/json would otherwise
// silently turn them into zero, which is a valid and actionable probability).
func (a *Answer) UnmarshalJSON(data []byte) error {
	type plain Answer
	var wire struct {
		*plain
		Probabilities map[string]*float64 `json:"probabilities"`
	}
	decoded := Answer{}
	wire.plain = (*plain)(&decoded)
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Probabilities != nil {
		decoded.Probabilities = make(map[string]float64, len(wire.Probabilities))
		for k, v := range wire.Probabilities {
			if v == nil {
				return failure(ErrResponse, "null probability")
			}
			decoded.Probabilities[k] = *v
		}
	}
	*a = decoded
	return nil
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
	// RequestID is the upstream x-request-id, when provided. RequestedModel
	// preserves an alias even when Model reports a concrete release.
	RequestID      string `json:"-"`
	RequestedModel string `json:"-"`
}

// Metadata is local accounting context. It is never sent to TypeSafe.
type Metadata struct {
	Purpose        string `json:"purpose"`
	UserID         string `json:"user_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	MessageID      string `json:"message_id,omitempty"`
	WorkspaceID    string `json:"workspace_id,omitempty"`
}

type Options struct {
	Metadata Metadata
	// Timeout bounds the complete evaluation, including retries/backoff.
	// Zero uses Config.Timeout; a caller's earlier deadline always wins.
	Timeout time.Duration
	// ExpectedModel optionally locks an alias to a tested concrete release.
	// Pinned requests already enforce that the returned model matches the pin.
	ExpectedModel string
}
