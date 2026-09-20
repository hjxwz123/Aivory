package typesafe

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Validate the serialized request so structs, typed maps, raw JSON and slices
// all follow the same rules. The snapshot is reused for retries and response
// validation, so caller-owned maps cannot change the meaning between attempts.
func prepare(req Request) ([]byte, Request, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, Request{}, failure(ErrValidation, "request is not JSON serializable")
	}
	var snapshot Request
	if json.Unmarshal(body, &snapshot) != nil {
		return nil, Request{}, failure(ErrValidation, "invalid request JSON")
	}
	if !entry(snapshot.State, false) {
		return nil, Request{}, failure(ErrValidation, "state must be a string, object or array")
	}
	if len(snapshot.Questions) == 0 {
		return nil, Request{}, failure(ErrValidation, "at least one question is required")
	}
	for id, q := range snapshot.Questions {
		if strings.TrimSpace(id) == "" || !entry(q.Instructions, true) {
			return nil, Request{}, failure(ErrValidation, "invalid question id or instructions")
		}
		switch q.Type {
		case Choice:
			criteria, ok := q.Criteria.(map[string]any)
			if !ok || len(criteria) == 0 || len(criteria) > 255 {
				return nil, Request{}, failure(ErrValidation, "choice requires 1 to 255 options")
			}
			for _, v := range criteria {
				if !entry(v, true) {
					return nil, Request{}, failure(ErrValidation, "invalid choice description")
				}
			}
		case Score:
			levels, ok := q.Criteria.([]any)
			if !ok || len(levels) < 2 || len(levels) > 10 {
				return nil, Request{}, failure(ErrValidation, "score requires 2 to 10 ordered levels")
			}
			for _, v := range levels {
				if !entry(v, true) {
					return nil, Request{}, failure(ErrValidation, "invalid score description")
				}
			}
		case Noul:
			if q.Criteria == nil {
				continue
			}
			criteria, ok := q.Criteria.(map[string]any)
			if !ok {
				return nil, Request{}, failure(ErrValidation, "noul criteria must be an object")
			}
			for k, v := range criteria {
				if (k != "true" && k != "false") || !entry(v, true) {
					return nil, Request{}, failure(ErrValidation, "invalid noul criteria")
				}
			}
		default:
			return nil, Request{}, failure(ErrValidation, "unsupported question type")
		}
	}
	return body, snapshot, nil
}

func entry(v any, nullable bool) bool {
	switch v.(type) {
	case string, map[string]any, []any:
		return true
	case nil:
		return nullable
	default:
		return false
	}
}

func probability(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1
}

func validateResponse(req Request, result *Response, expected string) error {
	if strings.TrimSpace(result.Model) == "" {
		return failure(ErrResponse, "missing response model")
	}
	if expected != "" && result.Model != expected {
		return failure(ErrModelVersion, "served model does not match expected release")
	}
	if len(result.Answers) != len(req.Questions) {
		return failure(ErrResponse, "answer count does not match questions")
	}
	for id, q := range req.Questions {
		a, ok := result.Answers[id]
		if !ok || a.Type != q.Type {
			return failure(ErrResponse, "missing answer or mismatched answer type")
		}
		if q.Type == Noul {
			if a.Noul == nil || !probability(*a.Noul) || a.Choice != nil || a.Score != nil || a.Confidence != nil {
				return failure(ErrResponse, "invalid noul answer")
			}
			continue
		}
		if a.Confidence == nil || !probability(*a.Confidence) || a.Noul != nil {
			return failure(ErrResponse, "missing or invalid confidence")
		}
		var keys []string
		if q.Type == Choice {
			options := q.Criteria.(map[string]any)
			if a.Choice == nil || a.Score != nil {
				return failure(ErrResponse, "invalid choice answer")
			}
			if _, ok := options[*a.Choice]; !ok {
				return failure(ErrResponse, "choice is not a requested option")
			}
			for k := range options {
				keys = append(keys, k)
			}
		} else {
			levels := q.Criteria.([]any)
			if a.Score == nil || a.Choice != nil || math.IsNaN(*a.Score) || math.IsInf(*a.Score, 0) || *a.Score < 0 || *a.Score > float64(len(levels)-1) || len(a.Legend) != len(levels) {
				return failure(ErrResponse, "invalid score or legend")
			}
			for i := range levels {
				k := strconv.Itoa(i)
				if _, ok := a.Legend[k]; !ok {
					return failure(ErrResponse, "missing score legend level")
				}
				keys = append(keys, k)
			}
		}
		if len(a.Probabilities) != len(keys) {
			return failure(ErrResponse, "probability options do not match criteria")
		}
		sum := 0.0
		for _, k := range keys {
			p, ok := a.Probabilities[k]
			if !ok || !probability(p) {
				return failure(ErrResponse, "missing or invalid probability")
			}
			sum += p
		}
		// Permit harmless provider rounding without accepting an invalid distribution.
		if math.Abs(sum-1) > 0.01 {
			return failure(ErrResponse, "probabilities do not sum to one")
		}
	}
	return nil
}
