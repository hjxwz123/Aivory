package llm

import (
	"context"
	"fmt"

	"aivory/server/internal/rag"
	"aivory/server/internal/systemprompts"
	"aivory/server/internal/typesafe"
)

// Bound the hot-path request. If metadata exceeds the candidate budget, retain
// normal retrieval over the entire authorized scope rather than truncate it.
const decisionDocumentLimit = 128

func (t *TaskLLM) RouteDocuments(ctx context.Context, input rag.DocumentRouteInput, opts rag.RouterOpts) (rag.RouteDecision, bool, error) {
	model := t.policyDecisionModel(ctx, "file_route_model_id")
	if model == nil {
		return rag.RouteDecision{}, false, nil
	}
	fallback := rag.RouteDecision{Strategy: "retrieve", Queries: []string{input.Message}}
	if len(input.Documents) == 0 || len(input.Documents) > decisionDocumentLimit {
		return fallback, true, nil
	}
	questions := map[string]typesafe.Question{
		"strategy": typesafe.NewChoice(systemprompts.Text(t.db, "decision.documents.instructions"), map[string]any{
			"none":     systemprompts.Text(t.db, "decision.documents.none"),
			"retrieve": systemprompts.Text(t.db, "decision.documents.retrieve"),
			"full_doc": systemprompts.Text(t.db, "decision.documents.full_doc"),
		}),
	}
	for i, document := range input.Documents {
		questions[fmt.Sprintf("document_%d", i)] = typesafe.NewNoul(map[string]any{
			"question":  systemprompts.Text(t.db, "decision.documents.candidate"),
			"candidate": document,
		}, nil)
	}
	result, err := t.RunDecision(ctx, model.ID, typesafe.Request{State: input, Questions: questions}, typesafe.Options{Metadata: typesafe.Metadata{
		Purpose: string(TaskRouter), UserID: opts.UserID, ConversationID: opts.ConversationID, MessageID: opts.MessageID, WorkspaceID: opts.WorkspaceID,
	}})
	if err != nil {
		return fallback, true, err
	}
	strategy := result.Answers["strategy"]
	if *strategy.Confidence < 0.8 {
		return fallback, true, nil
	}
	switch *strategy.Choice {
	case "none":
		return rag.RouteDecision{Strategy: "none"}, true, nil
	case "full_doc":
		selected := []string{}
		seen := map[string]bool{}
		for i, document := range input.Documents {
			// Retain uncertain candidates; only exclude confidently irrelevant ones.
			if *result.Answers[fmt.Sprintf("document_%d", i)].Noul > 0.15 && document.DocumentID != "" && !seen[document.DocumentID] {
				selected = append(selected, document.DocumentID)
				seen[document.DocumentID] = true
			}
		}
		// Contradictory answers must not silently turn into full coverage of an
		// arbitrary current file. Use the original-query retrieval fallback.
		if len(selected) == 0 {
			return fallback, true, nil
		}
		return rag.RouteDecision{Strategy: "full_doc", DocumentIDs: selected}, true, nil
	default:
		return fallback, true, nil
	}
}
