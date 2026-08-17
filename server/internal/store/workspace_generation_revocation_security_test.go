package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestWorkspaceGenerationWritesStopAtMembershipRevocation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "workspace-generation-revocation.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, id := range []string{"generation-owner", "generation-member"} {
		exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES(?,?, 'h','user','active')`, id, id+"@example.test")
	}
	workspace, err := CreateWorkspace(ctx, db, "generation-owner", "Generation revocation")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := JoinWorkspace(ctx, db, workspace.ID, "generation-member"); err != nil {
		t.Fatalf("join member: %v", err)
	}
	conversation, err := CreateConversation(ctx, db, Conversation{
		ID: "generation-revocation-conversation", UserID: "generation-owner",
		WorkspaceID: workspace.ID, IsPublic: true, Title: "Generation revocation",
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	question, err := CreateMessageForUser(ctx, db, Message{
		ID: "generation-revocation-question", ConversationID: conversation.ID,
		Role: "user", AuthorID: "generation-member",
		Blocks: json.RawMessage(`[{
			"kind":"text","text":"question created while authorized"
		}]`),
	}, "generation-member")
	if err != nil {
		t.Fatalf("create question: %v", err)
	}
	completed, err := CreateMessageForUser(ctx, db, Message{
		ID: "generation-completed-before-revocation", ConversationID: conversation.ID,
		ParentID: question.ID, Role: "assistant", AuthorID: "generation-member",
		Blocks: json.RawMessage(`[]`), Status: "streaming",
	}, "generation-member")
	if err != nil {
		t.Fatalf("create completed placeholder: %v", err)
	}
	completedBlocks := json.RawMessage(`[{
		"kind":"text","text":"completed before revocation"
	}]`)
	if _, err := CreateArtifactForUser(ctx, db, Artifact{
		ID: "artifact-before-revocation", MessageID: completed.ID, Filename: "before.txt",
		StoragePath: "/tmp/artifact-before-revocation", MimeType: "text/plain", SizeBytes: 6,
	}, conversation.ID, "generation-member"); err != nil {
		t.Fatalf("artifact before revocation: %v", err)
	}
	if err := FinishMessageForUser(ctx, db, completed.ID, conversation.ID, "generation-member", MessageFinishPatch{
		Blocks: completedBlocks, Citations: json.RawMessage(`[]`), StopReason: "stop",
		OutputTokens: 4, Cost: 0.25, Status: "complete", GenMs: 12,
	}); err != nil {
		t.Fatalf("finish before revocation: %v", err)
	}

	streaming, err := CreateMessageForUser(ctx, db, Message{
		ID: "generation-streaming-at-revocation", ConversationID: conversation.ID,
		ParentID: question.ID, Role: "assistant", AuthorID: "generation-member",
		Blocks: json.RawMessage(`[]`), Status: "streaming",
	}, "generation-member")
	if err != nil {
		t.Fatalf("create streaming placeholder: %v", err)
	}
	if err := RemoveWorkspaceMember(ctx, db, workspace.ID, "generation-owner", "generation-member"); err != nil {
		t.Fatalf("revoke membership: %v", err)
	}
	// Rejoining grants a new authorization epoch. It must not revive placeholders
	// created by the membership that was just revoked.
	if err := JoinWorkspace(ctx, db, workspace.ID, "generation-member"); err != nil {
		t.Fatalf("rejoin member: %v", err)
	}

	secretPatch := MessageFinishPatch{
		Blocks: json.RawMessage(`[{
			"kind":"text","text":"must not persist after revocation"
		}]`),
		Raw:        json.RawMessage(`{"provider_secret":"must not persist"}`),
		Citations:  json.RawMessage(`[{"url":"https://secret.example"}]`),
		StopReason: "stop", InputTokens: 9, OutputTokens: 8,
		CacheReadTokens: 7, CacheWriteTokens: 6, Cost: 1.5, Credits: 3,
		Status: "complete", Error: "must not persist", GenMs: 99,
	}
	if err := FinishMessageForUser(ctx, db, streaming.ID, conversation.ID, "generation-member", secretPatch); !errors.Is(err, ErrConversationAccessRevoked) {
		t.Fatalf("finish after revocation error=%v, want ErrConversationAccessRevoked", err)
	}
	if err := SetMessageVerifyForUser(ctx, db, streaming.ID, conversation.ID, "generation-member", json.RawMessage(`{"verdict":"clean","secret":"must not persist"}`)); !errors.Is(err, ErrConversationAccessRevoked) {
		t.Fatalf("verify after revocation error=%v, want ErrConversationAccessRevoked", err)
	}
	if _, err := CreateArtifactForUser(ctx, db, Artifact{
		ID: "artifact-after-revocation", MessageID: streaming.ID, Filename: "after.txt",
		StoragePath: "/tmp/artifact-after-revocation", MimeType: "text/plain", SizeBytes: 5,
	}, conversation.ID, "generation-member"); !errors.Is(err, ErrConversationAccessRevoked) {
		t.Fatalf("artifact after revocation error=%v, want ErrConversationAccessRevoked", err)
	}

	persisted, err := GetMessage(ctx, db, streaming.ID)
	if err != nil {
		t.Fatalf("load scrubbed placeholder: %v", err)
	}
	if string(persisted.Blocks) != "[]" || len(persisted.Raw) != 0 || string(persisted.Citations) != "[]" {
		t.Fatalf("revoked output was retained: blocks=%s raw=%s citations=%s", persisted.Blocks, persisted.Raw, persisted.Citations)
	}
	if persisted.Status != "stopped" || persisted.StopReason != "stopped" || persisted.Error != "" {
		t.Fatalf("scrubbed status=%q stop=%q error=%q, want stopped/stopped/empty", persisted.Status, persisted.StopReason, persisted.Error)
	}
	if persisted.InputTokens != 0 || persisted.OutputTokens != 0 || persisted.CacheReadTokens != 0 ||
		persisted.CacheWriteTokens != 0 || persisted.Cost != 0 || persisted.Credits != 0 || len(persisted.Verify) != 0 {
		t.Fatalf("revoked accounting/audit retained: %+v", persisted)
	}
	var afterArtifacts int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE id='artifact-after-revocation'`).Scan(&afterArtifacts); err != nil {
		t.Fatalf("count rejected artifact: %v", err)
	}
	if afterArtifacts != 0 {
		t.Fatalf("artifact created after revocation: %d", afterArtifacts)
	}

	// A write that linearized before kick is retained, while the former member can
	// no longer use the same finalizer to overwrite it.
	before, err := GetMessage(ctx, db, completed.ID)
	if err != nil {
		t.Fatalf("load completed message: %v", err)
	}
	if string(before.Blocks) != string(completedBlocks) || before.Status != "complete" || before.OutputTokens != 4 || before.Cost != 0.25 {
		t.Fatalf("pre-revocation result changed: %+v", before)
	}
	if _, err := GetArtifact(ctx, db, "artifact-before-revocation", "generation-owner"); err != nil {
		t.Fatalf("owner lost pre-revocation artifact: %v", err)
	}

	freshQuestion, err := CreateMessageForUser(ctx, db, Message{
		ID: "generation-after-rejoin-question", ConversationID: conversation.ID,
		ParentID: streaming.ID, Role: "user", AuthorID: "generation-member",
		Blocks: json.RawMessage(`[{"kind":"text","text":"new membership generation"}]`),
	}, "generation-member")
	if err != nil {
		t.Fatalf("create fresh question after rejoin: %v", err)
	}
	freshAssistant, err := CreateMessageForUser(ctx, db, Message{
		ID: "generation-after-rejoin-assistant", ConversationID: conversation.ID,
		ParentID: freshQuestion.ID, Role: "assistant", AuthorID: "generation-member",
		Blocks: json.RawMessage(`[]`), Status: "streaming",
	}, "generation-member")
	if err != nil {
		t.Fatalf("create fresh assistant after rejoin: %v", err)
	}
	if err := FinishMessageForUser(ctx, db, freshAssistant.ID, conversation.ID, "generation-member", MessageFinishPatch{
		Blocks:    json.RawMessage(`[{"kind":"text","text":"fresh generation persisted"}]`),
		Citations: json.RawMessage(`[]`), StopReason: "stop", Status: "complete",
	}); err != nil {
		t.Fatalf("finish fresh generation after rejoin: %v", err)
	}
}
