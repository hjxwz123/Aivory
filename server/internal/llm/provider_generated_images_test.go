package llm

import (
	"context"
	"errors"
	"testing"
)

type providerImageRegistryStub struct {
	calls  int
	failAt int
}

func (r *providerImageRegistryStub) List(string) []ToolDef { return nil }

func (r *providerImageRegistryStub) Run(context.Context, string, []byte, *ToolContext) (string, []Citation, error) {
	return "", nil, nil
}

func (r *providerImageRegistryStub) SaveArtifact(_ context.Context, tc *ToolContext, name, mime string, data []byte) error {
	r.calls++
	if r.calls == r.failAt {
		return errors.New("artifact store unavailable")
	}
	if tc.OnArtifact != nil {
		tc.OnArtifact(ArtifactRef{ID: name, Filename: name, MimeType: mime, Size: int64(len(data))})
	}
	return nil
}

func TestPersistProviderGeneratedImagesReportsPartialSuccessAndArtifactEvents(t *testing.T) {
	registry := &providerImageRegistryStub{failAt: 2}
	orchestrator := &Orchestrator{tools: registry}
	events := []ArtifactRef{}
	tc := &ToolContext{OnArtifact: func(artifact ArtifactRef) { events = append(events, artifact) }}
	images := []GeneratedImage{
		{Data: testPNGBytes(8), MimeType: "image/png"},
		{Data: testPNGBytes(16), MimeType: "image/png"},
	}

	persisted, err := orchestrator.persistProviderGeneratedImages(context.Background(), tc, images)
	if err == nil {
		t.Fatal("second artifact failure must be returned")
	}
	if persisted != 1 || len(events) != 1 || events[0].Filename != "image_1.png" {
		t.Fatalf("persisted=%d events=%#v", persisted, events)
	}
}
