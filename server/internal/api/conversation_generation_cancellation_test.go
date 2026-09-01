package api

import (
	"context"
	"testing"
	"time"

	"aivory/server/internal/cache"
)

func TestConversationGenerationCancellationCoversPersonalConversations(t *testing.T) {
	memoryCache := cache.NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := newGenerationAccessRevocationWatcher(
		Deps{Cache: memoryCache}, ctx, cancel, "personal-conversation", "", nil, nil, nil,
	)
	defer watcher.close()

	cancelConversationGenerations(Deps{Cache: memoryCache}, "personal-conversation")

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("personal conversation generation was not canceled")
	}
}
