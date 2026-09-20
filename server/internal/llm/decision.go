package llm

import (
	"context"
	"strings"

	"aivory/server/internal/store"
	"aivory/server/internal/typesafe"
)

// RunDecision resolves an administrator-managed database model for every call.
// Credentials, upstream model/version and pricing come exclusively from the
// saved model and channel. Request.Model cannot override the selected model.
// All callers share policy validation, quota admission, accounting and logging.
func (t *TaskLLM) RunDecision(ctx context.Context, modelID string, req typesafe.Request, opts typesafe.Options) (*typesafe.Response, error) {
	if t == nil || t.db == nil || strings.TrimSpace(modelID) == "" {
		return nil, &typesafe.Error{Kind: typesafe.ErrDisabled, Message: "decision model is not configured"}
	}
	if opts.Metadata.Purpose == "" {
		opts.Metadata.Purpose = "task.decision"
	}
	if !strings.HasPrefix(opts.Metadata.Purpose, "task.") {
		return nil, &typesafe.Error{Kind: typesafe.ErrValidation, Message: "decision purpose must start with task."}
	}
	model, err := store.GetModel(ctx, t.db, strings.TrimSpace(modelID))
	if err != nil {
		return nil, err
	}
	return t.runPolicyDecision(ctx, model, req, opts)
}
