package rag

import (
	"context"
	"errors"
	"testing"

	"aivory/server/internal/store"
)

type typedDocumentTestRouter struct {
	recordingRouter
	handled    bool
	decision   RouteDecision
	routeErr   error
	input      DocumentRouteInput
	opts       RouterOpts
	routeCalls int
}

func (r *typedDocumentTestRouter) RouteDocuments(_ context.Context, input DocumentRouteInput, opts RouterOpts) (RouteDecision, bool, error) {
	r.routeCalls++
	r.input, r.opts = input, opts
	return r.decision, r.handled, r.routeErr
}

func TestTypedDocumentRouterPreservesScopeAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		handled   bool
		routeErr  error
		want      string
		chatCalls int
	}{
		{"decision model", true, nil, "none", 0},
		{"decision failure", true, errors.New("provider unavailable"), "retrieve", 0},
		{"ordinary model", false, nil, "none", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.InvalidateConfig()
			t.Cleanup(store.InvalidateConfig)
			ctx := context.Background()
			db := seedEmbeddedConversationDoc(t, ctx)
			defer db.Close()
			if err := store.SetSetting(db, "rag_full_text_threshold", 1); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('excluded','c1','excluded.txt','text/plain',10,'ready')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content) VALUES('excluded-chunk','excluded','c1',0,'text','not in allowed scope')`); err != nil {
				t.Fatal(err)
			}
			router := &typedDocumentTestRouter{handled: tc.handled, routeErr: tc.routeErr, decision: RouteDecision{Strategy: "none"}, recordingRouter: recordingRouter{decision: RouteDecision{Strategy: "none"}}}
			svc := New(db, nil, nil)
			svc.SetTaskLLM(router)
			_, decision, err := svc.RouteAndRetrieveDocumentScope(ctx, "u1", "c1", nil, []string{"d1"}, []string{"d1"}, "question", nil, 8)
			if err != nil || decision.Strategy != tc.want || router.routeCalls != 1 || router.calls != tc.chatCalls {
				t.Fatalf("decision=%+v err=%v typed=%d chat=%d", decision, err, router.routeCalls, router.calls)
			}
			if router.input.Message != "question" || len(router.input.Documents) != 1 || router.input.Documents[0].DocumentID != "d1" || !router.input.Documents[0].CurrentTurn || router.opts.UserID != "u1" {
				t.Fatalf("scope/metadata lost: %+v %+v", router.input, router.opts)
			}
			if tc.want == "retrieve" && (len(decision.Queries) != 1 || decision.Queries[0] != "question") {
				t.Fatalf("fallback query=%v", decision.Queries)
			}
		})
	}
}

func TestTypedDocumentRouterFullCoverageSelection(t *testing.T) {
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	if err := store.SetSetting(db, "rag_full_text_threshold", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('d2','c1','second.txt','text/plain',10,'ready')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model) VALUES('second-chunk','d2','c1',0,'text','selected text','aivory-local-embed')`); err != nil {
		t.Fatal(err)
	}
	router := &typedDocumentTestRouter{handled: true, decision: RouteDecision{Strategy: "full_doc", DocumentIDs: []string{"d2", "outside-scope"}}}
	svc := New(db, nil, nil)
	svc.SetTaskLLM(router)
	snippets, decision, err := svc.RouteAndRetrieve(ctx, "u1", "c1", nil, "summarize the relevant document", nil, 8)
	if err != nil || decision.Strategy != "full_doc" || len(snippets) != 1 || snippets[0].URL != "doc://d2" || router.calls != 0 {
		t.Fatalf("selection=%+v snippets=%+v chat=%d err=%v", decision, snippets, router.calls, err)
	}
	// Small authorized scopes still use the deterministic full-text fast path.
	if err := store.SetSetting(db, "rag_full_text_threshold", 1000); err != nil {
		t.Fatal(err)
	}
	_, decision, err = svc.RouteAndRetrieve(ctx, "u1", "c1", nil, "question", nil, 8)
	if err != nil || decision.Strategy != "full_text" || router.routeCalls != 1 {
		t.Fatalf("fast path decision=%+v typed=%d err=%v", decision, router.routeCalls, err)
	}
}
