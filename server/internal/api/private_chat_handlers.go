package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"aivory/server/internal/llm"
	"aivory/server/internal/sse"
	"aivory/server/internal/store"
)

const privateChatBodyLimit int64 = 32 << 20
const privateChatImageLimit = 5 << 20

type privateChatImage struct {
	Data     string `json:"data"`
	MimeType string `json:"mime_type"`
}

type privateChatMessage struct {
	Role   string             `json:"role"`
	Text   string             `json:"text"`
	Images []privateChatImage `json:"images,omitempty"`
}

type privateChatRequest struct {
	ModelID  string               `json:"model_id"`
	Messages []privateChatMessage `json:"messages"`
}

func privateChatHistory(body privateChatRequest, model *store.Model, imageLimit int64) ([]llm.UnifiedMessage, error) {
	if len(body.Messages) == 0 || len(body.Messages) > 128 || body.Messages[len(body.Messages)-1].Role != "user" {
		return nil, errors.New("private_history_limit")
	}
	history := make([]llm.UnifiedMessage, 0, len(body.Messages))
	textBytes := 0
	imageCount := 0
	for index, message := range body.Messages {
		role := "user"
		if index%2 != 0 {
			role = "assistant"
		}
		if message.Role != role || (strings.TrimSpace(message.Text) == "" && len(message.Images) == 0) || !utf8.ValidString(message.Text) {
			return nil, errors.New("private_invalid_request")
		}
		textBytes += len(message.Text)
		if textBytes > 1<<20 {
			return nil, errors.New("private_history_limit")
		}
		entry := llm.UnifiedMessage{Role: role, Blocks: []llm.UnifiedBlock{}}
		if message.Text != "" {
			entry.Blocks = append(entry.Blocks, llm.UnifiedBlock{Kind: "text", Text: message.Text})
		}
		if len(message.Images) > 0 && (!model.Vision || role != "user") {
			return nil, errors.New("private_images_not_supported")
		}
		for _, image := range message.Images {
			imageCount++
			if imageCount > 16 || len(image.Data) > base64.StdEncoding.EncodedLen(int(imageLimit)) {
				return nil, errors.New("private_image_limit")
			}
			if image.MimeType != "image/png" && image.MimeType != "image/jpeg" && image.MimeType != "image/gif" && image.MimeType != "image/webp" {
				return nil, errors.New("private_image_invalid")
			}
			decoded, err := base64.StdEncoding.Strict().DecodeString(image.Data)
			if err != nil || len(decoded) == 0 || int64(len(decoded)) > imageLimit || http.DetectContentType(decoded) != image.MimeType {
				return nil, errors.New("private_image_invalid")
			}
			entry.Blocks = append(entry.Blocks, llm.UnifiedBlock{Kind: "image", Data: base64.StdEncoding.EncodeToString(decoded), MimeType: image.MimeType})
		}
		history = append(history, entry)
	}
	return history, nil
}

func privateChatHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("Pragma", "no-cache")
	if d.Orchestrator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "private_model_unavailable"})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, privateChatBodyLimit))
	defer r.Body.Close()
	if err != nil || !utf8.Valid(data) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "private_history_limit"})
		return
	}
	var body privateChatRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || strings.TrimSpace(body.ModelID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "private_invalid_request"})
		return
	}
	model, err := store.GetModel(r.Context(), d.DB, body.ModelID)
	if err != nil || !model.Enabled || model.Kind != "chat" || model.Fast {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "private_model_unavailable"})
		return
	}
	imageLimit := int64(privateChatImageLimit)
	if d.Config.MaxUploadBytes > 0 {
		imageLimit = min(imageLimit, uploadLimitBytes(d, "image"))
	}
	history, err := privateChatHistory(body, model, imageLimit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	stream := sse.New(w)
	if stream == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stream.Ping()
			}
		}
	}()
	defer func() {
		cancel()
		<-heartbeatDone
	}()
	emit := func(event llm.SseEvent) {
		if err := stream.Send(event, event.Type); err != nil {
			cancel()
		}
	}
	defer func() {
		if recover() != nil {
			emit(llm.SseEvent{Type: "error", Code: "private_provider_error", Message: "private_provider_error"})
		}
	}()
	if err := d.Orchestrator.RunPrivate(ctx, authUser(r).ID, model, history, emit); err != nil {
		emit(llm.SseEvent{Type: "error", Code: err.Error(), Message: err.Error()})
	}
}
