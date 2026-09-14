package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"aivory/server/internal/store"
)

// ImageEditRequest is persisted on the user turn, not in conversation-global
// provider state: retries must retain exactly the selected image and mask.
type ImageEditRequest struct {
	BaseArtifactID string `json:"base_artifact_id"`
	MaskFileID     string `json:"mask_file_id"`
}

var ErrImageMaskEdit = errors.New("invalid_image_mask_edit")

func SupportsImageMaskEdit(model *store.Model, channel *store.Channel) bool {
	return model != nil && channel != nil && model.Kind == "image" && model.Enabled && channel.Enabled &&
		strings.EqualFold(channel.Type, "openai") && !strings.Contains(strings.ToLower(model.RequestID), "dall-e-3")
}

func ImageEditFromBlocks(raw json.RawMessage) (*ImageEditRequest, error) {
	var blocks []UnifiedBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	for _, block := range blocks {
		if block.Kind != "image_edit" {
			continue
		}
		var edit ImageEditRequest
		if json.Unmarshal(block.Input, &edit) != nil || edit.BaseArtifactID == "" || edit.MaskFileID == "" {
			return nil, ErrImageMaskEdit
		}
		return &edit, nil
	}
	return nil, nil
}

// Repeat this at execution time, including retries. An accessible artifact from
// a different conversation or sibling branch is not a valid edit source.
func ValidateImageEditRequest(ctx context.Context, db *sql.DB, convID, userID, leafID string, model *store.Model, edit *ImageEditRequest) error {
	if edit == nil {
		return nil
	}
	if model == nil || userID == "" || edit.BaseArtifactID == "" || edit.MaskFileID == "" {
		return ErrImageMaskEdit
	}
	visibleFiles, err := store.ListFilesByConversationBranch(ctx, db, convID, userID, leafID)
	if err != nil {
		return ErrImageMaskEdit
	}
	maskVisible := false
	for _, file := range visibleFiles {
		if file.ID == edit.MaskFileID {
			maskVisible = true
			break
		}
	}
	if !maskVisible {
		return ErrImageMaskEdit
	}
	channel, err := store.GetChannel(ctx, db, model.ChannelID)
	if err != nil || !SupportsImageMaskEdit(model, channel) {
		return ErrImageMaskEdit
	}
	artifact, err := store.GetArtifact(ctx, db, edit.BaseArtifactID, userID)
	if err != nil || !strings.HasPrefix(artifact.MimeType, "image/") {
		return ErrImageMaskEdit
	}
	switch artifact.Source {
	case "", store.ArtifactSourceImageGenerate, store.ArtifactSourceHostedImageGeneration:
	default:
		return ErrImageMaskEdit
	}
	seen := map[string]bool{}
	found := false
	for leafID != "" && !seen[leafID] {
		seen[leafID] = true
		message, err := store.GetMessage(ctx, db, leafID)
		if err != nil || message.ConversationID != convID {
			return ErrImageMaskEdit
		}
		if message.ID == artifact.MessageID {
			found = true
			break
		}
		leafID = message.ParentID
	}
	if !found {
		return ErrImageMaskEdit
	}
	mask, err := store.GetFile(ctx, db, edit.MaskFileID, userID)
	if err != nil || mask.ConversationID != convID || mask.MimeType != "image/png" {
		return ErrImageMaskEdit
	}
	return nil
}
