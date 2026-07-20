package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

var (
	errNoModelConfigured      = errors.New("no model configured")
	errSelectedModelInvalid   = errors.New("selected model is unavailable")
	errImageInputUnsupported  = errors.New("selected model does not support image input")
	errImageConversationScope = errors.New("conversation_id is required for image uploads")
	errAttachmentIDRequired   = errors.New("attachment id is required")
	errAttachmentUnavailable  = errors.New("attachment not found in conversation")
)

const storedAttachmentSniffBytes = 4096

// resolveEffectiveConversationModel mirrors the model precedence used by a
// chat turn. Fast mode resolves the hidden model server-side and ignores every
// client-visible model id. Otherwise the current request wins, followed by the
// conversation's saved picker value and finally the global default.
func resolveEffectiveConversationModel(
	ctx context.Context,
	db *sql.DB,
	conv *store.Conversation,
	requestedModelID string,
	fast bool,
) (*store.Model, error) {
	if fast {
		fastModel, err := store.GetFastModel(ctx, db)
		if err != nil {
			return nil, fmt.Errorf("resolve fast model: %w", err)
		}
		if fastModel != nil {
			return fastModel, nil
		}
	}

	modelID := strings.TrimSpace(requestedModelID)
	if modelID == "" && conv != nil {
		modelID = strings.TrimSpace(conv.ModelID)
	}
	if modelID == "" {
		raw, err := store.GetSetting(db, "default_model_id")
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("load default model: %w", err)
		}
		if err == nil {
			_ = json.Unmarshal(raw, &modelID)
			modelID = strings.TrimSpace(modelID)
		}
	}
	if modelID == "" {
		return nil, errNoModelConfigured
	}

	model, err := store.GetModel(ctx, db, modelID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w: model not found", errSelectedModelInvalid)
	}
	if err != nil {
		return nil, fmt.Errorf("load selected model: %w", err)
	}
	if !model.Enabled {
		return nil, fmt.Errorf("%w: model is disabled", errSelectedModelInvalid)
	}
	return model, nil
}

// modelSupportsImageInput is deliberately kind-aware. Image-generation models
// accept reference images regardless of their chat-only Vision flag; ordinary
// chat models accept images only when an administrator enabled Vision.
func modelSupportsImageInput(model *store.Model) bool {
	if model == nil {
		return false
	}
	return model.Kind == "image" || (model.Kind == "chat" && model.Vision)
}

// normalizeConversationAttachments replaces every client-controlled metadata
// field with the current conversation's files row. A bounded storage-prefix
// inspection repairs legacy rows whose kind/MIME predates server-side image
// classification and prevents a PDF or arbitrary large file from being sent to
// a vision provider merely because the request labels it as an image.
func normalizeConversationAttachments(
	ctx context.Context,
	db *sql.DB,
	conversationID string,
	userID string,
	attachments []llm.Attachment,
) ([]llm.Attachment, error) {
	if len(attachments) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		id := strings.TrimSpace(attachment.ID)
		if id == "" {
			return nil, errAttachmentIDRequired
		}
		ids = append(ids, id)
	}
	files, err := store.ConversationFilesByIDs(ctx, db, conversationID, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("load conversation attachments: %w", err)
	}
	normalized := make([]llm.Attachment, 0, len(attachments))
	for _, attachment := range attachments {
		id := strings.TrimSpace(attachment.ID)
		file, ok := files[id]
		if !ok {
			return nil, errAttachmentUnavailable
		}
		kind, mimeType := normalizedStoredFileType(file)
		normalized = append(normalized, llm.Attachment{
			ID:         file.ID,
			DocumentID: file.DocumentID,
			Filename:   file.Filename,
			MimeType:   mimeType,
			Kind:       kind,
			URL:        "/api/files/" + file.ID,
		})
	}
	return normalized, nil
}

// normalizedStoredFileType combines the persisted classification with a
// bounded signature check. Definite bytes win in both directions: actual image
// bytes promote an old text row to image, while definite PDF/text/binary bytes
// demote an incorrectly image-labelled legacy row. If storage cannot be read,
// conservative metadata fallback keeps old remote/missing rows usable.
func normalizedStoredFileType(file store.File) (kind, mimeType string) {
	kind = strings.ToLower(strings.TrimSpace(file.Kind))
	mimeType = normalizeStoredMIME(file.MimeType)
	metadataImage := kind == "image" || strings.HasPrefix(mimeType, "image/") || filenameLooksLikeImage(file.Filename)

	if sample, inspected := readStoredFilePrefix(file.StoragePath); inspected && len(sample) > 0 {
		if imageMIME := detectImageMIMEFromBytes(sample); imageMIME != "" {
			return "image", imageMIME
		}
		if metadataImage {
			detected := normalizeStoredMIME(http.DetectContentType(sample))
			switch detected {
			case "application/pdf":
				return "pdf", detected
			case "text/plain", "text/html", "text/xml", "application/xml":
				return "text", detected
			default:
				// The prefix was readable but is not a supported image signature.
				// Never let image-looking metadata alone inline arbitrary bytes.
				return "other", orStoredMIME(detected, "application/octet-stream")
			}
		}
	}

	if metadataImage {
		if !strings.HasPrefix(mimeType, "image/") {
			mimeType = detectUploadedImageMIME(file.Filename, "", nil)
		}
		return "image", orStoredMIME(mimeType, "application/octet-stream")
	}
	if kind == "" {
		kind = kindOf(mimeType, file.Filename)
	}
	if kind == "image" {
		// This is only reachable for inconsistent metadata where the explicit
		// signals above did not recognize an image; keep it out of providers.
		kind = "other"
	}
	return kind, orStoredMIME(mimeType, "application/octet-stream")
}

func readStoredFilePrefix(path string) ([]byte, bool) {
	path = strings.TrimSpace(path)
	if !looksLocalStoragePath(path) {
		return nil, false
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	sample, err := io.ReadAll(io.LimitReader(file, storedAttachmentSniffBytes))
	if err != nil {
		return nil, false
	}
	return sample, true
}

func filenameLooksLikeImage(filename string) bool {
	return detectUploadedImageMIME(filename, "", nil) != ""
}

func normalizeStoredMIME(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
}

func orStoredMIME(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func attachmentNormalizationErrorStatus(err error) int {
	if errors.Is(err, errAttachmentIDRequired) || errors.Is(err, errAttachmentUnavailable) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// ensureImageAttachmentsSupported performs the pre-SSE image gate for a turn.
// Its attachments must already have passed normalizeConversationAttachments;
// therefore Kind and MimeType are server-owned, byte-signature-aware values.
func ensureImageAttachmentsSupported(
	ctx context.Context,
	db *sql.DB,
	conv *store.Conversation,
	requestedModelID string,
	fast bool,
	attachments []llm.Attachment,
) error {
	if conv == nil {
		return errors.New("conversation required")
	}
	if len(attachments) == 0 {
		return nil
	}
	hasImage := false
	for _, attachment := range attachments {
		if attachment.Kind == "image" || strings.HasPrefix(normalizeStoredMIME(attachment.MimeType), "image/") {
			hasImage = true
			break
		}
	}
	if !hasImage {
		return nil
	}
	model, err := resolveEffectiveConversationModel(ctx, db, conv, requestedModelID, fast)
	if err != nil {
		return err
	}
	if !modelSupportsImageInput(model) {
		return errImageInputUnsupported
	}
	return nil
}

func imageCapabilityErrorStatus(err error) int {
	switch {
	case errors.Is(err, errImageConversationScope):
		return http.StatusBadRequest
	case errors.Is(err, errNoModelConfigured),
		errors.Is(err, errSelectedModelInvalid),
		errors.Is(err, errImageInputUnsupported):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// optionalBoolQuery preserves the difference between an omitted upload query
// and an explicit false value. Omission falls back to the conversation state.
func optionalBoolQuery(r *http.Request, key string) (value bool, provided bool, err error) {
	values, provided := r.URL.Query()[key]
	if !provided {
		return false, false, nil
	}
	if len(values) != 1 {
		return false, true, fmt.Errorf("%s must be a boolean", key)
	}
	switch strings.ToLower(strings.TrimSpace(values[0])) {
	case "1", "true":
		return true, true, nil
	case "0", "false":
		return false, true, nil
	default:
		return false, true, fmt.Errorf("%s must be a boolean", key)
	}
}
