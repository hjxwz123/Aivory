package rag

import "context"

// DocumentRouter is an optional typed routing path for decision models. The
// boolean reports whether a decision model is selected; errors on that path
// fall back to retrieval, not a generative call to the decision provider.
type DocumentRouter interface {
	RouteDocuments(context.Context, DocumentRouteInput, RouterOpts) (RouteDecision, bool, error)
}

type DocumentRouteInput struct {
	Message   string              `json:"message"`
	Documents []DocumentRouteHint `json:"documents"`
}

// IDs are supplied by the already-authorized retrieval scope. Names remain
// untrusted user data; current_turn is assigned by the application.
type DocumentRouteHint struct {
	DocumentID  string `json:"document_id"`
	Filename    string `json:"filename"`
	CurrentTurn bool   `json:"current_turn"`
	Indexed     bool   `json:"indexed"`
}
