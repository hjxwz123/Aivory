package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMemoryStreamRevocationAtomicallyDeletesAndDenies(t *testing.T) {
	streamKey := "gen:test-message"
	tombstoneKey := "gen:test-message:revoked"
	workspaceKey := "workspace-generation-revoked:test-workspace"
	c := NewMemory()

	if _, allowed, ok := c.StreamAppendIfAllowed(streamKey, []string{tombstoneKey, workspaceKey}, "before", time.Hour); !ok || !allowed {
		t.Fatalf("initial append allowed=%v ok=%v", allowed, ok)
	}
	if !c.StreamRevoke(streamKey, tombstoneKey, time.Hour) {
		t.Fatal("stream revoke failed")
	}
	if rows, ok := c.StreamRead(streamKey, "", 10); !ok || len(rows) != 0 {
		t.Fatalf("raw stream survived revoke: rows=%v ok=%v", rows, ok)
	}
	if _, allowed, ok := c.StreamAppendIfAllowed(streamKey, []string{tombstoneKey, workspaceKey}, "after", time.Hour); !ok || allowed {
		t.Fatalf("post-revoke append allowed=%v ok=%v", allowed, ok)
	}
	if rows, allowed, ok := c.StreamReadIfAllowed(streamKey, []string{tombstoneKey, workspaceKey}, "", 10); !ok || allowed || len(rows) != 0 {
		t.Fatalf("post-revoke read rows=%v allowed=%v ok=%v", rows, allowed, ok)
	}

	// A different immutable message id is a fresh generation epoch and must not
	// inherit the old stream's tombstone after a legitimate membership rejoin.
	if _, allowed, ok := c.StreamAppendIfAllowed("gen:fresh-message", []string{"gen:fresh-message:revoked", workspaceKey}, "fresh", time.Hour); !ok || !allowed {
		t.Fatalf("fresh message inherited old tombstone: allowed=%v ok=%v", allowed, ok)
	}
}

func TestMemoryStreamAppendCannotWinAfterRevokeReturns(t *testing.T) {
	streamKey := "gen:concurrent-message"
	tombstoneKey := "gen:concurrent-message:revoked"
	c := NewMemory()
	start := make(chan struct{})
	var workers sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			for event := 0; event < 100; event++ {
				_, _, _ = c.StreamAppendIfAllowed(
					streamKey, []string{tombstoneKey}, fmt.Sprintf("%d-%d", worker, event), time.Hour,
				)
			}
		}(worker)
	}
	close(start)
	if !c.StreamRevoke(streamKey, tombstoneKey, time.Hour) {
		t.Fatal("stream revoke failed")
	}
	workers.Wait()
	if rows, ok := c.StreamRead(streamKey, "", 5000); !ok || len(rows) != 0 {
		t.Fatalf("events appeared after atomic revoke: count=%d ok=%v", len(rows), ok)
	}
}

func TestMemoryWorkspaceTombstoneDeniesGenerationStream(t *testing.T) {
	c := NewMemory()
	workspaceKey := "workspace-generation-revoked:deleted-workspace"
	c.Set(workspaceKey, "1", time.Hour)
	if _, allowed, ok := c.StreamAppendIfAllowed(
		"gen:workspace-message", []string{"gen:workspace-message:revoked", workspaceKey}, "secret", time.Hour,
	); !ok || allowed {
		t.Fatalf("workspace-revoked append allowed=%v ok=%v", allowed, ok)
	}
	if _, allowed, ok := c.StreamReadIfAllowed(
		"gen:workspace-message", []string{"gen:workspace-message:revoked", workspaceKey}, "", 10,
	); !ok || allowed {
		t.Fatalf("workspace-revoked read allowed=%v ok=%v", allowed, ok)
	}
}
