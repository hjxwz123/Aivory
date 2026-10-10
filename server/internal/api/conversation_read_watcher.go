package api

import (
	"context"
	"time"

	"aivory/server/internal/store"
)

// A follower loses only its own connection when read access is revoked. It
// must never revoke or scrub a generation started by another workspace member.
func watchConversationReadAccess(d Deps, ctx context.Context, cancel context.CancelFunc, conversationID, workspaceID, userID string) func() {
	topics := []string{conversationGenerationCancellationTopic(conversationID),
		conversationGenerationRevocationTopic(conversationID), userPermissionRevocationTopic(userID)}
	if workspaceID != "" {
		topics = append(topics, workspaceGenerationRevocationTopic(workspaceID),
			workspaceMemberGenerationRevocationTopic(workspaceID, userID),
			workspacePolicyGenerationRevocationTopic(workspaceID))
	}
	unsubs := make([]func(), 0, len(topics))
	for _, topic := range topics {
		unsubs = append(unsubs, subscribeAccessRevocationTopic(d, ctx, cancel, topic))
	}
	allowed := func() bool {
		user, err := store.GetUserAuthState(ctx, d.DB, userID)
		if err != nil || user.Status != "active" {
			return false
		}
		_, err = store.GetConversation(ctx, d.DB, conversationID, userID)
		return err == nil
	}
	// Subscribe before checking, covering removal racing connection setup.
	if !allowed() {
		cancel()
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !allowed() {
					cancel()
					return
				}
			}
		}
	}()
	return func() {
		for _, unsub := range unsubs {
			unsub()
		}
	}
}
