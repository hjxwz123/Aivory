package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestWorkspaceMembershipRevocationRemovesCreatorAccessEverywhere(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "workspace-revocation.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, id := range []string{"owner", "creator", "member", "leaver"} {
		exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES(?,?, 'h','user','active')`, id, id+"@example.test")
	}
	workspace, err := CreateWorkspace(ctx, db, "owner", "Security")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	for _, id := range []string{"creator", "member", "leaver"} {
		if err := JoinWorkspace(ctx, db, workspace.ID, id); err != nil {
			t.Fatalf("join %s: %v", id, err)
		}
	}
	originalInviteToken := workspace.InviteToken
	exec(t, db, `INSERT INTO channels(id,name,type) VALUES('rev-ch','Embedding','openai')`)
	exec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES('rev-emb','rev-ch','embedding','emb','Embedding',3)`)

	conversation, err := CreateConversation(ctx, db, Conversation{
		ID: "rev-conv", UserID: "creator", WorkspaceID: workspace.ID, IsPublic: true,
		Title: "Revocation secret",
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	project, err := CreateProject(ctx, db, Project{
		ID: "rev-project", UserID: "creator", WorkspaceID: workspace.ID, Name: "Shared project",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	kb, err := CreateKB(ctx, db, KnowledgeBase{
		ID: "rev-kb", UserID: "creator", WorkspaceID: workspace.ID, Name: "Shared KB",
		EmbeddingModelID: "rev-emb", EmbeddingDim: 3,
	})
	if err != nil {
		t.Fatalf("create kb: %v", err)
	}
	file, err := CreateFile(ctx, db, File{
		ID: "rev-file", UserID: "creator", ConversationID: conversation.ID,
		Filename: "secret.txt", MimeType: "text/plain", Kind: "text",
		StoragePath: "/tmp/revocation-secret", SizeBytes: 10,
	})
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	document, err := CreateDocumentForUser(ctx, db, Document{
		ID: "rev-doc", KBID: kb.ID, Filename: "secret.txt", MimeType: "text/plain",
		StoragePath: "/tmp/revocation-document", Status: "ready",
	}, "creator")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	retryDocument, err := CreateDocumentForUser(ctx, db, Document{
		ID: "rev-retry-doc", ConversationID: conversation.ID,
		Filename: "retry.txt", MimeType: "text/plain", SizeBytes: 7,
		StoragePath: "/tmp/revocation-retry-document", Status: "failed",
	}, "creator")
	if err != nil {
		t.Fatalf("create retry document: %v", err)
	}
	legacyQuestion, err := CreateMessage(ctx, db, Message{
		ID: "rev-question", ConversationID: conversation.ID, Role: "user",
		Blocks: json.RawMessage(`[{"kind":"text","text":"revocation phrase"}]`),
		// Empty AuthorID is a legacy row and belongs to the conversation creator.
	})
	if err != nil {
		t.Fatalf("create legacy question: %v", err)
	}
	answer, err := CreateMessage(ctx, db, Message{
		ID: "rev-answer", ConversationID: conversation.ID, ParentID: legacyQuestion.ID,
		Role: "assistant", Blocks: json.RawMessage(`[{"kind":"text","text":"answer"}]`),
	})
	if err != nil {
		t.Fatalf("create answer: %v", err)
	}
	providerStateMessage, err := CreateMessageForUser(ctx, db, Message{
		ID: "rev-provider-state-message", ConversationID: conversation.ID,
		ParentID: answer.ID, Role: "assistant", AuthorID: "creator",
		Blocks: json.RawMessage(`[]`), Status: "streaming",
	}, "creator")
	if err != nil {
		t.Fatalf("create provider-state placeholder: %v", err)
	}
	if _, err := SetMessageFeedbackForUser(ctx, db, MessageFeedback{
		MessageID: answer.ID, ConversationID: conversation.ID, UserID: "creator",
		Rating: MessageFeedbackLike,
	}); err != nil {
		t.Fatalf("create feedback before kick: %v", err)
	}
	artifact, err := CreateArtifact(ctx, db, Artifact{
		ID: "rev-artifact", MessageID: answer.ID, Filename: "result.png",
		StoragePath: "/tmp/revocation-artifact", MimeType: "image/png",
	})
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}

	if err := UpdateMessageContentForUser(ctx, db, conversation.ID, "member", legacyQuestion.ID, json.RawMessage(`[{"kind":"text","text":"forged"}]`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member edit legacy creator question = %v, want ErrNotFound", err)
	}
	if err := UpdateMessageContentForUser(ctx, db, conversation.ID, "creator", legacyQuestion.ID, json.RawMessage(`[{"kind":"text","text":"revocation phrase edited"}]`)); err != nil {
		t.Fatalf("creator edit own legacy question before kick: %v", err)
	}
	if _, err := SetMessageFeedbackForUser(ctx, db, MessageFeedback{
		MessageID: answer.ID, ConversationID: conversation.ID, UserID: "creator",
		Rating: MessageFeedbackLike,
	}); err != nil {
		t.Fatalf("restore feedback after question edit: %v", err)
	}
	if err := SetConvProviderStateKeyForUser(ctx, db, conversation.ID, providerStateMessage.ID, "creator", "sandbox_id", "before-kick"); err != nil {
		t.Fatalf("set provider state before kick: %v", err)
	}
	share, err := CreateShare(ctx, db, "creator", conversation.ID, conversation.Title, []byte(`[]`))
	if err != nil {
		t.Fatalf("current creator create share: %v", err)
	}
	if _, err := GetShareByToken(ctx, db, share.ID); err != nil {
		t.Fatalf("share before kick: %v", err)
	}

	leaveConversation, err := CreateConversation(ctx, db, Conversation{
		ID: "leave-conv", UserID: "leaver", WorkspaceID: workspace.ID, IsPublic: true, Title: "Leave secret",
	})
	if err != nil {
		t.Fatalf("create leave conversation: %v", err)
	}
	leaveStreaming, err := CreateMessageForUser(ctx, db, Message{
		ID: "leave-streaming", ConversationID: leaveConversation.ID,
		Role: "assistant", AuthorID: "leaver", Blocks: json.RawMessage(`[]`), Status: "streaming",
	}, "leaver")
	if err != nil {
		t.Fatalf("create leave streaming placeholder: %v", err)
	}
	leaveShare, err := CreateShare(ctx, db, "leaver", leaveConversation.ID, "leave share", []byte(`[]`))
	if err != nil {
		t.Fatalf("create leave share: %v", err)
	}
	if err := LeaveWorkspace(ctx, db, workspace.ID, "leaver"); err != nil {
		t.Fatalf("leave workspace: %v", err)
	}
	if _, err := GetConversation(ctx, db, leaveConversation.ID, "leaver"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("leaver GetConversation = %v, want ErrNotFound", err)
	}
	if _, err := GetShareByToken(ctx, db, leaveShare.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("leaver share after leave: %v", err)
	}
	if _, err := JoinWorkspaceByInviteToken(ctx, db, originalInviteToken, "leaver"); err != nil {
		t.Fatalf("leaver rejoin: %v", err)
	}
	if err := FinishMessageForUser(ctx, db, leaveStreaming.ID, leaveConversation.ID, "leaver", MessageFinishPatch{
		Blocks:    json.RawMessage(`[{"kind":"text","text":"resurrected after leave"}]`),
		Citations: json.RawMessage(`[]`), Status: "complete",
	}); !errors.Is(err, ErrConversationAccessRevoked) {
		t.Fatalf("old leave generation after rejoin error=%v, want ErrConversationAccessRevoked", err)
	}
	if stopped, err := GetMessage(ctx, db, leaveStreaming.ID); err != nil || stopped.Status != "stopped" || string(stopped.Blocks) != "[]" {
		t.Fatalf("leave generation resurrected after rejoin: %#v err=%v", stopped, err)
	}
	if _, err := GetShareByToken(ctx, db, leaveShare.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("leaver share revived after rejoin: %v", err)
	}
	if err := LeaveWorkspace(ctx, db, workspace.ID, "leaver"); err != nil {
		t.Fatalf("leaver second leave: %v", err)
	}

	if err := RemoveWorkspaceMember(ctx, db, workspace.ID, "creator"); err != nil {
		t.Fatalf("kick creator: %v", err)
	}

	assertNotFound := func(label string, err error) {
		t.Helper()
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s error=%v, want ErrNotFound", label, err)
		}
	}
	_, err = GetConversation(ctx, db, conversation.ID, "creator")
	assertNotFound("GetConversation", err)
	_, err = UpdateConversation(ctx, db, conversation.ID, "creator", ConversationPatch{Title: stringPtr("forged")})
	assertNotFound("UpdateConversation", err)
	_, err = DeleteConversationWithState(ctx, db, conversation.ID, "creator")
	assertNotFound("DeleteConversationWithState", err)
	_, err = DeleteRound(ctx, db, conversation.ID, "creator", legacyQuestion.ID)
	assertNotFound("DeleteRound", err)
	assertNotFound("UpdateMessageContentForUser", UpdateMessageContentForUser(ctx, db, conversation.ID, "creator", legacyQuestion.ID, json.RawMessage(`[{"kind":"text","text":"forged"}]`)))
	_, err = CreateMessageForUser(ctx, db, Message{
		ConversationID: conversation.ID, Role: "user", AuthorID: "creator",
		Blocks: json.RawMessage(`[{"kind":"text","text":"forged"}]`),
	}, "creator")
	assertNotFound("CreateMessageForUser", err)
	if err := SetConvProviderStateKeyForUser(ctx, db, conversation.ID, providerStateMessage.ID, "creator", "sandbox_id", "after-kick"); !errors.Is(err, ErrConversationAccessRevoked) {
		t.Fatalf("SetConvProviderStateKeyForUser error=%v, want ErrConversationAccessRevoked", err)
	}
	if state, err := GetConvProviderStateKey(ctx, db, conversation.ID, "sandbox_id"); err != nil || state != "before-kick" {
		t.Fatalf("revoked provider-state write changed value=%q err=%v", state, err)
	}

	_, err = GetProject(ctx, db, project.ID, "creator")
	assertNotFound("GetProject", err)
	_, err = UpdateProject(ctx, db, project.ID, "creator", ProjectPatch{Name: stringPtr("forged")})
	assertNotFound("UpdateProject", err)
	assertNotFound("DeleteProject", DeleteProject(ctx, db, project.ID, "creator"))
	_, err = GetKB(ctx, db, kb.ID, "creator")
	assertNotFound("GetKB", err)
	assertNotFound("DeleteKB", DeleteKB(ctx, db, kb.ID, "creator"))
	_, err = GetFile(ctx, db, file.ID, "creator")
	assertNotFound("GetFile", err)
	assertNotFound("DeleteConversationFile", DeleteConversationFile(ctx, db, file.ID, conversation.ID, "creator"))
	_, err = GetDocumentForUser(ctx, db, document.ID, "creator")
	assertNotFound("GetDocumentForUser", err)
	assertNotFound("RenameDocumentForUser", RenameDocumentForUser(ctx, db, document.ID, "kb", kb.ID, "creator", "forged.txt"))
	assertNotFound("DeleteDocumentForUser", DeleteDocumentForUser(ctx, db, document.ID, "kb", kb.ID, "creator"))
	assertNotFound("RetryDocumentForUser", RetryDocumentForUser(ctx, db, retryDocument.ID, conversation.ID, "creator"))
	assertNotFound("PromoteDocumentToKB", PromoteDocumentToKB(ctx, db, retryDocument.ID, kb.ID, "creator"))
	_, err = SetMessageFeedbackForUser(ctx, db, MessageFeedback{
		MessageID: answer.ID, ConversationID: conversation.ID, UserID: "creator",
		Rating: MessageFeedbackDislike, Reasons: []string{"incorrect_fact"},
	})
	assertNotFound("SetMessageFeedbackForUser", err)
	if retained, err := GetDocument(ctx, db, retryDocument.ID); err != nil || retained.Status != "failed" || retained.ConversationID != conversation.ID || retained.KBID != "" {
		t.Fatalf("revoked retry/promote changed document: %#v err=%v", retained, err)
	}
	if retained, err := GetMessageFeedbackForUser(ctx, db, answer.ID, "creator"); err != nil || retained.Rating != MessageFeedbackLike {
		t.Fatalf("revoked feedback changed row: %#v err=%v", retained, err)
	}
	_, err = GetArtifact(ctx, db, artifact.ID, "creator")
	assertNotFound("GetArtifact", err)

	if rows, err := ListWorkspaceConversationsForUser(ctx, db, workspace.ID, "", "any", "creator", 100, 0); err != nil || len(rows) != 0 {
		t.Fatalf("creator conversation list=%#v err=%v, want empty", rows, err)
	}
	if rows, err := ListWorkspaceProjectsForUser(ctx, db, workspace.ID, "creator"); err != nil || len(rows) != 0 {
		t.Fatalf("creator project list=%#v err=%v, want empty", rows, err)
	}
	if rows, err := ListWorkspaceKBsForUser(ctx, db, workspace.ID, "creator"); err != nil || len(rows) != 0 {
		t.Fatalf("creator KB list=%#v err=%v, want empty", rows, err)
	}
	if rows, err := ListFilesByConversation(ctx, db, conversation.ID, "creator"); err != nil || len(rows) != 0 {
		t.Fatalf("creator file list=%#v err=%v, want empty", rows, err)
	}
	if rows, err := ListDocumentsForUser(ctx, db, "kb", kb.ID, "creator"); err != nil || len(rows) != 0 {
		t.Fatalf("creator document list=%#v err=%v, want empty", rows, err)
	}
	if ids := OwnedKBIDs(ctx, db, "creator", workspace.ID, []string{kb.ID}); len(ids) != 0 {
		t.Fatalf("creator OwnedKBIDs=%v, want empty", ids)
	}
	if titles, messages, err := SearchConversations(ctx, db, "creator", workspace.ID, "revocation", 10, 10); err != nil || len(titles) != 0 || len(messages) != 0 {
		t.Fatalf("creator search titles=%#v messages=%#v err=%v, want empty", titles, messages, err)
	}
	if _, err := GetProjectByName(ctx, db, "creator", project.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetProjectByName exposed workspace project: %v", err)
	}
	if _, err := GetKBByName(ctx, db, "creator", kb.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetKBByName exposed workspace KB: %v", err)
	}
	if n, err := CountProjectsByUser(ctx, db, "creator"); err != nil || n != 0 {
		t.Fatalf("CountProjectsByUser=%d err=%v, want 0", n, err)
	}
	if n, err := CountStandaloneKBsByUser(ctx, db, "creator"); err != nil || n != 0 {
		t.Fatalf("CountStandaloneKBsByUser=%d err=%v, want 0", n, err)
	}
	if n, err := CountAdminFiles(ctx, db, AdminFileFilter{UserID: "creator", AccessUserID: "creator"}); err != nil || n != 0 {
		t.Fatalf("user-scoped retained file inventory=%d err=%v, want 0", n, err)
	}
	if n, err := CountAdminFiles(ctx, db, AdminFileFilter{UserID: "creator"}); err != nil || n == 0 {
		t.Fatalf("admin retained file inventory=%d err=%v, want retained rows", n, err)
	}
	if used, err := UserStorageUsage(ctx, db, "creator"); err != nil || used != 0 {
		t.Fatalf("UserStorageUsage after kick=%d err=%v, want 0", used, err)
	}
	if used, err := UserStorageUsage(ctx, db, "owner"); err != nil || used != 17 {
		t.Fatalf("owner retained workspace usage=%d err=%v, want 17", used, err)
	}
	if n, err := CountAdminFiles(ctx, db, AdminFileFilter{BillingUserID: "owner", AccessUserID: "owner"}); err != nil || n < 2 {
		t.Fatalf("owner billing inventory=%d err=%v, want retained file and direct document", n, err)
	}

	if allowed, err := CanManageConversationShare(ctx, db, conversation.ID, "creator"); err != nil || allowed {
		t.Fatalf("creator CanManageConversationShare=%v err=%v, want false", allowed, err)
	}
	_, err = GetShareByToken(ctx, db, share.ID)
	assertNotFound("GetShareByToken after publisher kick", err)
	_, err = GetWorkspaceByInviteToken(ctx, db, originalInviteToken)
	assertNotFound("GetWorkspaceByInviteToken after kick rotation", err)
	_, err = JoinWorkspaceByInviteToken(ctx, db, originalInviteToken, "creator")
	assertNotFound("JoinWorkspaceByInviteToken with revoked token", err)
	_, err = CreateShare(ctx, db, "creator", conversation.ID, "forged", []byte(`[]`))
	assertNotFound("CreateShare", err)
	assertNotFound("DeleteShareByConversation", DeleteShareByConversation(ctx, db, conversation.ID, "creator"))

	_, err = CreateConversation(ctx, db, Conversation{UserID: "creator", WorkspaceID: workspace.ID, Title: "forged"})
	assertNotFound("CreateConversation", err)
	_, err = CreateProject(ctx, db, Project{UserID: "creator", WorkspaceID: workspace.ID, Name: "forged project"})
	assertNotFound("CreateProject", err)
	_, err = CreateKB(ctx, db, KnowledgeBase{UserID: "creator", WorkspaceID: workspace.ID, Name: "forged kb", EmbeddingModelID: "rev-emb", EmbeddingDim: 3})
	assertNotFound("CreateKB", err)
	_, err = CreateFile(ctx, db, File{UserID: "creator", ConversationID: conversation.ID, Filename: "forged", StoragePath: "/tmp/forged"})
	assertNotFound("CreateFile", err)
	_, err = CreateDocumentForUser(ctx, db, Document{KBID: kb.ID, Filename: "forged", StoragePath: "/tmp/forged-doc"}, "creator")
	assertNotFound("CreateDocumentForUser", err)

	// Remaining members retain shared content, while the canonical owner works
	// even when a legacy database is missing its redundant owner membership row.
	if _, err := GetConversation(ctx, db, conversation.ID, "member"); err != nil {
		t.Fatalf("member conversation after kick: %v", err)
	}
	if _, err := GetProject(ctx, db, project.ID, "member"); err != nil {
		t.Fatalf("member project after kick: %v", err)
	}
	if _, err := GetKB(ctx, db, kb.ID, "member"); err != nil {
		t.Fatalf("member KB after kick: %v", err)
	}
	if _, err := GetFile(ctx, db, file.ID, "member"); err != nil {
		t.Fatalf("member file after kick: %v", err)
	}
	if _, err := GetDocumentForUser(ctx, db, document.ID, "member"); err != nil {
		t.Fatalf("member document after kick: %v", err)
	}
	if _, err := GetArtifact(ctx, db, artifact.ID, "member"); err != nil {
		t.Fatalf("member artifact after kick: %v", err)
	}
	// A fresh owner-issued capability may let the creator rejoin under the product
	// policy, but the public share capability revoked by the kick must not revive.
	rotatedWorkspace, err := GetWorkspace(ctx, db, workspace.ID)
	if err != nil || rotatedWorkspace.InviteToken == originalInviteToken {
		t.Fatalf("rotated workspace=%#v err=%v", rotatedWorkspace, err)
	}
	if _, err := JoinWorkspaceByInviteToken(ctx, db, rotatedWorkspace.InviteToken, "creator"); err != nil {
		t.Fatalf("rejoin with fresh token: %v", err)
	}
	if err := SetConvProviderStateKeyForUser(ctx, db, conversation.ID, providerStateMessage.ID, "creator", "sandbox_id", "after-rejoin"); !errors.Is(err, ErrConversationAccessRevoked) {
		t.Fatalf("old provider-state generation after rejoin error=%v, want ErrConversationAccessRevoked", err)
	}
	if err := FinishMessageForUser(ctx, db, providerStateMessage.ID, conversation.ID, "creator", MessageFinishPatch{
		Blocks:    json.RawMessage(`[{"kind":"text","text":"resurrected"}]`),
		Citations: json.RawMessage(`[]`), Status: "complete",
	}); !errors.Is(err, ErrConversationAccessRevoked) {
		t.Fatalf("old generation finish after rejoin error=%v, want ErrConversationAccessRevoked", err)
	}
	if stopped, err := GetMessage(ctx, db, providerStateMessage.ID); err != nil || stopped.Status != "stopped" || string(stopped.Blocks) != "[]" {
		t.Fatalf("old generation resurrected after rejoin: %#v err=%v", stopped, err)
	}
	if _, err := GetShareByToken(ctx, db, share.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("kicked publisher share revived after rejoin: %v", err)
	}
	if err := RemoveWorkspaceMember(ctx, db, workspace.ID, "creator"); err != nil {
		t.Fatalf("kick rejoined creator: %v", err)
	}
	exec(t, db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id='owner'`, workspace.ID)
	if _, err := GetConversation(ctx, db, conversation.ID, "owner"); err != nil {
		t.Fatalf("canonical owner fallback: %v", err)
	}
	ownerShare, err := CreateShare(ctx, db, "owner", conversation.ID, "owner share", []byte(`[]`))
	if err != nil {
		t.Fatalf("canonical owner create share: %v", err)
	}
	if _, err := GetShareByToken(ctx, db, ownerShare.ID); err != nil {
		t.Fatalf("canonical owner share token: %v", err)
	}
}

func TestWorkspaceKickSerializesAgainstInviteJoin(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "workspace-kick-join.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, id := range []string{"owner", "member"} {
		exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES(?,?, 'h','user','active')`, id, id+"@example.test")
	}
	workspace, err := CreateWorkspace(ctx, db, "owner", "Race")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := JoinWorkspace(ctx, db, workspace.ID, "member"); err != nil {
		t.Fatalf("initial join: %v", err)
	}
	conversation, err := CreateConversation(ctx, db, Conversation{
		ID: "race-conv", UserID: "member", WorkspaceID: workspace.ID, IsPublic: true, Title: "Race",
	})
	if err != nil {
		t.Fatalf("create race conversation: %v", err)
	}

	start := make(chan struct{})
	joinResult := make(chan error, 1)
	kickResult := make(chan error, 1)
	type shareAttempt struct {
		share *Share
		err   error
	}
	shareResult := make(chan shareAttempt, 1)
	go func() {
		<-start
		_, err := JoinWorkspaceByInviteToken(ctx, db, workspace.InviteToken, "member")
		joinResult <- err
	}()
	go func() {
		<-start
		kickResult <- RemoveWorkspaceMember(ctx, db, workspace.ID, "member")
	}()
	go func() {
		<-start
		share, err := CreateShare(ctx, db, "member", conversation.ID, "race", []byte(`[]`))
		shareResult <- shareAttempt{share: share, err: err}
	}()
	close(start)
	if err := <-kickResult; err != nil {
		t.Fatalf("kick: %v", err)
	}
	if err := <-joinResult; err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatalf("concurrent join: %v", err)
	}
	shareAttemptResult := <-shareResult
	if shareAttemptResult.err != nil && !errors.Is(shareAttemptResult.err, ErrNotFound) {
		t.Fatalf("concurrent share: %v", shareAttemptResult.err)
	}
	if shareAttemptResult.share != nil {
		if _, err := GetShareByToken(ctx, db, shareAttemptResult.share.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("concurrent publisher share survived kick: %v", err)
		}
	}
	var liveShares int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM conversation_shares WHERE conversation_id=?`, conversation.ID,
	).Scan(&liveShares); err != nil || liveShares != 0 {
		t.Fatalf("live shares after kick=%d err=%v", liveShares, err)
	}
	if role, err := IsWorkspaceMember(ctx, db, workspace.ID, "member"); err != nil || role != "" {
		t.Fatalf("membership after join/kick race role=%q err=%v", role, err)
	}
	if _, err := GetWorkspaceByInviteToken(ctx, db, workspace.InviteToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old invite after join/kick race: %v", err)
	}
}

func stringPtr(value string) *string { return &value }
