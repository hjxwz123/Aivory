package api

import (
	"context"

	"aivory/server/internal/store"
)

// imageModelConfigured reports whether the local image_generate tool has an
// enabled image model. Prefer the live tool registry because it owns the same
// runtime check used when tools are declared and executed.
func imageModelConfigured(d Deps) bool {
	if d.Tools != nil {
		return d.Tools.ImageGenerationConfigured()
	}
	if d.DB == nil {
		return false
	}
	models, err := store.ListModels(context.Background(), d.DB, "image", true)
	return err == nil && len(models) > 0
}
